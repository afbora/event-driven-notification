package http

import (
	"context"
	"fmt"

	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// CreateBatch overrides the embedded unimplementedServer stub and
// dispatches to the wired CreateBatch executor. It is the operation
// behind `POST /api/v1/notifications/batch`.
//
// The response intentionally omits the member notifications — the
// openapi.yaml description spells out that POST 202 keeps the payload
// small and the caller fetches members later via
// `GET /api/v1/notifications/batch/{id}`. Returning hundreds of full
// Notification structs on every create would dwarf the actual signal
// (batch id and size) the client needs in the synchronous reply.
func (s *Server) CreateBatch(ctx context.Context, req api.CreateBatchRequestObject) (api.CreateBatchResponseObject, error) {
	if s.createBatch == nil {
		return nil, ErrNotImplemented
	}
	if req.Body == nil {
		return nil, &domain.ValidationError{
			Field:  "body",
			Reason: "request body is required",
		}
	}

	in := fromAPICreateBatchRequest(req.Body, req.Params)
	b, err := s.createBatch(ctx, in)
	if err != nil {
		return nil, err
	}

	out, err := toAPIBatch(b, false) // false = omit member notifications on POST 202
	if err != nil {
		return nil, fmt.Errorf("map domain batch: %w", err)
	}

	location := "/api/v1/notifications/batch/" + string(b.ID)
	correlation := b.CorrelationID

	return api.CreateBatch202JSONResponse{
		Body: out,
		Headers: api.CreateBatch202ResponseHeaders{
			Location:       &location,
			XCorrelationID: &correlation,
		},
	}, nil
}
