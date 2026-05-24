package http

import (
	"context"
	"fmt"

	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// GetNotification overrides the embedded unimplementedServer stub and
// dispatches to the wired GetNotification executor. It is the
// operation behind `GET /api/v1/notifications/{id}`.
//
// ports.ErrNotFound returned by the use case flows through
// RespondWithError and is translated to a 404 problem — no special
// handling here. The handler only ferries the parsed path id into the
// use case and the resulting domain.Notification back out.
func (s *Server) GetNotification(ctx context.Context, req api.GetNotificationRequestObject) (api.GetNotificationResponseObject, error) {
	if s.getNotification == nil {
		return nil, ErrNotImplemented
	}

	n, err := s.getNotification(ctx, application.GetNotificationInput{
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
	return api.GetNotification200JSONResponse{
		Body: out,
		Headers: api.GetNotification200ResponseHeaders{
			XCorrelationID: &correlation,
		},
	}, nil
}
