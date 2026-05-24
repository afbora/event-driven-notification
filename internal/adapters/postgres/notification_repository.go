// Package postgres holds the postgres adapter — concrete implementations of
// the persistence ports declared in internal/ports/. The package depends on
// pgx/v5 for the connection pool and on the sqlc-generated bindings under
// internal/adapters/postgres/sqlc for type-safe query execution.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/afbora/event-driven-notification/internal/adapters/postgres/sqlc"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// NotificationRepository is the postgres-backed implementation of
// ports.NotificationRepository. Construction is cheap: it captures the pool
// and creates one sqlc Queries handle to dispatch named queries through.
type NotificationRepository struct {
	pool *pgxpool.Pool
	q    *sqlc.Queries
}

// NewNotificationRepository wires a pgxpool.Pool into a repository.
func NewNotificationRepository(pool *pgxpool.Pool) *NotificationRepository {
	return &NotificationRepository{
		pool: pool,
		q:    sqlc.New(pool),
	}
}

// Create persists a new notification.
func (r *NotificationRepository) Create(ctx context.Context, n *domain.Notification) error {
	params, err := notificationToCreateParams(n)
	if err != nil {
		return fmt.Errorf("convert notification: %w", err)
	}
	if err := r.q.CreateNotification(ctx, params); err != nil {
		return fmt.Errorf("create notification %s: %w", n.ID, err)
	}
	return nil
}

// ClaimForProcessing atomically moves the notification from queued/retrying
// into processing, increments the attempts counter, and returns the claimed
// entity (with the new status and updated_at). The flow distinguishes three
// outcomes for the caller:
//
//   - Row missing entirely: returns ports.ErrNotFound (a bug — queue payload
//     references a notification that does not exist).
//   - Row present but unclaimable (status not in queued/retrying): returns
//     ports.ErrAlreadyClaimed (benign race; worker logs and exits cleanly).
//   - Row claimed successfully: returned with status=processing.
//
// The two-query pattern (Get, then UPDATE ... RETURNING) is deliberate so
// the caller can branch on the ErrNotFound case. The window between the two
// queries is harmless: if another worker claims the row in that window, the
// UPDATE returns zero rows and we surface ErrAlreadyClaimed.
func (r *NotificationRepository) ClaimForProcessing(ctx context.Context, id domain.NotificationID, now time.Time) (*domain.Notification, error) {
	pgID, err := parseUUID(string(id))
	if err != nil {
		return nil, fmt.Errorf("parse notification id %q: %w", id, err)
	}

	if _, err := r.q.GetNotification(ctx, pgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: notification %s", ports.ErrNotFound, id)
		}
		return nil, fmt.Errorf("get notification %s: %w", id, err)
	}

	row, err := r.q.ClaimForProcessing(ctx, sqlc.ClaimForProcessingParams{
		ID:        pgID,
		UpdatedAt: timeToTimestamptz(now),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: notification %s", ports.ErrAlreadyClaimed, id)
		}
		return nil, fmt.Errorf("claim notification %s: %w", id, err)
	}
	return notificationFromRow(row)
}

// UpdateStatus persists a status transition the caller has already applied
// to the in-memory entity via a Mark* method. The WHERE clause guards
// against concurrent modification: if the row's current status no longer
// matches expectedSource (another writer beat us, or the id is unknown),
// no rows are affected and the method returns ports.ErrConcurrentUpdate
// without touching the database.
//
// Use cases that need a hard 404 (CancelNotification, ProcessNotification)
// must Get the row first; UpdateStatus folds NotFound into ConcurrentUpdate.
func (r *NotificationRepository) UpdateStatus(ctx context.Context, n *domain.Notification, expectedSource domain.Status) error {
	pgID, err := parseUUID(string(n.ID))
	if err != nil {
		return fmt.Errorf("parse notification id %q: %w", n.ID, err)
	}

	rows, err := r.q.UpdateNotificationStatus(ctx, sqlc.UpdateNotificationStatusParams{
		ID:             pgID,
		NewStatus:      string(n.Status),
		Attempts:       int32(n.Attempts), //nolint:gosec // capped by retry policy
		LastError:      n.LastError,
		NextRetryAt:    timeToTimestamptzPtr(n.NextRetryAt),
		UpdatedAt:      timeToTimestamptz(n.UpdatedAt),
		ExpectedSource: string(expectedSource),
	})
	if err != nil {
		return fmt.Errorf("update notification %s: %w", n.ID, err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: notification %s expected source %s",
			ports.ErrConcurrentUpdate, n.ID, expectedSource)
	}
	return nil
}

// Get returns the notification with the given id, or ports.ErrNotFound when
// the row does not exist. ID strings that do not parse as a UUID are
// reported as errors before the database is consulted.
func (r *NotificationRepository) Get(ctx context.Context, id domain.NotificationID) (*domain.Notification, error) {
	pgID, err := parseUUID(string(id))
	if err != nil {
		return nil, fmt.Errorf("parse notification id %q: %w", id, err)
	}
	row, err := r.q.GetNotification(ctx, pgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: notification %s", ports.ErrNotFound, id)
		}
		return nil, fmt.Errorf("get notification %s: %w", id, err)
	}
	return notificationFromRow(row)
}

// --- domain <-> sqlc row conversions --------------------------------------

func notificationToCreateParams(n *domain.Notification) (sqlc.CreateNotificationParams, error) {
	id, err := parseUUID(string(n.ID))
	if err != nil {
		return sqlc.CreateNotificationParams{}, fmt.Errorf("id: %w", err)
	}

	batchID := pgtype.UUID{}
	if n.BatchID != nil {
		bid, err := parseUUID(string(*n.BatchID))
		if err != nil {
			return sqlc.CreateNotificationParams{}, fmt.Errorf("batch_id: %w", err)
		}
		batchID = bid
	}

	templateID := pgtype.UUID{}
	if n.TemplateID != nil {
		tid, err := parseUUID(*n.TemplateID)
		if err != nil {
			return sqlc.CreateNotificationParams{}, fmt.Errorf("template_id: %w", err)
		}
		templateID = tid
	}

	// The idempotency_key column is partial-unique on non-empty values
	// (db/migrations/000001_initial_schema.up.sql); empty becomes NULL.
	var idempotency *string
	if n.IdempotencyKey != "" {
		key := n.IdempotencyKey
		idempotency = &key
	}

	return sqlc.CreateNotificationParams{
		ID:             id,
		BatchID:        batchID,
		IdempotencyKey: idempotency,
		CorrelationID:  n.CorrelationID,
		Channel:        string(n.Channel),
		Priority:       string(n.Priority),
		Recipient:      n.Recipient,
		Content:        n.Content,
		Status:         string(n.Status),
		Attempts:       int32(n.Attempts), //nolint:gosec // Attempts capped well below int32 max by retry policy
		LastError:      n.LastError,
		NextRetryAt:    timeToTimestamptzPtr(n.NextRetryAt),
		ScheduledAt:    timeToTimestamptzPtr(n.ScheduledAt),
		TemplateID:     templateID,
		CreatedAt:      timeToTimestamptz(n.CreatedAt),
		UpdatedAt:      timeToTimestamptz(n.UpdatedAt),
	}, nil
}

func notificationFromRow(row sqlc.Notification) (*domain.Notification, error) {
	id, err := uuidToString(row.ID)
	if err != nil {
		return nil, fmt.Errorf("id: %w", err)
	}

	var batchID *domain.BatchID
	if row.BatchID.Valid {
		s, err := uuidToString(row.BatchID)
		if err != nil {
			return nil, fmt.Errorf("batch_id: %w", err)
		}
		bid := domain.BatchID(s)
		batchID = &bid
	}

	var templateID *string
	if row.TemplateID.Valid {
		s, err := uuidToString(row.TemplateID)
		if err != nil {
			return nil, fmt.Errorf("template_id: %w", err)
		}
		templateID = &s
	}

	var idempotencyKey string
	if row.IdempotencyKey != nil {
		idempotencyKey = *row.IdempotencyKey
	}

	return &domain.Notification{
		ID:             domain.NotificationID(id),
		BatchID:        batchID,
		IdempotencyKey: idempotencyKey,
		CorrelationID:  row.CorrelationID,
		Channel:        domain.Channel(row.Channel),
		Priority:       domain.Priority(row.Priority),
		Recipient:      row.Recipient,
		Content:        row.Content,
		Status:         domain.Status(row.Status),
		Attempts:       int(row.Attempts),
		LastError:      row.LastError,
		NextRetryAt:    timestamptzToTimePtr(row.NextRetryAt),
		ScheduledAt:    timestamptzToTimePtr(row.ScheduledAt),
		TemplateID:     templateID,
		CreatedAt:      row.CreatedAt.Time,
		UpdatedAt:      row.UpdatedAt.Time,
	}, nil
}

// --- pgtype helpers -------------------------------------------------------

// parseUUID accepts the standard 36-character hyphenated UUID form. Used
// for the id columns of every domain entity persisted here.
func parseUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	return u, nil
}

// uuidToString renders the binary uuid back into the canonical 8-4-4-4-12
// hex form so domain code never has to think about pgtype.
func uuidToString(u pgtype.UUID) (string, error) {
	if !u.Valid {
		return "", errors.New("uuid is null")
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func timeToTimestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func timeToTimestamptzPtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{Valid: false}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func timestamptzToTimePtr(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}
