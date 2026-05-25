package application

import (
	"context"
	"fmt"
	"time"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// CreateBatchItem is one row of a batch request. Channel and Priority are
// kept as strings on the boundary; parsing happens inside Execute so that
// HTTP handlers can pass raw client values through unchanged.
type CreateBatchItem struct {
	Channel        string
	Priority       string
	Recipient      string
	Content        string
	IdempotencyKey string
	TemplateID     *string
}

// CreateBatchInput is the payload accepted by CreateBatch. Every item shares
// the batch's CorrelationID per CLAUDE.md §2.3: one business action, one
// correlation id, end-to-end traceable.
type CreateBatchInput struct {
	CorrelationID string
	Notifications []CreateBatchItem
}

// CreateBatch persists a batch of 1-1000 notifications, writes a "created"
// audit-log row per notification, and enqueues each one for processing.
// On any failure (input parse, batch domain rule, repository) Execute
// returns before the next side-effect; partial persistence is impossible
// because every notification flows through the batch repository in one call.
type CreateBatch struct {
	batchRepo ports.BatchRepository
	notifRepo ports.NotificationRepository
	logRepo   ports.NotificationLogRepository
	queue     ports.Queue
	idGen     ports.IDGenerator
	clock     ports.Clock
	metrics   MetricsRecorder
}

// NewCreateBatch wires the dependencies. Every port is required
// except metricsRec — tests pass nil and the emit is skipped.
func NewCreateBatch(
	batchRepo ports.BatchRepository,
	notifRepo ports.NotificationRepository,
	logRepo ports.NotificationLogRepository,
	queue ports.Queue,
	idGen ports.IDGenerator,
	clock ports.Clock,
	metricsRec MetricsRecorder,
) *CreateBatch {
	return &CreateBatch{
		batchRepo: batchRepo,
		notifRepo: notifRepo,
		logRepo:   logRepo,
		queue:     queue,
		idGen:     idGen,
		clock:     clock,
		metrics:   metricsRec,
	}
}

// Execute runs the use case. The flow is:
//
//  1. Resolve correlation id (generate one if the caller did not provide it).
//  2. Build a domain.Notification per item.
//  3. Wrap them in a domain.Batch — this enforces 1 ≤ N ≤ 1000 and the
//     shared-correlation-id invariant, and auto-links each notification to
//     the batch id.
//  4. Persist the batch atomically.
//  5. Append a "created" log row and enqueue each notification.
func (uc *CreateBatch) Execute(ctx context.Context, in CreateBatchInput) (*domain.Batch, error) {
	correlationID := in.CorrelationID
	if correlationID == "" {
		correlationID = uc.idGen.NewCorrelationID()
	}
	now := uc.clock.Now()

	notifications, err := uc.buildNotifications(in.Notifications, correlationID, now)
	if err != nil {
		return nil, err
	}

	batch, err := domain.NewBatch(domain.NewBatchInput{
		ID:            uc.idGen.NewBatchID(),
		CorrelationID: correlationID,
		Notifications: notifications,
	}, now)
	if err != nil {
		return nil, err
	}

	if err := uc.batchRepo.Create(ctx, batch); err != nil {
		return nil, fmt.Errorf("create batch: %w", err)
	}

	for _, n := range batch.Notifications {
		if err := uc.enqueueMember(ctx, n, now); err != nil {
			return nil, err
		}
	}

	return batch, nil
}

// enqueueMember runs the per-notification side effects for a batch
// member: write the "created" log, enqueue the asynq task, transition
// pending → queued in the DB so the worker's atomic claim accepts it
// (CLAUDE.md §3.10), write the "queued" log, and emit metrics. Any
// failure aborts; the reconciler's orphaned-pending sweep recovers
// from a half-finished enqueue (ADR-0011).
func (uc *CreateBatch) enqueueMember(ctx context.Context, n *domain.Notification, now time.Time) error {
	if err := uc.recordCreated(ctx, n, now); err != nil {
		return err
	}
	if err := uc.queue.Enqueue(ctx, n.ID, n.Priority, n.IdempotencyKey); err != nil {
		return fmt.Errorf("enqueue notification %s: %w", n.ID, err)
	}
	if err := n.MarkQueued(now); err != nil {
		return fmt.Errorf("mark queued %s: %w", n.ID, err)
	}
	if err := uc.notifRepo.UpdateStatus(ctx, n, domain.StatusPending); err != nil {
		return fmt.Errorf("persist queued status %s: %w", n.ID, err)
	}
	if err := uc.recordQueued(ctx, n, now); err != nil {
		return err
	}
	if uc.metrics != nil {
		uc.metrics.NotificationCreated(string(n.Channel), string(n.Priority))
	}
	return nil
}

// recordQueued writes the "queued" row into notification_logs after
// the pending → queued transition.
func (uc *CreateBatch) recordQueued(ctx context.Context, n *domain.Notification, now time.Time) error {
	entry, err := domain.NewNotificationLog(domain.NewNotificationLogInput{
		ID:             uc.idGen.NewLogID(),
		NotificationID: n.ID,
		CorrelationID:  n.CorrelationID,
		Event:          domain.LogEventQueued,
	}, now)
	if err != nil {
		return fmt.Errorf("build queued log entry for %s: %w", n.ID, err)
	}
	if err := uc.logRepo.Append(ctx, entry); err != nil {
		return fmt.Errorf("append queued log entry for %s: %w", n.ID, err)
	}
	return nil
}

// buildNotifications constructs the domain.Notification for every input item.
// Index errors are wrapped with the offending position so callers can map
// back to the request body.
func (uc *CreateBatch) buildNotifications(items []CreateBatchItem, correlationID string, now time.Time) ([]*domain.Notification, error) {
	notifs := make([]*domain.Notification, 0, len(items))
	for i, item := range items {
		channel, err := domain.NewChannel(item.Channel)
		if err != nil {
			return nil, fmt.Errorf("notification %d: %w", i, err)
		}
		priority, err := domain.NewPriority(item.Priority)
		if err != nil {
			return nil, fmt.Errorf("notification %d: %w", i, err)
		}
		n, err := domain.NewNotification(domain.NewNotificationInput{
			ID:             uc.idGen.NewNotificationID(),
			CorrelationID:  correlationID,
			Channel:        channel,
			Priority:       priority,
			Recipient:      item.Recipient,
			Content:        item.Content,
			IdempotencyKey: item.IdempotencyKey,
			TemplateID:     item.TemplateID,
		}, now)
		if err != nil {
			return nil, fmt.Errorf("notification %d: %w", i, err)
		}
		notifs = append(notifs, n)
	}
	return notifs, nil
}

// recordCreated writes the initial "created" row into notification_logs.
func (uc *CreateBatch) recordCreated(ctx context.Context, n *domain.Notification, now time.Time) error {
	entry, err := domain.NewNotificationLog(domain.NewNotificationLogInput{
		ID:             uc.idGen.NewLogID(),
		NotificationID: n.ID,
		CorrelationID:  n.CorrelationID,
		Event:          domain.LogEventCreated,
	}, now)
	if err != nil {
		return fmt.Errorf("build log entry for %s: %w", n.ID, err)
	}
	if err := uc.logRepo.Append(ctx, entry); err != nil {
		return fmt.Errorf("append log entry for %s: %w", n.ID, err)
	}
	return nil
}
