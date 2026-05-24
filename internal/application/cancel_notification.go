package application

import (
	"context"
	"fmt"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// CancelNotificationInput is the parameter bundle for CancelNotification.
type CancelNotificationInput struct {
	ID domain.NotificationID
}

// CancelNotification moves a notification into the cancelled terminal
// state. Legal source states are pending, queued, and retrying (per the
// FSM). Canceling a notification already in processing is rejected: once
// the provider call is in flight the message may already be on its way.
type CancelNotification struct {
	repo    ports.NotificationRepository
	logRepo ports.NotificationLogRepository
	queue   ports.Queue
	idGen   ports.IDGenerator
	clock   ports.Clock
}

// NewCancelNotification wires the dependencies. Every port is required.
func NewCancelNotification(
	repo ports.NotificationRepository,
	logRepo ports.NotificationLogRepository,
	queue ports.Queue,
	idGen ports.IDGenerator,
	clock ports.Clock,
) *CancelNotification {
	return &CancelNotification{
		repo:    repo,
		logRepo: logRepo,
		queue:   queue,
		idGen:   idGen,
		clock:   clock,
	}
}

// Execute runs the use case. The flow:
//
//   - Fetch the notification (ports.ErrNotFound propagates).
//   - Capture the current status, then call domain.Cancel — an illegal
//     source state returns ErrInvalidTransition without mutating the entity.
//   - Persist the new status with the expected source as a concurrency guard.
//   - Append a "cancelled" row to notification_logs.
//   - Send a best-effort cancel hint to the queue.
func (uc *CancelNotification) Execute(ctx context.Context, in CancelNotificationInput) (*domain.Notification, error) {
	n, err := uc.repo.Get(ctx, in.ID)
	if err != nil {
		return nil, fmt.Errorf("get notification %s: %w", in.ID, err)
	}

	sourceStatus := n.Status
	now := uc.clock.Now()

	if err := n.Cancel(now); err != nil {
		return nil, err
	}

	if err := uc.repo.UpdateStatus(ctx, n, sourceStatus); err != nil {
		return nil, fmt.Errorf("update status %s: %w", n.ID, err)
	}

	entry, err := domain.NewNotificationLog(domain.NewNotificationLogInput{
		ID:             uc.idGen.NewLogID(),
		NotificationID: n.ID,
		CorrelationID:  n.CorrelationID,
		Event:          domain.LogEventCancelled,
	}, now)
	if err != nil {
		return nil, fmt.Errorf("build log entry: %w", err)
	}
	if err := uc.logRepo.Append(ctx, entry); err != nil {
		return nil, fmt.Errorf("append log entry: %w", err)
	}

	if err := uc.queue.Cancel(ctx, n.ID); err != nil {
		return nil, fmt.Errorf("queue cancel %s: %w", n.ID, err)
	}

	return n, nil
}
