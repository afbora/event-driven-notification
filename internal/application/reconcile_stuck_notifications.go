package application

import (
	"context"
	"fmt"
	"time"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// Reconciliation thresholds. The reconciler binary calls Execute once per
// minute (CLAUDE.md §3.11); these constants decide how long a notification
// has to be in a non-progressing state before the sweep claims it.
const (
	orphanedPendingThreshold = 5 * time.Minute
	stuckProcessingThreshold = 5 * time.Minute
	overdueRetryingThreshold = 1 * time.Minute

	// stuckQueuedThreshold catches the dual-write race documented in
	// CLAUDE.md §3.11: a notification ends up in queued with no asynq
	// task because the worker dequeued before the API flipped status
	// from pending to queued. The race window is sub-second, so any
	// row sitting in queued past this threshold has definitely lost
	// its task and needs a re-enqueue. 5 minutes mirrors the other
	// status sweeps and keeps false-positive risk negligible.
	stuckQueuedThreshold = 5 * time.Minute

	reconcilerBatchSize = 100
)

// ReconcileStuckNotificationsInput is the parameter bundle for the use case.
// Empty today; configurable thresholds may land here in a later phase.
type ReconcileStuckNotificationsInput struct{}

// ReconcileStuckNotificationsOutput surfaces the counts so the cmd/reconciler
// binary can emit them as Prometheus metrics (phase 6).
type ReconcileStuckNotificationsOutput struct {
	OrphanedPendingReenqueued int
	StuckProcessingFailed     int
	OverdueRetryingReenqueued int
	StuckQueuedReenqueued     int
}

// ReconcileStuckNotifications is the safety-net use case (CLAUDE.md §3.11,
// ADR-0011). It runs four independent sweeps over the notifications table
// in a single Execute call:
//
//   - Orphaned pending — a notification that sat in pending past the
//     threshold. The API persisted the row but the enqueue failed before
//     commit. We move it to queued and enqueue it.
//   - Stuck processing — a worker claimed the notification but never
//     finished (crash, OOM, network partition). Mark it failed with
//     reason worker_timeout; the user-facing trace explains the outcome.
//   - Overdue retrying — a retrying notification whose NextRetryAt has
//     elapsed but never got picked up (Redis flush, asynq schedule loss).
//     Re-enqueue without changing status; the worker re-attempts.
//   - Stuck queued — a notification stranded in queued because the worker
//     dequeued the asynq task before the API flipped status from pending
//     to queued (CreateNotification enqueues, then markQueued). The
//     atomic claim filter is queued|retrying, so the claim was a no-op,
//     asynq counted the task as delivered, and the row landed in queued
//     with no task on the queue. Re-enqueue without changing status. The
//     repo's FindStuckQueued query excludes rows whose scheduled_at is
//     still in the future — those are correctly waiting on a delayed
//     asynq task and re-enqueuing them would duplicate the delivery.
type ReconcileStuckNotifications struct {
	repo    ports.NotificationRepository
	logRepo ports.NotificationLogRepository
	queue   ports.Queue
	idGen   ports.IDGenerator
	clock   ports.Clock
}

// NewReconcileStuckNotifications wires the dependencies.
func NewReconcileStuckNotifications(
	repo ports.NotificationRepository,
	logRepo ports.NotificationLogRepository,
	queue ports.Queue,
	idGen ports.IDGenerator,
	clock ports.Clock,
) *ReconcileStuckNotifications {
	return &ReconcileStuckNotifications{
		repo:    repo,
		logRepo: logRepo,
		queue:   queue,
		idGen:   idGen,
		clock:   clock,
	}
}

// Execute runs the four sweeps in order. Each sweep is independent: an
// error in one does not skip the others — but to keep the use case simple
// in Phase 2 we propagate the first error encountered. Phase 6 metrics will
// add partial-progress visibility.
//
// The body is a thin dispatcher; each sweep lives in its own method so
// Execute stays under SonarCloud's cognitive-complexity threshold (S3776)
// even as the sweep count grows.
func (uc *ReconcileStuckNotifications) Execute(ctx context.Context, _ ReconcileStuckNotificationsInput) (ReconcileStuckNotificationsOutput, error) {
	now := uc.clock.Now()
	out := ReconcileStuckNotificationsOutput{}

	if err := uc.sweepOrphanedPending(ctx, now, &out); err != nil {
		return out, err
	}
	if err := uc.sweepStuckProcessing(ctx, now, &out); err != nil {
		return out, err
	}
	if err := uc.sweepOverdueRetrying(ctx, now, &out); err != nil {
		return out, err
	}
	if err := uc.sweepStuckQueued(ctx, now, &out); err != nil {
		return out, err
	}

	return out, nil
}

// sweepOrphanedPending finds notifications stuck in pending past the
// orphaned threshold and re-enqueues each one.
func (uc *ReconcileStuckNotifications) sweepOrphanedPending(ctx context.Context, now time.Time, out *ReconcileStuckNotificationsOutput) error {
	rows, err := uc.repo.FindOrphanedPending(ctx, now.Add(-orphanedPendingThreshold), reconcilerBatchSize)
	if err != nil {
		return fmt.Errorf("find orphaned pending: %w", err)
	}
	for _, n := range rows {
		if err := uc.handleOrphanedPending(ctx, n, now); err != nil {
			return err
		}
		out.OrphanedPendingReenqueued++
	}
	return nil
}

// sweepStuckProcessing finds notifications a worker claimed but never
// finished and marks each one failed with reason worker_timeout.
func (uc *ReconcileStuckNotifications) sweepStuckProcessing(ctx context.Context, now time.Time, out *ReconcileStuckNotificationsOutput) error {
	rows, err := uc.repo.FindStuckProcessing(ctx, now.Add(-stuckProcessingThreshold), reconcilerBatchSize)
	if err != nil {
		return fmt.Errorf("find stuck processing: %w", err)
	}
	for _, n := range rows {
		if err := uc.handleStuckProcessing(ctx, n, now); err != nil {
			return err
		}
		out.StuckProcessingFailed++
	}
	return nil
}

// sweepOverdueRetrying finds retrying notifications whose NextRetryAt
// has elapsed and re-enqueues each one without changing status.
func (uc *ReconcileStuckNotifications) sweepOverdueRetrying(ctx context.Context, now time.Time, out *ReconcileStuckNotificationsOutput) error {
	rows, err := uc.repo.FindOverdueRetrying(ctx, now.Add(-overdueRetryingThreshold), reconcilerBatchSize)
	if err != nil {
		return fmt.Errorf("find overdue retrying: %w", err)
	}
	for _, n := range rows {
		if err := uc.handleOverdueRetrying(ctx, n); err != nil {
			return err
		}
		out.OverdueRetryingReenqueued++
	}
	return nil
}

// sweepStuckQueued recovers the dual-write race documented in CLAUDE.md
// §3.11 — rows that landed in queued with no asynq task waiting. The
// repo query excludes future-scheduled rows so the recovery does not
// duplicate the eventual delivery.
func (uc *ReconcileStuckNotifications) sweepStuckQueued(ctx context.Context, now time.Time, out *ReconcileStuckNotificationsOutput) error {
	rows, err := uc.repo.FindStuckQueued(ctx, now.Add(-stuckQueuedThreshold), reconcilerBatchSize)
	if err != nil {
		return fmt.Errorf("find stuck queued: %w", err)
	}
	for _, n := range rows {
		if err := uc.handleStuckQueued(ctx, n); err != nil {
			return err
		}
		out.StuckQueuedReenqueued++
	}
	return nil
}

// handleOrphanedPending moves the notification into queued and re-enqueues it.
func (uc *ReconcileStuckNotifications) handleOrphanedPending(ctx context.Context, n *domain.Notification, now time.Time) error {
	if err := n.MarkQueued(now); err != nil {
		return err
	}
	if err := uc.repo.UpdateStatus(ctx, n, domain.StatusPending); err != nil {
		return fmt.Errorf("update status (orphaned %s): %w", n.ID, err)
	}
	if err := uc.recordEvent(ctx, n, domain.LogEventQueued, now); err != nil {
		return err
	}
	if err := uc.queue.Enqueue(ctx, n.ID, n.Priority, n.IdempotencyKey); err != nil {
		return fmt.Errorf("re-enqueue orphaned %s: %w", n.ID, err)
	}
	return nil
}

// handleStuckProcessing marks the notification as failed with worker_timeout.
// No enqueue — terminal state.
func (uc *ReconcileStuckNotifications) handleStuckProcessing(ctx context.Context, n *domain.Notification, now time.Time) error {
	if err := n.MarkFailed(now, "worker_timeout"); err != nil {
		return err
	}
	if err := uc.repo.UpdateStatus(ctx, n, domain.StatusProcessing); err != nil {
		return fmt.Errorf("update status (stuck %s): %w", n.ID, err)
	}
	return uc.recordEvent(ctx, n, domain.LogEventFailed, now)
}

// handleOverdueRetrying re-enqueues without changing status. The notification
// is already in retrying; the worker will pick it up and re-attempt.
func (uc *ReconcileStuckNotifications) handleOverdueRetrying(ctx context.Context, n *domain.Notification) error {
	if err := uc.queue.Enqueue(ctx, n.ID, n.Priority, n.IdempotencyKey); err != nil {
		return fmt.Errorf("re-enqueue overdue %s: %w", n.ID, err)
	}
	return nil
}

// handleStuckQueued re-enqueues a notification that landed in queued with
// no asynq task on the queue (the dual-write race documented in
// CLAUDE.md §3.11). Status is already correct — only the missed
// delivery is restored; no log row is written because there is no
// new event, just a recovered enqueue.
func (uc *ReconcileStuckNotifications) handleStuckQueued(ctx context.Context, n *domain.Notification) error {
	if err := uc.queue.Enqueue(ctx, n.ID, n.Priority, n.IdempotencyKey); err != nil {
		return fmt.Errorf("re-enqueue stuck queued %s: %w", n.ID, err)
	}
	return nil
}

// recordEvent appends one notification_logs row at the given time.
func (uc *ReconcileStuckNotifications) recordEvent(ctx context.Context, n *domain.Notification, event domain.LogEvent, now time.Time) error {
	entry, err := domain.NewNotificationLog(domain.NewNotificationLogInput{
		ID:             uc.idGen.NewLogID(),
		NotificationID: n.ID,
		CorrelationID:  n.CorrelationID,
		Event:          event,
	}, now)
	if err != nil {
		return fmt.Errorf("build log entry: %w", err)
	}
	if err := uc.logRepo.Append(ctx, entry); err != nil {
		return fmt.Errorf("append log entry: %w", err)
	}
	return nil
}
