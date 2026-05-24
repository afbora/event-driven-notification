package application

import (
	"context"
	"errors"
	"time"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// ErrScheduledInPast is returned when ScheduleNotification receives a
// scheduled_at value that is not strictly later than the current clock.
// Callers wanting immediate delivery use CreateNotification.
var ErrScheduledInPast = errors.New("scheduled_at must be strictly in the future")

// ScheduleNotificationInput is the payload accepted by ScheduleNotification.
// ScheduledAt is mandatory (a plain time.Time, not a pointer) — the whole
// point of this use case is future delivery.
type ScheduleNotificationInput struct {
	Channel        string
	Priority       string
	Recipient      string
	Content        string
	IdempotencyKey string
	CorrelationID  string
	ScheduledAt    time.Time
	TemplateID     *string
}

// ScheduleNotification persists a future-delivery notification. It is a
// thin composition over CreateNotification: validate that ScheduledAt is
// strictly in the future, then delegate. Keeping a distinct use case lets
// the HTTP layer expose a dedicated endpoint with explicit semantics, and
// it places the past-time guard in one obvious location.
type ScheduleNotification struct {
	createUC *CreateNotification
	clock    ports.Clock
}

// NewScheduleNotification wires the dependencies.
func NewScheduleNotification(createUC *CreateNotification, clock ports.Clock) *ScheduleNotification {
	return &ScheduleNotification{createUC: createUC, clock: clock}
}

// Execute validates ScheduledAt, then delegates to the inner CreateNotification.
func (uc *ScheduleNotification) Execute(ctx context.Context, in ScheduleNotificationInput) (*domain.Notification, error) {
	now := uc.clock.Now()
	if !in.ScheduledAt.After(now) {
		return nil, ErrScheduledInPast
	}

	scheduledAt := in.ScheduledAt
	return uc.createUC.Execute(ctx, CreateNotificationInput{
		Channel:        in.Channel,
		Priority:       in.Priority,
		Recipient:      in.Recipient,
		Content:        in.Content,
		IdempotencyKey: in.IdempotencyKey,
		CorrelationID:  in.CorrelationID,
		ScheduledAt:    &scheduledAt,
		TemplateID:     in.TemplateID,
	})
}
