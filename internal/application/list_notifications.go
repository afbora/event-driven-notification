package application

import (
	"context"
	"fmt"
	"time"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// Pagination defaults and ceiling for ListNotifications. Out-of-range limits
// (zero, negative, or above maxListLimit) snap back to defaultListLimit so
// API callers cannot exhaust the database with a single wide query. In Phase
// 4 these may become configurable via the HTTP layer.
const (
	defaultListLimit = 20
	maxListLimit     = 100
)

// ListNotificationsInput is the payload accepted by ListNotifications. All
// fields are optional; the empty input returns the first page of every
// notification.
type ListNotificationsInput struct {
	Status        string // empty = no filter
	Channel       string // empty = no filter
	BatchID       *domain.BatchID
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	Cursor        string
	Limit         int // 0 or out-of-range → defaultListLimit
}

// ListNotificationsOutput carries one page of notifications plus the cursor
// for the next page. NextCursor is empty when no more pages exist.
type ListNotificationsOutput struct {
	Notifications []*domain.Notification
	NextCursor    string
}

// ListNotifications returns one page of notifications, optionally filtered.
// It is the read-only counterpart to CreateNotification: parse caller input,
// delegate to the repository, return what the repository said.
type ListNotifications struct {
	repo ports.NotificationRepository
}

// NewListNotifications wires the dependency.
func NewListNotifications(repo ports.NotificationRepository) *ListNotifications {
	return &ListNotifications{repo: repo}
}

// Execute parses the string filters into domain types, clamps the limit,
// and delegates to the repository. Invalid filter values surface their
// domain sentinel (ErrInvalidStatus, ErrInvalidChannel) without touching
// the repository.
func (uc *ListNotifications) Execute(ctx context.Context, in ListNotificationsInput) (ListNotificationsOutput, error) {
	filter := ports.NotificationFilter{
		BatchID:       in.BatchID,
		CreatedAfter:  in.CreatedAfter,
		CreatedBefore: in.CreatedBefore,
	}

	if in.Status != "" {
		s, err := domain.NewStatus(in.Status)
		if err != nil {
			return ListNotificationsOutput{}, err
		}
		filter.Status = &s
	}
	if in.Channel != "" {
		c, err := domain.NewChannel(in.Channel)
		if err != nil {
			return ListNotificationsOutput{}, err
		}
		filter.Channel = &c
	}

	limit := in.Limit
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}

	items, nextCursor, err := uc.repo.List(ctx, filter, in.Cursor, limit)
	if err != nil {
		return ListNotificationsOutput{}, fmt.Errorf("list notifications: %w", err)
	}

	return ListNotificationsOutput{
		Notifications: items,
		NextCursor:    nextCursor,
	}, nil
}
