package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/afbora/event-driven-notification/internal/adapters/postgres/sqlc"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// BatchRepository is the postgres-backed implementation of
// ports.BatchRepository. Create persists the batch and every notification
// inside it atomically in one transaction; Get eagerly loads the batch and
// its notifications via two queries.
type BatchRepository struct {
	pool *pgxpool.Pool
}

// NewBatchRepository wires a pgxpool.Pool into a repository.
func NewBatchRepository(pool *pgxpool.Pool) *BatchRepository {
	return &BatchRepository{pool: pool}
}

// Create persists a batch and every notification it owns in a single
// transaction. Partial inserts are impossible — any failure rolls the
// whole thing back.
func (r *BatchRepository) Create(ctx context.Context, b *domain.Batch) error {
	batchID, err := parseUUID(string(b.ID))
	if err != nil {
		return fmt.Errorf("parse batch id %q: %w", b.ID, err)
	}

	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		q := sqlc.New(tx)

		if err := q.CreateBatch(ctx, sqlc.CreateBatchParams{
			ID:            batchID,
			CorrelationID: b.CorrelationID,
			CreatedAt:     timeToTimestamptz(b.CreatedAt),
		}); err != nil {
			return fmt.Errorf("create batch %s: %w", b.ID, err)
		}

		for _, n := range b.Notifications {
			params, err := notificationToCreateParams(n)
			if err != nil {
				return fmt.Errorf("convert notification %s: %w", n.ID, err)
			}
			if err := q.CreateNotification(ctx, params); err != nil {
				return fmt.Errorf("create notification %s in batch %s: %w", n.ID, b.ID, err)
			}
		}
		return nil
	})
}

// Get returns a batch with all of its notifications eagerly loaded. Returns
// ports.ErrNotFound when the batch id is unknown.
func (r *BatchRepository) Get(ctx context.Context, id domain.BatchID) (*domain.Batch, error) {
	pgID, err := parseUUID(string(id))
	if err != nil {
		return nil, fmt.Errorf("parse batch id %q: %w", id, err)
	}

	q := sqlc.New(r.pool)

	batchRow, err := q.GetBatch(ctx, pgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: batch %s", ports.ErrNotFound, id)
		}
		return nil, fmt.Errorf("get batch %s: %w", id, err)
	}

	notifRows, err := q.ListNotificationsByBatch(ctx, pgID)
	if err != nil {
		return nil, fmt.Errorf("list notifications for batch %s: %w", id, err)
	}
	notifications, err := rowsToNotifications(notifRows)
	if err != nil {
		return nil, fmt.Errorf("convert batch notifications: %w", err)
	}

	idStr, err := uuidToString(batchRow.ID)
	if err != nil {
		return nil, fmt.Errorf("batch id: %w", err)
	}

	return &domain.Batch{
		ID:            domain.BatchID(idStr),
		CorrelationID: batchRow.CorrelationID,
		Notifications: notifications,
		CreatedAt:     batchRow.CreatedAt.Time,
	}, nil
}
