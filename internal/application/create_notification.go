// Package application holds the use cases that orchestrate domain entities
// via the ports interfaces. Each use case is a small struct: dependencies
// injected through the constructor, one Execute method that performs the
// orchestration. Use cases never reach for time.Now or for a concrete
// adapter — both are dependencies (CLAUDE.md §3.6, §3.3).
package application

import (
	"context"
	"fmt"
	"time"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// CreateNotificationInput is the payload accepted by CreateNotification.
// String forms of Channel and Priority are kept on the input side so HTTP
// handlers can pass the raw client value; parsing happens inside Execute.
type CreateNotificationInput struct {
	Channel        string
	Priority       string
	Recipient      string
	Content        string
	IdempotencyKey string
	CorrelationID  string // optional — generated server-side when empty
	ScheduledAt    *time.Time
	TemplateID     *string
}

// CreateNotification persists a new notification, writes the initial
// audit-log row, and enqueues it for processing. On success it returns the
// notification in its initial pending state with all server-generated fields
// (ID, CorrelationID, CreatedAt) populated.
type CreateNotification struct {
	repo    ports.NotificationRepository
	logRepo ports.NotificationLogRepository
	queue   ports.Queue
	idGen   ports.IDGenerator
	clock   ports.Clock
}

// NewCreateNotification wires the dependencies. Every port is required;
// passing nil yields a panic on first use, surfaced cheaply at startup
// rather than during a request.
func NewCreateNotification(
	repo ports.NotificationRepository,
	logRepo ports.NotificationLogRepository,
	queue ports.Queue,
	idGen ports.IDGenerator,
	clock ports.Clock,
) *CreateNotification {
	return &CreateNotification{
		repo:    repo,
		logRepo: logRepo,
		queue:   queue,
		idGen:   idGen,
		clock:   clock,
	}
}

// Execute runs the use case. Failures from input parsing or domain
// construction return before any side effect, so the repository, log, and
// queue stay untouched on validation errors (the test enforces this).
//
// If repo.Create succeeds but log.Append or queue.Enqueue fails, Execute
// returns the error. The notification remains persisted in the pending
// state; the reconciler (ADR-0011) sweeps it back into circulation after
// the orphan threshold elapses. This trades latency on the rare failure
// path for simpler synchronous semantics here.
func (uc *CreateNotification) Execute(ctx context.Context, in CreateNotificationInput) (*domain.Notification, error) {
	channel, err := domain.NewChannel(in.Channel)
	if err != nil {
		return nil, err
	}
	priority, err := domain.NewPriority(in.Priority)
	if err != nil {
		return nil, err
	}

	correlationID := in.CorrelationID
	if correlationID == "" {
		correlationID = uc.idGen.NewCorrelationID()
	}

	now := uc.clock.Now()

	n, err := domain.NewNotification(domain.NewNotificationInput{
		ID:             uc.idGen.NewNotificationID(),
		CorrelationID:  correlationID,
		Channel:        channel,
		Priority:       priority,
		Recipient:      in.Recipient,
		Content:        in.Content,
		IdempotencyKey: in.IdempotencyKey,
		ScheduledAt:    in.ScheduledAt,
		TemplateID:     in.TemplateID,
	}, now)
	if err != nil {
		return nil, err
	}

	if err := uc.repo.Create(ctx, n); err != nil {
		return nil, fmt.Errorf("create notification: %w", err)
	}

	logEntry, err := domain.NewNotificationLog(domain.NewNotificationLogInput{
		ID:             uc.idGen.NewLogID(),
		NotificationID: n.ID,
		CorrelationID:  n.CorrelationID,
		Event:          domain.LogEventCreated,
	}, now)
	if err != nil {
		return nil, fmt.Errorf("build log entry: %w", err)
	}
	if err := uc.logRepo.Append(ctx, logEntry); err != nil {
		return nil, fmt.Errorf("append log entry: %w", err)
	}

	if err := uc.enqueue(ctx, n); err != nil {
		return nil, fmt.Errorf("enqueue notification: %w", err)
	}

	// Move pending → queued so the worker's atomic claim
	// (queued|retrying → processing) accepts the notification.
	// If the status update fails the notification remains in pending;
	// the reconciler's orphaned-pending sweep (ADR-0011) puts it back
	// into circulation. This trades a small extra latency on the rare
	// failure path for a strictly safer transition order.
	if err := n.MarkQueued(now); err != nil {
		return nil, fmt.Errorf("mark queued: %w", err)
	}
	if err := uc.repo.UpdateStatus(ctx, n, domain.StatusPending); err != nil {
		return nil, fmt.Errorf("persist queued status: %w", err)
	}

	queuedLog, err := domain.NewNotificationLog(domain.NewNotificationLogInput{
		ID:             uc.idGen.NewLogID(),
		NotificationID: n.ID,
		CorrelationID:  n.CorrelationID,
		Event:          domain.LogEventQueued,
	}, now)
	if err != nil {
		return nil, fmt.Errorf("build queued log entry: %w", err)
	}
	if err := uc.logRepo.Append(ctx, queuedLog); err != nil {
		return nil, fmt.Errorf("append queued log entry: %w", err)
	}

	return n, nil
}

// enqueue selects between immediate and scheduled delivery based on the
// notification's ScheduledAt. Pulled out so Execute reads top-to-bottom.
func (uc *CreateNotification) enqueue(ctx context.Context, n *domain.Notification) error {
	if n.ScheduledAt != nil {
		return uc.queue.EnqueueScheduled(ctx, n.ID, n.Priority, *n.ScheduledAt)
	}
	return uc.queue.Enqueue(ctx, n.ID, n.Priority, n.IdempotencyKey)
}
