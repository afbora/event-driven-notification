package application

import (
	"context"
	"fmt"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// GetNotificationTraceInput is the parameter bundle for GetNotificationTrace.
type GetNotificationTraceInput struct {
	NotificationID domain.NotificationID
}

// GetNotificationTrace returns the ordered audit log for one notification.
// Used by the GET /api/v1/notifications/{id}/trace endpoint (CLAUDE.md
// §12.3) — support staff and API consumers see every status transition
// (created → queued → processing → delivered/failed/retrying/cancelled)
// in chronological order.
//
// The use case verifies the notification exists first so an unknown id
// surfaces as ErrNotFound (404) instead of an empty array (200).
type GetNotificationTrace struct {
	repo    ports.NotificationRepository
	logRepo ports.NotificationLogRepository
}

// NewGetNotificationTrace wires the dependencies.
func NewGetNotificationTrace(repo ports.NotificationRepository, logRepo ports.NotificationLogRepository) *GetNotificationTrace {
	return &GetNotificationTrace{repo: repo, logRepo: logRepo}
}

// Execute runs the use case.
func (uc *GetNotificationTrace) Execute(ctx context.Context, in GetNotificationTraceInput) ([]*domain.NotificationLog, error) {
	if _, err := uc.repo.Get(ctx, in.NotificationID); err != nil {
		return nil, fmt.Errorf("get notification %s: %w", in.NotificationID, err)
	}
	entries, err := uc.logRepo.List(ctx, in.NotificationID)
	if err != nil {
		return nil, fmt.Errorf("list logs for %s: %w", in.NotificationID, err)
	}
	return entries, nil
}
