package application

import (
	"context"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// GetNotificationInput is the parameter bundle for GetNotification.
type GetNotificationInput struct {
	ID domain.NotificationID
}

// GetNotification returns a notification by id. The use case is intentionally
// thin: there is no caller-side authorization to enforce yet, and every
// transformation belongs at the HTTP boundary. Having it as a distinct seam
// matters because it gives the HTTP layer a stable port to depend on and a
// place to grow when authorization or projection logic arrives.
type GetNotification struct {
	repo ports.NotificationRepository
}

// NewGetNotification wires the dependency.
func NewGetNotification(repo ports.NotificationRepository) *GetNotification {
	return &GetNotification{repo: repo}
}

// Execute delegates to the repository. ports.ErrNotFound propagates as-is
// so the HTTP translator can map it to a 404 response.
func (uc *GetNotification) Execute(ctx context.Context, in GetNotificationInput) (*domain.Notification, error) {
	return uc.repo.Get(ctx, in.ID)
}
