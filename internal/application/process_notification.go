package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// Retry policy constants. CLAUDE.md §5 specifies 5 attempts with exponential
// backoff (30s * 2^(attempt-1) + jitter). Jitter is omitted here so the
// state-machine behavior is deterministic for tests; the asynq adapter
// applies its own retry schedule on top, so this is best-effort scheduling.
const (
	defaultMaxAttempts = 5
	backoffBase        = 30 * time.Second
	rateLimitBackoff   = 1 * time.Second
)

// ProcessNotificationInput is the parameter bundle for ProcessNotification.
// Workers receive only the notification id from the queue payload; the use
// case loads the full notification through the atomic claim.
type ProcessNotificationInput struct {
	NotificationID domain.NotificationID
}

// ProcessNotification is the worker-side use case. It owns the atomic claim,
// the outbound rate-limit check, the provider call, and the resulting status
// transition (delivered / failed / retrying). The atomic claim is the
// load-bearing defense against double-sends from concurrent workers or
// asynq redeliveries (CLAUDE.md §3.10, ADR-0009).
type ProcessNotification struct {
	repo        ports.NotificationRepository
	logRepo     ports.NotificationLogRepository
	provider    ports.Provider
	rateLimiter ports.RateLimiter
	broadcaster ports.StatusBroadcaster
	idGen       ports.IDGenerator
	clock       ports.Clock
	metrics     MetricsRecorder
}

// NewProcessNotification wires the dependencies. Every port is
// required except metricsRec — tests pass nil to skip emit.
func NewProcessNotification(
	repo ports.NotificationRepository,
	logRepo ports.NotificationLogRepository,
	provider ports.Provider,
	rateLimiter ports.RateLimiter,
	broadcaster ports.StatusBroadcaster,
	idGen ports.IDGenerator,
	clock ports.Clock,
	metricsRec MetricsRecorder,
) *ProcessNotification {
	return &ProcessNotification{
		repo:        repo,
		logRepo:     logRepo,
		provider:    provider,
		rateLimiter: rateLimiter,
		broadcaster: broadcaster,
		idGen:       idGen,
		clock:       clock,
		metrics:     metricsRec,
	}
}

// Execute runs the worker-side processing flow:
//
//   - Atomically claim the notification (queued|retrying → processing).
//     ErrAlreadyClaimed is swallowed silently — another worker or a
//     redelivery beat us to it and there is nothing else to do.
//   - Record the processing transition (log + broadcast).
//   - Check the outbound rate limit; if denied, fall the notification into
//     retrying with a short backoff so asynq re-delivers it shortly.
//   - Call the provider.
//   - Apply the DeliveryResult: delivered, failed (permanent or retries
//     exhausted), or retrying with exponential backoff.
func (uc *ProcessNotification) Execute(ctx context.Context, in ProcessNotificationInput) error {
	start := uc.clock.Now()
	claimed, err := uc.repo.ClaimForProcessing(ctx, in.NotificationID, start)
	if err != nil {
		if errors.Is(err, ports.ErrAlreadyClaimed) {
			return nil // no-op — another worker won the claim race
		}
		return fmt.Errorf("claim notification %s: %w", in.NotificationID, err)
	}

	if err := uc.recordEvent(ctx, claimed, domain.LogEventProcessing); err != nil {
		return err
	}

	allowed, _, err := uc.rateLimiter.Allow(ctx, "channel:"+string(claimed.Channel))
	if err != nil {
		return fmt.Errorf("rate limiter: %w", err)
	}
	if !allowed {
		return uc.rescheduleForRateLimit(ctx, claimed)
	}

	result := uc.provider.Send(ctx, claimed.Channel, claimed.Recipient, claimed.Content)
	if uc.metrics != nil {
		uc.metrics.NotificationAttempt(string(claimed.Channel), attemptOutcome(result))
	}
	return uc.applyResult(ctx, claimed, result, start)
}

// attemptOutcome maps a DeliveryResult onto the three labels the
// notifications_attempts_total counter exposes.
func attemptOutcome(result domain.DeliveryResult) string {
	switch {
	case result.Success:
		return "success"
	case result.Retryable:
		return "transient"
	default:
		return "permanent"
	}
}

// applyResult maps the provider response onto the state machine.
// start is when the worker first claimed the notification — used to
// stamp the processing-duration histogram.
func (uc *ProcessNotification) applyResult(ctx context.Context, n *domain.Notification, result domain.DeliveryResult, start time.Time) error {
	now := uc.clock.Now()

	// Every terminal branch records a processing-duration sample so
	// p99 / p95 dashboards capture even retries that eventually
	// failed. Sampled here (not in Execute) because the rate-limit
	// path returns earlier without a provider call.
	defer func() {
		if uc.metrics != nil {
			uc.metrics.ObserveProcessing(string(n.Channel), now.Sub(start))
		}
	}()

	switch {
	case result.Success:
		if err := n.MarkDelivered(now); err != nil {
			return err
		}
		if err := uc.repo.UpdateStatus(ctx, n, domain.StatusProcessing); err != nil {
			return fmt.Errorf("update status (delivered): %w", err)
		}
		if uc.metrics != nil {
			uc.metrics.NotificationDelivered(string(n.Channel))
		}
		return uc.recordEvent(ctx, n, domain.LogEventDelivered)

	case !result.Retryable || n.Attempts >= defaultMaxAttempts:
		// Permanent failure, or retries exhausted — terminal.
		if err := n.MarkFailed(now, result.Reason); err != nil {
			return err
		}
		if err := uc.repo.UpdateStatus(ctx, n, domain.StatusProcessing); err != nil {
			return fmt.Errorf("update status (failed): %w", err)
		}
		if uc.metrics != nil {
			reason := result.Reason
			if reason == "" {
				reason = "unspecified"
			}
			uc.metrics.NotificationFailed(string(n.Channel), reason)
		}
		return uc.recordEvent(ctx, n, domain.LogEventFailed)

	default:
		// Transient failure with attempts remaining — schedule a retry.
		nextRetryAt := now.Add(backoffFor(n.Attempts))
		if err := n.MarkRetrying(now, result.Reason, nextRetryAt); err != nil {
			return err
		}
		if err := uc.repo.UpdateStatus(ctx, n, domain.StatusProcessing); err != nil {
			return fmt.Errorf("update status (retrying): %w", err)
		}
		return uc.recordEvent(ctx, n, domain.LogEventRetrying)
	}
}

// rescheduleForRateLimit moves the notification into retrying with a short
// backoff. The asynq adapter respects NextRetryAt; the next delivery will
// re-run Execute and re-check the limiter.
func (uc *ProcessNotification) rescheduleForRateLimit(ctx context.Context, n *domain.Notification) error {
	now := uc.clock.Now()
	if err := n.MarkRetrying(now, "outbound rate limit exceeded", now.Add(rateLimitBackoff)); err != nil {
		return err
	}
	if err := uc.repo.UpdateStatus(ctx, n, domain.StatusProcessing); err != nil {
		return fmt.Errorf("update status (rate limited): %w", err)
	}
	return uc.recordEvent(ctx, n, domain.LogEventRetrying)
}

// recordEvent writes a notification_logs row for the current status and
// publishes a status update to the WebSocket fan-out backbone.
func (uc *ProcessNotification) recordEvent(ctx context.Context, n *domain.Notification, event domain.LogEvent) error {
	entry, err := domain.NewNotificationLog(domain.NewNotificationLogInput{
		ID:             uc.idGen.NewLogID(),
		NotificationID: n.ID,
		CorrelationID:  n.CorrelationID,
		Event:          event,
	}, uc.clock.Now())
	if err != nil {
		return fmt.Errorf("build log entry: %w", err)
	}
	if err := uc.logRepo.Append(ctx, entry); err != nil {
		return fmt.Errorf("append log entry: %w", err)
	}
	if err := uc.broadcaster.Publish(ctx, n.ID, n.Status); err != nil {
		return fmt.Errorf("broadcast status: %w", err)
	}
	return nil
}

// backoffFor returns the retry delay for the given attempt count using the
// exponential schedule from CLAUDE.md §5 (30s * 2^(attempts-1)). The asynq
// adapter applies its own jitter on top of this.
func backoffFor(attempts int) time.Duration {
	if attempts <= 0 {
		return backoffBase
	}
	return backoffBase * time.Duration(1<<(attempts-1))
}
