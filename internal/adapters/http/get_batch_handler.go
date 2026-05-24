package http

import (
	"context"
	"fmt"

	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// GetBatch overrides the embedded stub and dispatches to the wired
// executor. It is the operation behind `GET
// /api/v1/notifications/batch/{id}`.
//
// In contrast to POST 202 (which omits the member notifications to
// keep the synchronous reply small), GET inlines them — the client
// has explicitly asked for the full picture.
func (s *Server) GetBatch(ctx context.Context, req api.GetBatchRequestObject) (api.GetBatchResponseObject, error) {
	if s.getBatch == nil {
		return nil, ErrNotImplemented
	}

	b, err := s.getBatch(ctx, application.GetBatchInput{
		ID: domain.BatchID(req.Id.String()),
	})
	if err != nil {
		return nil, err
	}

	out, err := toAPIBatch(b, true) // true = include member notifications on GET
	if err != nil {
		return nil, fmt.Errorf("map domain batch: %w", err)
	}

	correlation := b.CorrelationID
	return api.GetBatch200JSONResponse{
		Body: out,
		Headers: api.GetBatch200ResponseHeaders{
			XCorrelationID: &correlation,
		},
	}, nil
}
