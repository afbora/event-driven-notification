// Package postgres holds the postgres adapter — concrete implementations of
// the persistence ports declared in internal/ports/. The package depends on
// pgx/v5 for the connection pool and on the sqlc-generated bindings under
// internal/adapters/postgres/sqlc for type-safe query execution.
package postgres

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
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

// parseNotificationIDErr formats the standard wrap used wherever a
// NotificationID string fails to parse as a pgtype UUID. Centralizing
// the format satisfies SonarCloud S1192 (literal duplicated three or
// more times) without scattering a const across the file.
func parseNotificationIDErr(id domain.NotificationID, err error) error {
	return fmt.Errorf("parse notification id %q: %w", id, err)
}

// wrapNotificationErr annotates a sentinel error (typically
// ports.ErrNotFound or ports.ErrAlreadyClaimed) with the offending
// notification id. Callers keep using errors.Is on the sentinel; this
// just adds context to the message.
func wrapNotificationErr(sentinel error, id domain.NotificationID) error {
	return fmt.Errorf("%w: notification %s", sentinel, id)
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
		return nil, parseNotificationIDErr(id, err)
	}

	if _, err := r.q.GetNotification(ctx, pgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, wrapNotificationErr(ports.ErrNotFound, id)
		}
		return nil, fmt.Errorf("get notification %s: %w", id, err)
	}

	row, err := r.q.ClaimForProcessing(ctx, sqlc.ClaimForProcessingParams{
		ID:        pgID,
		UpdatedAt: timeToTimestamptz(now),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, wrapNotificationErr(ports.ErrAlreadyClaimed, id)
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
		return parseNotificationIDErr(n.ID, err)
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
		return nil, parseNotificationIDErr(id, err)
	}
	row, err := r.q.GetNotification(ctx, pgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, wrapNotificationErr(ports.ErrNotFound, id)
		}
		return nil, fmt.Errorf("get notification %s: %w", id, err)
	}
	return notificationFromRow(row)
}

// List returns a page of notifications matching filter, ordered by
// (created_at DESC, id DESC). The returned cursor is opaque to the caller;
// passing it back on the next call yields the next page. An empty next
// cursor means the caller has reached the end.
//
// The implementation fetches limit+1 rows so it can detect "another page
// exists" without an extra COUNT query — a standard keyset-pagination
// trick that scales without surprises.
func (r *NotificationRepository) List(ctx context.Context, filter ports.NotificationFilter, cursor string, limit int) ([]*domain.Notification, string, error) {
	params, err := buildListParams(filter, cursor, limit)
	if err != nil {
		return nil, "", err
	}

	rows, err := r.q.ListNotifications(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("list notifications: %w", err)
	}

	items, err := rowsToNotifications(rows)
	if err != nil {
		return nil, "", err
	}

	items, nextCursor := paginate(items, limit)
	return items, nextCursor, nil
}

// buildListParams translates the public filter + cursor pair into the
// sqlc-generated parameter struct. The +1 row trick (RowLimit = limit+1)
// is centralized here so the pagination helper does not have to know
// about sqlc internals.
func buildListParams(filter ports.NotificationFilter, cursor string, limit int) (sqlc.ListNotificationsParams, error) {
	params := sqlc.ListNotificationsParams{
		RowLimit: int32(limit + 1), //nolint:gosec // limit constrained by callers
	}

	if cursor != "" {
		t, idStr, err := decodeCursor(cursor)
		if err != nil {
			return sqlc.ListNotificationsParams{}, fmt.Errorf("decode cursor: %w", err)
		}
		pgID, err := parseUUID(idStr)
		if err != nil {
			return sqlc.ListNotificationsParams{}, fmt.Errorf("cursor id: %w", err)
		}
		params.CursorCreatedAt = timeToTimestamptz(t)
		params.CursorID = pgID
	}

	if filter.Status != nil {
		s := string(*filter.Status)
		params.Status = &s
	}
	if filter.Channel != nil {
		c := string(*filter.Channel)
		params.Channel = &c
	}
	if filter.BatchID != nil {
		bid, err := parseUUID(string(*filter.BatchID))
		if err != nil {
			return sqlc.ListNotificationsParams{}, fmt.Errorf("batch_id filter: %w", err)
		}
		params.BatchID = bid
	}
	if filter.CreatedAfter != nil {
		params.CreatedAfter = timeToTimestamptz(*filter.CreatedAfter)
	}
	if filter.CreatedBefore != nil {
		params.CreatedBefore = timeToTimestamptz(*filter.CreatedBefore)
	}

	return params, nil
}

// paginate applies the keyset trick: List queries for limit+1 rows; if
// more than limit rows came back, the limit-th row's keyset becomes the
// next cursor and the slice is truncated to limit. An empty next cursor
// means the caller reached the end.
func paginate(items []*domain.Notification, limit int) ([]*domain.Notification, string) {
	if len(items) <= limit {
		return items, ""
	}
	last := items[limit-1]
	return items[:limit], encodeCursor(last.CreatedAt, string(last.ID))
}

// encodeCursor produces an opaque base64 token from the (created_at, id)
// pair that uniquely identifies a row in the keyset order. RFC3339Nano
// preserves microsecond precision (postgres timestamptz resolution).
func encodeCursor(t time.Time, id string) string {
	raw := t.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.URLEncoding.EncodeToString([]byte(raw))
}

// FindOrphanedPending returns pending notifications whose created_at is
// older than olderThan, capped at limit. CLAUDE.md §3.11: rows that sat in
// pending past the threshold are the dual-write race recovery target —
// the reconciler marks them queued and re-enqueues. FOR UPDATE SKIP LOCKED
// lets multiple reconciler instances scan concurrently.
func (r *NotificationRepository) FindOrphanedPending(ctx context.Context, olderThan time.Time, limit int) ([]*domain.Notification, error) {
	rows, err := r.q.FindOrphanedPending(ctx, sqlc.FindOrphanedPendingParams{
		OlderThan: timeToTimestamptz(olderThan),
		RowLimit:  int32(limit), //nolint:gosec // limit is operator-controlled, small
	})
	if err != nil {
		return nil, fmt.Errorf("find orphaned pending: %w", err)
	}
	return rowsToNotifications(rows)
}

// FindStuckProcessing returns processing notifications whose updated_at is
// older than olderThan. These are worker crashes — the reconciler marks
// them failed with reason worker_timeout.
func (r *NotificationRepository) FindStuckProcessing(ctx context.Context, olderThan time.Time, limit int) ([]*domain.Notification, error) {
	rows, err := r.q.FindStuckProcessing(ctx, sqlc.FindStuckProcessingParams{
		OlderThan: timeToTimestamptz(olderThan),
		RowLimit:  int32(limit), //nolint:gosec
	})
	if err != nil {
		return nil, fmt.Errorf("find stuck processing: %w", err)
	}
	return rowsToNotifications(rows)
}

// FindOverdueRetrying returns retrying notifications whose next_retry_at is
// in the past relative to beforeAt. The reconciler re-enqueues them so the
// worker can re-attempt.
func (r *NotificationRepository) FindOverdueRetrying(ctx context.Context, beforeAt time.Time, limit int) ([]*domain.Notification, error) {
	rows, err := r.q.FindOverdueRetrying(ctx, sqlc.FindOverdueRetryingParams{
		BeforeAt: timeToTimestamptz(beforeAt),
		RowLimit: int32(limit), //nolint:gosec
	})
	if err != nil {
		return nil, fmt.Errorf("find overdue retrying: %w", err)
	}
	return rowsToNotifications(rows)
}

// FindStuckQueued returns queued notifications whose updated_at is
// older than olderThan, capped at limit. CLAUDE.md §3.11: this catches
// the dual-write race where the worker dequeues the asynq task before
// CreateNotification has flipped status from pending to queued — the
// atomic claim misses, asynq drops the task, the API writes queued,
// and the row is then stranded in queued forever with no task on the
// queue. The reconciler re-enqueues without changing status (the row
// is already correct; only the missed delivery is restored).
// FOR UPDATE SKIP LOCKED lets multiple reconciler instances scan
// concurrently without conflicting claims.
func (r *NotificationRepository) FindStuckQueued(ctx context.Context, olderThan time.Time, limit int) ([]*domain.Notification, error) {
	rows, err := r.q.FindStuckQueued(ctx, sqlc.FindStuckQueuedParams{
		OlderThan: timeToTimestamptz(olderThan),
		RowLimit:  int32(limit), //nolint:gosec // limit is operator-controlled, small
	})
	if err != nil {
		return nil, fmt.Errorf("find stuck queued: %w", err)
	}
	return rowsToNotifications(rows)
}

// rowsToNotifications converts a slice of sqlc rows into domain entities,
// short-circuiting on the first conversion failure.
func rowsToNotifications(rows []sqlc.Notification) ([]*domain.Notification, error) {
	out := make([]*domain.Notification, 0, len(rows))
	for _, row := range rows {
		n, err := notificationFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// decodeCursor inverts encodeCursor. Any parse failure surfaces as a clean
// error so the HTTP layer can drop the cursor and start a fresh page.
func decodeCursor(s string) (time.Time, string, error) {
	raw, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("base64: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", errors.New("malformed cursor: missing separator")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("timestamp: %w", err)
	}
	return t, parts[1], nil
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
