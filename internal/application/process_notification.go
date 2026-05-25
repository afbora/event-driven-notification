package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// tracerName is the otel.Tracer key the worker use case uses. Spans
// are no-ops until the global TracerProvider is configured by
// internal/infrastructure/tracing.Setup.
const tracerName = "github.com/afbora/event-driven-notification/internal/application"

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

// ProcessNotificationDeps bundles the eight ports that ProcessNotification
// composes. Bundling keeps NewProcessNotification's signature within
// SonarCloud's parameter-count limit (S107) and makes the wiring code at
// every call site self-documenting via field names rather than positional
// order. Metrics is optional — tests pass a zero MetricsRecorder (nil)
// to skip emission.
type ProcessNotificationDeps struct {
	Repo        ports.NotificationRepository
	LogRepo     ports.NotificationLogRepository
	Provider    ports.Provider
	RateLimiter ports.RateLimiter
	Broadcaster ports.StatusBroadcaster
	IDGen       ports.IDGenerator
	Clock       ports.Clock
	Metrics     MetricsRecorder
}

// NewProcessNotification wires the dependencies. Every port in deps is
// required except Metrics — tests pass nil to skip emit.
func NewProcessNotification(deps ProcessNotificationDeps) *ProcessNotification {
	return &ProcessNotification{
		repo:        deps.Repo,
		logRepo:     deps.LogRepo,
		provider:    deps.Provider,
		rateLimiter: deps.RateLimiter,
		broadcaster: deps.Broadcaster,
		idGen:       deps.IDGen,
		clock:       deps.Clock,
		metrics:     deps.Metrics,
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
//
// Each terminal branch emits a single INFO log line via
// logProcessingOutcome so dashboards can group by outcome and
// duration_ms (CLAUDE.md §3.8 / §12.2). PII (recipient, content) is
// deliberately excluded from the log fields.
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
		return uc.rescheduleForRateLimit(ctx, claimed, start)
	}

	providerCtx, providerSpan := otel.Tracer(tracerName).Start(ctx, "provider.send",
		trace.WithAttributes(
			attribute.String("notification.id", string(claimed.ID)),
			attribute.String("notification.channel", string(claimed.Channel)),
		),
	)
	result := uc.provider.Send(providerCtx, claimed.Channel, claimed.Recipient, claimed.Content)
	providerSpan.SetAttributes(
		attribute.Bool("provider.success", result.Success),
		attribute.Bool("provider.retryable", result.Retryable),
	)
	providerSpan.End()

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
// stamp the processing-duration histogram and the per-task INFO log.
// Each terminal branch is delegated to a dedicated helper so this
// function stays a thin dispatch table.
func (uc *ProcessNotification) applyResult(ctx context.Context, n *domain.Notification, result domain.DeliveryResult, start time.Time) error {
	now := uc.clock.Now()
	duration := now.Sub(start)

	// Every terminal branch records a processing-duration sample so
	// p99 / p95 dashboards capture even retries that eventually
	// failed. Sampled here (not in Execute) because the rate-limit
	// path returns earlier without a provider call.
	defer func() {
		if uc.metrics != nil {
			uc.metrics.ObserveProcessing(string(n.Channel), duration)
		}
	}()

	switch {
	case result.Success:
		return uc.markDelivered(ctx, n, now, duration)
	case !result.Retryable || n.Attempts >= defaultMaxAttempts:
		return uc.markFailed(ctx, n, result, now, duration)
	default:
		return uc.markRetrying(ctx, n, result, now, duration)
	}
}

// markDelivered finalizes a successful send: transition the entity,
// persist, emit metrics, and append the delivered log row.
func (uc *ProcessNotification) markDelivered(ctx context.Context, n *domain.Notification, now time.Time, duration time.Duration) error {
	if err := n.MarkDelivered(now); err != nil {
		return err
	}
	if err := uc.repo.UpdateStatus(ctx, n, domain.StatusProcessing); err != nil {
		return fmt.Errorf("update status (delivered): %w", err)
	}
	if uc.metrics != nil {
		uc.metrics.NotificationDelivered(string(n.Channel))
	}
	if err := uc.recordEvent(ctx, n, domain.LogEventDelivered); err != nil {
		return err
	}
	logProcessingOutcome(ctx, n, "delivered", duration, "")
	return nil
}

// markFailed handles terminal failure — either permanent (non-retryable
// provider response) or retries exhausted. The failure-reason label is
// normalized so the failed-counter never carries an empty string.
func (uc *ProcessNotification) markFailed(ctx context.Context, n *domain.Notification, result domain.DeliveryResult, now time.Time, duration time.Duration) error {
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
	if err := uc.recordEvent(ctx, n, domain.LogEventFailed); err != nil {
		return err
	}
	logProcessingOutcome(ctx, n, "failed", duration, result.Reason)
	return nil
}

// markRetrying schedules a transient failure for re-delivery. asynq
// honors NextRetryAt; the worker re-runs Execute on the next delivery.
func (uc *ProcessNotification) markRetrying(ctx context.Context, n *domain.Notification, result domain.DeliveryResult, now time.Time, duration time.Duration) error {
	nextRetryAt := now.Add(backoffFor(n.Attempts))
	if err := n.MarkRetrying(now, result.Reason, nextRetryAt); err != nil {
		return err
	}
	if err := uc.repo.UpdateStatus(ctx, n, domain.StatusProcessing); err != nil {
		return fmt.Errorf("update status (retrying): %w", err)
	}
	if err := uc.recordEvent(ctx, n, domain.LogEventRetrying); err != nil {
		return err
	}
	logProcessingOutcome(ctx, n, "retrying", duration, result.Reason)
	return nil
}

// rescheduleForRateLimit moves the notification into retrying with a short
// backoff. The asynq adapter respects NextRetryAt; the next delivery will
// re-run Execute and re-check the limiter.
func (uc *ProcessNotification) rescheduleForRateLimit(ctx context.Context, n *domain.Notification, start time.Time) error {
	now := uc.clock.Now()
	if err := n.MarkRetrying(now, "outbound rate limit exceeded", now.Add(rateLimitBackoff)); err != nil {
		return err
	}
	if err := uc.repo.UpdateStatus(ctx, n, domain.StatusProcessing); err != nil {
		return fmt.Errorf("update status (rate limited): %w", err)
	}
	if err := uc.recordEvent(ctx, n, domain.LogEventRetrying); err != nil {
		return err
	}
	logProcessingOutcome(ctx, n, "rate_limited", now.Sub(start), "outbound rate limit exceeded")
	return nil
}

// logProcessingOutcome emits the single canonical INFO line for one
// worker pass over a notification (CLAUDE.md §3.8 / §12.2). Fields
// are intentionally low-cardinality so Grafana panels can group
// without exploding the time-series count; the duration_ms field
// mirrors the histogram in human-readable form for log searches.
//
// PII is deliberately excluded: recipient (phone / email / device
// token) and content (the message body) NEVER appear in log output
// (CLAUDE.md §3.5, Sonar S5145). The provider's reason string is
// included on non-delivered outcomes so an operator can correlate a
// log line with the upstream failure without opening the trace
// endpoint.
//
// correlation_id is attached automatically by the project's slog
// contextHandler (internal/infrastructure/logger) — no need to read
// it explicitly here.
func logProcessingOutcome(ctx context.Context, n *domain.Notification, outcome string, duration time.Duration, errReason string) {
	attrs := []any{
		"notification_id", string(n.ID),
		"channel", string(n.Channel),
		"priority", string(n.Priority),
		"attempts", n.Attempts,
		"outcome", outcome,
		"duration_ms", duration.Milliseconds(),
	}
	if errReason != "" {
		attrs = append(attrs, "error", errReason)
	}
	slog.InfoContext(ctx, "processed notification", attrs...)
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
