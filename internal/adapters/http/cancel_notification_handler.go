package http

import (
	"context"
	"fmt"

	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// CancelNotification overrides the embedded stub and dispatches to the
// wired executor. It is the operation behind `PATCH
// /api/v1/notifications/{id}/cancel`.
//
// Notable error semantics handled entirely by RespondWithError:
//
//   - ports.ErrNotFound → 404 (id does not exist).
//   - *domain.TransitionError → 409 (already delivered/failed/cancelled).
//     The detail string carries the offending from→to pair so support
//     can immediately tell what state the notification was already in.
func (s *Server) CancelNotification(ctx context.Context, req api.CancelNotificationRequestObject) (api.CancelNotificationResponseObject, error) {
	if s.cancelNotification == nil {
		return nil, ErrNotImplemented
	}

	n, err := s.cancelNotification(ctx, application.CancelNotificationInput{
		ID: domain.NotificationID(req.Id.String()),
	})
	if err != nil {
		return nil, err
	}

	out, err := toAPINotification(n)
	if err != nil {
		return nil, fmt.Errorf("map domain notification: %w", err)
	}

	correlation := n.CorrelationID
	return api.CancelNotification200JSONResponse{
		Body: out,
		Headers: api.CancelNotification200ResponseHeaders{
			XCorrelationID: &correlation,
		},
	}, nil
}
