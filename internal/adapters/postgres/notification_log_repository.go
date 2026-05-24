package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/afbora/event-driven-notification/internal/adapters/postgres/sqlc"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// NotificationLogRepository is the postgres-backed implementation of
// ports.NotificationLogRepository. Append-only audit trail behind the
// trace endpoint (CLAUDE.md §12.3).
type NotificationLogRepository struct {
	pool *pgxpool.Pool
	q    *sqlc.Queries
}

// NewNotificationLogRepository wires a pgxpool.Pool into a repository.
func NewNotificationLogRepository(pool *pgxpool.Pool) *NotificationLogRepository {
	return &NotificationLogRepository{
		pool: pool,
		q:    sqlc.New(pool),
	}
}

// Append writes one notification_logs row. Details (optional) is encoded
// to JSONB; a nil map persists as SQL NULL.
func (r *NotificationLogRepository) Append(ctx context.Context, entry *domain.NotificationLog) error {
	id, err := parseUUID(string(entry.ID))
	if err != nil {
		return fmt.Errorf("parse log id %q: %w", entry.ID, err)
	}
	notifID, err := parseUUID(string(entry.NotificationID))
	if err != nil {
		return fmt.Errorf("parse notification id %q: %w", entry.NotificationID, err)
	}

	var details []byte
	if entry.Details != nil {
		details, err = json.Marshal(entry.Details)
		if err != nil {
			return fmt.Errorf("marshal log details: %w", err)
		}
	}

	if err := r.q.AppendNotificationLog(ctx, sqlc.AppendNotificationLogParams{
		ID:             id,
		NotificationID: notifID,
		CorrelationID:  entry.CorrelationID,
		Event:          string(entry.Event),
		Details:        details,
		CreatedAt:      timeToTimestamptz(entry.CreatedAt),
	}); err != nil {
		return fmt.Errorf("append log entry %s: %w", entry.ID, err)
	}
	return nil
}

// List returns every log row for a notification in chronological order.
// An empty result is normal — a freshly persisted notification with no
// transitions yet has no log rows besides the "created" event written
// by the use case.
func (r *NotificationLogRepository) List(ctx context.Context, notificationID domain.NotificationID) ([]*domain.NotificationLog, error) {
	pgID, err := parseUUID(string(notificationID))
	if err != nil {
		return nil, fmt.Errorf("parse notification id %q: %w", notificationID, err)
	}
	rows, err := r.q.ListNotificationLogs(ctx, pgID)
	if err != nil {
		return nil, fmt.Errorf("list logs for %s: %w", notificationID, err)
	}

	out := make([]*domain.NotificationLog, 0, len(rows))
	for _, row := range rows {
		entry, err := notificationLogFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

// notificationLogFromRow converts a sqlc NotificationLog into a domain
// NotificationLog. Details JSONB → map[string]any when present; nil when
// the column was NULL.
func notificationLogFromRow(row sqlc.NotificationLog) (*domain.NotificationLog, error) {
	id, err := uuidToString(row.ID)
	if err != nil {
		return nil, fmt.Errorf("log id: %w", err)
	}
	notifID, err := uuidToString(row.NotificationID)
	if err != nil {
		return nil, fmt.Errorf("notification id: %w", err)
	}

	var details map[string]any
	if len(row.Details) > 0 {
		if err := json.Unmarshal(row.Details, &details); err != nil {
			return nil, fmt.Errorf("unmarshal log details for %s: %w", id, err)
		}
	}

	return &domain.NotificationLog{
		ID:             domain.LogID(id),
		NotificationID: domain.NotificationID(notifID),
		CorrelationID:  row.CorrelationID,
		Event:          domain.LogEvent(row.Event),
		Details:        details,
		CreatedAt:      row.CreatedAt.Time,
	}, nil
}
