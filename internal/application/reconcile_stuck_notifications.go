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
	reconcilerBatchSize      = 100
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
// ADR-0011). It runs three independent sweeps over the notifications table
// in a single Execute call:
//
//   - Orphaned pending — a notification that sat in pending past the
//     threshold. This is the dual-write race CLAUDE.md §3.11 documents:
//     the API persisted the row but the enqueue failed before commit.
//     We move it to queued and enqueue it.
//   - Stuck processing — a worker claimed the notification but never
//     finished (crash, OOM, network partition). Mark it failed with
//     reason worker_timeout; the user-facing trace explains the outcome.
//   - Overdue retrying — a retrying notification whose NextRetryAt has
//     elapsed but never got picked up (Redis flush, asynq schedule loss).
//     Re-enqueue without changing status; the worker re-attempts.
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

// Execute runs the three sweeps in order. Each sweep is independent: an
// error in one does not skip the others — but to keep the use case simple
// in Phase 2 we propagate the first error encountered. Phase 6 metrics will
// add partial-progress visibility.
func (uc *ReconcileStuckNotifications) Execute(ctx context.Context, _ ReconcileStuckNotificationsInput) (ReconcileStuckNotificationsOutput, error) {
	now := uc.clock.Now()
	out := ReconcileStuckNotificationsOutput{}

	orphaned, err := uc.repo.FindOrphanedPending(ctx, now.Add(-orphanedPendingThreshold), reconcilerBatchSize)
	if err != nil {
		return out, fmt.Errorf("find orphaned pending: %w", err)
	}
	for _, n := range orphaned {
		if err := uc.handleOrphanedPending(ctx, n, now); err != nil {
			return out, err
		}
		out.OrphanedPendingReenqueued++
	}

	stuck, err := uc.repo.FindStuckProcessing(ctx, now.Add(-stuckProcessingThreshold), reconcilerBatchSize)
	if err != nil {
		return out, fmt.Errorf("find stuck processing: %w", err)
	}
	for _, n := range stuck {
		if err := uc.handleStuckProcessing(ctx, n, now); err != nil {
			return out, err
		}
		out.StuckProcessingFailed++
	}

	overdue, err := uc.repo.FindOverdueRetrying(ctx, now.Add(-overdueRetryingThreshold), reconcilerBatchSize)
	if err != nil {
		return out, fmt.Errorf("find overdue retrying: %w", err)
	}
	for _, n := range overdue {
		if err := uc.handleOverdueRetrying(ctx, n); err != nil {
			return out, err
		}
		out.OverdueRetryingReenqueued++
	}

	return out, nil
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
