package http

import (
	"context"
	"fmt"

	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// CreateNotification overrides the embedded unimplementedServer stub
// and dispatches to the wired executor. It is the operation behind
// `POST /api/v1/notifications`.
//
// Flow:
//
//  1. Reject an absent body — the strict-server already enforces
//     Content-Type=application/json and JSON-decodes it, but the body
//     pointer is nil when the caller sends an empty request. Treat
//     that as a domain.ValidationError so the error handler emits a
//     400 problem.
//  2. Map the wire-level request and params into the use case input
//     and invoke the executor. Validation errors from the domain
//     (ErrInvalidChannel, etc.) propagate as-is; the error handler
//     dispatches them via WriteError.
//  3. On success, convert the domain.Notification to its wire shape,
//     compute the Location URI, and return a typed 202 response.
//
// If the embedded executor is nil (no override wired) we still need a
// usable signal — return ErrNotImplemented so RespondWithError emits a
// 501 just like the unimplementedServer stub does for the other
// operations.
func (s *Server) CreateNotification(ctx context.Context, req api.CreateNotificationRequestObject) (api.CreateNotificationResponseObject, error) {
	if s.createNotification == nil {
		return nil, ErrNotImplemented
	}
	if req.Body == nil {
		return nil, &domain.ValidationError{
			Field:  "body",
			Reason: "request body is required",
		}
	}

	in := fromAPICreateNotificationRequest(req.Body, req.Params)
	n, err := s.createNotification(ctx, in)
	if err != nil {
		return nil, err
	}

	out, err := toAPINotification(n)
	if err != nil {
		return nil, fmt.Errorf("map domain notification: %w", err)
	}

	location := "/api/v1/notifications/" + string(n.ID)
	correlation := n.CorrelationID

	return api.CreateNotification202JSONResponse{
		Body: out,
		Headers: api.CreateNotification202ResponseHeaders{
			Location:       &location,
			XCorrelationID: &correlation,
		},
	}, nil
}
