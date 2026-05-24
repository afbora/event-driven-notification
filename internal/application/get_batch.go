package application

import (
	"context"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// GetBatchInput is the parameter bundle for GetBatch.
type GetBatchInput struct {
	ID domain.BatchID
}

// GetBatch returns a batch and its member notifications by id. Like
// GetNotification it is intentionally thin: a stable application-layer
// seam the HTTP handler depends on, with room to grow when projection
// or authorization logic arrives.
type GetBatch struct {
	repo ports.BatchRepository
}

// NewGetBatch wires the dependency.
func NewGetBatch(repo ports.BatchRepository) *GetBatch {
	return &GetBatch{repo: repo}
}

// Execute delegates to the repository. ports.ErrNotFound propagates
// unchanged so the HTTP translator maps it to a 404.
func (uc *GetBatch) Execute(ctx context.Context, in GetBatchInput) (*domain.Batch, error) {
	return uc.repo.Get(ctx, in.ID)
}
