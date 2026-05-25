//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/adapters/postgres"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// fixedIntegrationNow is the canonical timestamp used by integration tests.
// Truncated to microseconds because Postgres timestamptz has microsecond
// resolution; any nanoseconds we set on the Go side are rounded away on the
// round trip and would otherwise break equality checks.
var fixedIntegrationNow = time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)

// integrationNotificationID returns a deterministic UUID-v7-shaped string
// for use as a Notification ID. Real UUID format is required because the
// notifications.id column is typed UUID — arbitrary strings would error at
// insert time.
func integrationNotificationID(suffix string) domain.NotificationID {
	return domain.NotificationID("01940000-0000-7000-8000-0000000000" + suffix)
}

// makeIntegrationNotification builds a minimal known-good Notification for
// repository round-trip tests.
func makeIntegrationNotification(t *testing.T, id domain.NotificationID) *domain.Notification {
	t.Helper()
	n, err := domain.NewNotification(domain.NewNotificationInput{
		ID:             id,
		CorrelationID:  "01940000-0000-7000-8000-0000000000c0",
		Channel:        domain.ChannelSMS,
		Priority:       domain.PriorityNormal,
		Recipient:      "+905551234567",
		Content:        "Integration test message",
		IdempotencyKey: "idem-" + string(id),
	}, fixedIntegrationNow)
	require.NoError(t, err)
	return n
}

// TestNotificationRepository_CreateAndGet covers the happy path — insert
// a notification, read it back, every field round-trips.
func TestNotificationRepository_CreateAndGet(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	original := makeIntegrationNotification(t, integrationNotificationID("01"))
	require.NoError(t, repo.Create(ctx, original))

	got, err := repo.Get(ctx, original.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	// Round-trip integrity across every column.
	require.Equal(t, original.ID, got.ID)
	require.Equal(t, original.CorrelationID, got.CorrelationID)
	require.Equal(t, original.Channel, got.Channel)
	require.Equal(t, original.Priority, got.Priority)
	require.Equal(t, original.Recipient, got.Recipient)
	require.Equal(t, original.Content, got.Content)
	require.Equal(t, original.Status, got.Status)
	require.Equal(t, original.IdempotencyKey, got.IdempotencyKey)
	require.Equal(t, original.Attempts, got.Attempts)
	require.Equal(t, original.LastError, got.LastError)

	// Optional pointers stay nil when the original did not set them.
	require.Nil(t, got.BatchID)
	require.Nil(t, got.NextRetryAt)
	require.Nil(t, got.ScheduledAt)
	require.Nil(t, got.TemplateID)

	// Timestamps come back equal (postgres microsecond resolution).
	require.True(t, got.CreatedAt.Equal(original.CreatedAt),
		"CreatedAt: got %v, want %v", got.CreatedAt, original.CreatedAt)
	require.True(t, got.UpdatedAt.Equal(original.UpdatedAt),
		"UpdatedAt: got %v, want %v", got.UpdatedAt, original.UpdatedAt)
}

// TestNotificationRepository_Get_NotFound: missing id surfaces
// ports.ErrNotFound — the use case layer relies on errors.Is for routing.
func TestNotificationRepository_Get_NotFound(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)

	_, err := repo.Get(context.Background(), integrationNotificationID("ff"))
	require.ErrorIs(t, err, ports.ErrNotFound)
}

// TestNotificationRepository_MalformedID covers the parseNotificationIDErr
// call sites in Get, ClaimForProcessing, and UpdateStatus — each rejects
// inputs that do not parse as a UUID before touching the database. Bundled
// into one test because the assertion shape is identical across methods.
func TestNotificationRepository_MalformedID(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	badID := domain.NotificationID("not-a-uuid")

	t.Run("Get", func(t *testing.T) {
		_, err := repo.Get(context.Background(), badID)
		require.Error(t, err)
		require.Contains(t, err.Error(), "parse notification id")
	})

	t.Run("ClaimForProcessing", func(t *testing.T) {
		_, err := repo.ClaimForProcessing(context.Background(), badID, fixedIntegrationNow)
		require.Error(t, err)
		require.Contains(t, err.Error(), "parse notification id")
	})

	t.Run("UpdateStatus", func(t *testing.T) {
		n := &domain.Notification{ID: badID, Status: domain.StatusQueued}
		err := repo.UpdateStatus(context.Background(), n, domain.StatusPending)
		require.Error(t, err)
		require.Contains(t, err.Error(), "parse notification id")
	})
}

// TestNotificationRepository_Create_WithOptionalFields confirms the
// pointer-typed columns (BatchID, ScheduledAt, TemplateID) round-trip
// correctly when populated, not just when nil.
func TestNotificationRepository_Create_WithOptionalFields(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	scheduledAt := fixedIntegrationNow.Add(24 * time.Hour)
	templateID := "01940000-0000-7000-8000-0000000000a0"

	// Seed the referenced template so the FK can resolve. TemplateRepository
	// lands in PLAN.md task 15; until then a direct insert suffices for this
	// integration test.
	_, err := pool.Exec(ctx,
		`INSERT INTO templates (id, name, channel, body) VALUES ($1, 'tmpl-with-optionals', 'email', 'body {{.Var}}')`,
		templateID,
	)
	require.NoError(t, err, "seed template row for FK resolution")

	n, err := domain.NewNotification(domain.NewNotificationInput{
		ID:             integrationNotificationID("02"),
		CorrelationID:  "01940000-0000-7000-8000-0000000000c0",
		Channel:        domain.ChannelEmail,
		Priority:       domain.PriorityHigh,
		Recipient:      "user@example.com",
		Content:        "Scheduled email",
		IdempotencyKey: "idem-with-optionals",
		ScheduledAt:    &scheduledAt,
		TemplateID:     &templateID,
	}, fixedIntegrationNow)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, n))

	got, err := repo.Get(ctx, n.ID)
	require.NoError(t, err)

	require.NotNil(t, got.ScheduledAt)
	require.True(t, scheduledAt.Equal(*got.ScheduledAt))
	require.NotNil(t, got.TemplateID)
	require.Equal(t, templateID, *got.TemplateID)
}

// TestNotificationRepository_Create_IdempotencyKeyUnique: the partial unique
// index on idempotency_key rejects a duplicate non-empty key.
func TestNotificationRepository_Create_IdempotencyKeyUnique(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	first := makeIntegrationNotification(t, integrationNotificationID("03"))
	first.IdempotencyKey = "shared-key"
	require.NoError(t, repo.Create(ctx, first))

	second := makeIntegrationNotification(t, integrationNotificationID("04"))
	second.IdempotencyKey = "shared-key"
	err := repo.Create(ctx, second)
	require.Error(t, err, "duplicate idempotency_key must be rejected")
}

// --- ClaimForProcessing ---------------------------------------------------

// seedAtStatus persists a notification, then forces it into the requested
// status with a raw UPDATE (bypassing the FSM). Used by ClaimForProcessing
// tests to exercise every source state without walking the use-case chain.
func seedAtStatus(t *testing.T, repo *postgres.NotificationRepository, pool *pgxpool.Pool, id domain.NotificationID, target domain.Status) *domain.Notification {
	t.Helper()
	ctx := context.Background()
	n := makeIntegrationNotification(t, id)
	require.NoError(t, repo.Create(ctx, n))
	if target != domain.StatusPending {
		_, err := pool.Exec(ctx, `UPDATE notifications SET status = $2 WHERE id = $1`, string(id), string(target))
		require.NoError(t, err)
		n.Status = target
	}
	return n
}

// TestClaimForProcessing_FromQueued: queued → processing, attempts++,
// returned entity reflects the new state.
func TestClaimForProcessing_FromQueued(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	seeded := seedAtStatus(t, repo, pool, integrationNotificationID("10"), domain.StatusQueued)

	claimAt := fixedIntegrationNow.Add(time.Second)
	claimed, err := repo.ClaimForProcessing(context.Background(), seeded.ID, claimAt)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	require.Equal(t, domain.StatusProcessing, claimed.Status)
	require.Equal(t, 1, claimed.Attempts, "attempts++ on claim")
	require.True(t, claimed.UpdatedAt.Equal(claimAt))

	// And the row in the DB really moved.
	stored, err := repo.Get(context.Background(), seeded.ID)
	require.NoError(t, err)
	require.Equal(t, domain.StatusProcessing, stored.Status)
}

// TestClaimForProcessing_FromRetrying: retrying is also a legal source —
// MarkRetrying → reconciler/asynq → next claim runs against retrying.
func TestClaimForProcessing_FromRetrying(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	seeded := seedAtStatus(t, repo, pool, integrationNotificationID("11"), domain.StatusRetrying)

	claimed, err := repo.ClaimForProcessing(context.Background(), seeded.ID, fixedIntegrationNow)
	require.NoError(t, err)
	require.Equal(t, domain.StatusProcessing, claimed.Status)
}

// TestClaimForProcessing_AlreadyClaimed_FromProcessing: another worker (or
// a redelivery) already moved the notification into processing. Claim must
// refuse with ErrAlreadyClaimed and leave the row untouched.
func TestClaimForProcessing_AlreadyClaimed_FromProcessing(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	seeded := seedAtStatus(t, repo, pool, integrationNotificationID("12"), domain.StatusProcessing)

	_, err := repo.ClaimForProcessing(context.Background(), seeded.ID, fixedIntegrationNow)
	require.ErrorIs(t, err, ports.ErrAlreadyClaimed)

	// Row untouched.
	stored, err := repo.Get(context.Background(), seeded.ID)
	require.NoError(t, err)
	require.Equal(t, domain.StatusProcessing, stored.Status)
	require.Equal(t, 0, stored.Attempts, "attempts must not change on failed claim")
}

// TestClaimForProcessing_AlreadyClaimed_FromDelivered: terminal state, claim
// must refuse.
func TestClaimForProcessing_AlreadyClaimed_FromDelivered(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	seeded := seedAtStatus(t, repo, pool, integrationNotificationID("13"), domain.StatusDelivered)

	_, err := repo.ClaimForProcessing(context.Background(), seeded.ID, fixedIntegrationNow)
	require.ErrorIs(t, err, ports.ErrAlreadyClaimed)
}

// TestClaimForProcessing_AlreadyClaimed_FromPending: pending is not a valid
// source — only the reconciler can move pending → queued, and only queued
// or retrying may be claimed.
func TestClaimForProcessing_AlreadyClaimed_FromPending(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	seeded := seedAtStatus(t, repo, pool, integrationNotificationID("14"), domain.StatusPending)

	_, err := repo.ClaimForProcessing(context.Background(), seeded.ID, fixedIntegrationNow)
	require.ErrorIs(t, err, ports.ErrAlreadyClaimed)
}

// TestClaimForProcessing_NotFound: unknown id surfaces ErrNotFound, not
// ErrAlreadyClaimed. The distinction matters to the worker — NotFound is
// a bug (queue payload references a missing row), AlreadyClaimed is a
// benign race.
func TestClaimForProcessing_NotFound(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	_, err := repo.ClaimForProcessing(context.Background(), integrationNotificationID("ff"), fixedIntegrationNow)
	require.ErrorIs(t, err, ports.ErrNotFound)
}

// --- UpdateStatus ---------------------------------------------------------

// TestUpdateStatus_HappyPath: queued → cancelled, every mutable field
// (status, attempts, last_error, next_retry_at, updated_at) round-trips.
func TestUpdateStatus_HappyPath(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	seeded := seedAtStatus(t, repo, pool, integrationNotificationID("20"), domain.StatusQueued)

	// Mutate the in-memory entity the way the use case would, then persist.
	now := fixedIntegrationNow.Add(5 * time.Minute)
	require.NoError(t, seeded.Cancel(now))

	err := repo.UpdateStatus(ctx, seeded, domain.StatusQueued)
	require.NoError(t, err)

	stored, err := repo.Get(ctx, seeded.ID)
	require.NoError(t, err)
	require.Equal(t, domain.StatusCancelled, stored.Status)
	require.True(t, stored.UpdatedAt.Equal(now),
		"UpdatedAt: got %v, want %v", stored.UpdatedAt, now)
}

// TestUpdateStatus_RecordsRetryFields: MarkRetrying populates LastError and
// NextRetryAt; both must persist.
func TestUpdateStatus_RecordsRetryFields(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	seeded := seedAtStatus(t, repo, pool, integrationNotificationID("21"), domain.StatusProcessing)

	now := fixedIntegrationNow.Add(time.Minute)
	nextRetryAt := now.Add(30 * time.Second)
	require.NoError(t, seeded.MarkRetrying(now, "provider 503", nextRetryAt))

	require.NoError(t, repo.UpdateStatus(ctx, seeded, domain.StatusProcessing))

	stored, err := repo.Get(ctx, seeded.ID)
	require.NoError(t, err)
	require.Equal(t, domain.StatusRetrying, stored.Status)
	require.Equal(t, "provider 503", stored.LastError)
	require.NotNil(t, stored.NextRetryAt)
	require.True(t, stored.NextRetryAt.Equal(nextRetryAt))
}

// TestUpdateStatus_RecordsAttempts: the attempts counter mutated in-memory
// (e.g. by MarkProcessing in earlier flows) is persisted.
func TestUpdateStatus_RecordsAttempts(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	seeded := seedAtStatus(t, repo, pool, integrationNotificationID("22"), domain.StatusProcessing)
	seeded.Attempts = 3
	require.NoError(t, seeded.MarkDelivered(fixedIntegrationNow.Add(time.Minute)))

	require.NoError(t, repo.UpdateStatus(ctx, seeded, domain.StatusProcessing))

	stored, err := repo.Get(ctx, seeded.ID)
	require.NoError(t, err)
	require.Equal(t, 3, stored.Attempts)
}

// TestUpdateStatus_ConcurrentUpdate: the row's current status does not match
// expectedSource (another writer beat us to it). UpdateStatus surfaces
// ports.ErrConcurrentUpdate and leaves the row untouched.
func TestUpdateStatus_ConcurrentUpdate(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	// Row is actually in processing, but the caller thinks it's still queued.
	seeded := seedAtStatus(t, repo, pool, integrationNotificationID("23"), domain.StatusProcessing)
	require.NoError(t, seeded.MarkDelivered(fixedIntegrationNow.Add(time.Minute)))

	err := repo.UpdateStatus(ctx, seeded, domain.StatusQueued) // wrong expected source
	require.ErrorIs(t, err, ports.ErrConcurrentUpdate)

	// Row is still in processing — no writes happened.
	stored, err := repo.Get(ctx, seeded.ID)
	require.NoError(t, err)
	require.Equal(t, domain.StatusProcessing, stored.Status)
}

// TestUpdateStatus_NotFound: unknown id behaves like a concurrent update —
// the WHERE clause matches zero rows. Use cases that need a hard 404 should
// Get first (CancelNotification / ProcessNotification already do).
func TestUpdateStatus_NotFound(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	ghost := makeIntegrationNotification(t, integrationNotificationID("ff"))
	require.NoError(t, ghost.MarkQueued(fixedIntegrationNow.Add(time.Minute)))

	err := repo.UpdateStatus(ctx, ghost, domain.StatusPending)
	require.ErrorIs(t, err, ports.ErrConcurrentUpdate)
}

// --- List with cursor pagination -----------------------------------------

// seedTimestamped persists a notification with a caller-controlled CreatedAt
// so list-order tests can rely on a deterministic sequence.
func seedTimestamped(t *testing.T, repo *postgres.NotificationRepository, id domain.NotificationID, createdAt time.Time) *domain.Notification {
	t.Helper()
	n, err := domain.NewNotification(domain.NewNotificationInput{
		ID:            id,
		CorrelationID: "01940000-0000-7000-8000-0000000000c0",
		Channel:       domain.ChannelSMS,
		Priority:      domain.PriorityNormal,
		Recipient:     "+905551234567",
		Content:       "list pagination test",
	}, createdAt)
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), n))
	return n
}

// TestList_EmptyResult: no rows in the table → empty slice, empty cursor.
func TestList_EmptyResult(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	items, cursor, err := repo.List(context.Background(), ports.NotificationFilter{}, "", 10)
	require.NoError(t, err)
	require.Empty(t, items)
	require.Empty(t, cursor)
}

// TestList_SinglePage_FewerThanLimit: every row fits in one page → returned
// in created_at DESC order, no next cursor.
func TestList_SinglePage_FewerThanLimit(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	// Three rows, each one minute apart so ordering is unambiguous.
	for i := 0; i < 3; i++ {
		seedTimestamped(t, repo,
			integrationNotificationID(fmt.Sprintf("3%d", i)),
			fixedIntegrationNow.Add(time.Duration(i)*time.Minute),
		)
	}

	items, cursor, err := repo.List(ctx, ports.NotificationFilter{}, "", 10)
	require.NoError(t, err)
	require.Len(t, items, 3)
	require.Empty(t, cursor, "fewer than limit → no next page")

	// Latest first.
	require.Equal(t, integrationNotificationID("32"), items[0].ID)
	require.Equal(t, integrationNotificationID("31"), items[1].ID)
	require.Equal(t, integrationNotificationID("30"), items[2].ID)
}

// TestList_MultiPage: walk a 5-row table at limit=2. Three pages: 2, 2, 1.
// The final page has an empty cursor.
func TestList_MultiPage(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		seedTimestamped(t, repo,
			integrationNotificationID(fmt.Sprintf("4%d", i)),
			fixedIntegrationNow.Add(time.Duration(i)*time.Minute),
		)
	}

	// Page 1.
	items1, cursor1, err := repo.List(ctx, ports.NotificationFilter{}, "", 2)
	require.NoError(t, err)
	require.Len(t, items1, 2)
	require.NotEmpty(t, cursor1)
	require.Equal(t, integrationNotificationID("44"), items1[0].ID)
	require.Equal(t, integrationNotificationID("43"), items1[1].ID)

	// Page 2.
	items2, cursor2, err := repo.List(ctx, ports.NotificationFilter{}, cursor1, 2)
	require.NoError(t, err)
	require.Len(t, items2, 2)
	require.NotEmpty(t, cursor2)
	require.Equal(t, integrationNotificationID("42"), items2[0].ID)
	require.Equal(t, integrationNotificationID("41"), items2[1].ID)

	// Page 3 — final, partial, empty cursor.
	items3, cursor3, err := repo.List(ctx, ports.NotificationFilter{}, cursor2, 2)
	require.NoError(t, err)
	require.Len(t, items3, 1)
	require.Empty(t, cursor3, "final page → empty cursor")
	require.Equal(t, integrationNotificationID("40"), items3[0].ID)
}

// TestList_FilterByStatus: status filter narrows the result set.
func TestList_FilterByStatus(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	seedAtStatus(t, repo, pool, integrationNotificationID("50"), domain.StatusDelivered)
	seedAtStatus(t, repo, pool, integrationNotificationID("51"), domain.StatusFailed)
	seedAtStatus(t, repo, pool, integrationNotificationID("52"), domain.StatusDelivered)

	delivered := domain.StatusDelivered
	items, _, err := repo.List(ctx, ports.NotificationFilter{Status: &delivered}, "", 10)
	require.NoError(t, err)
	require.Len(t, items, 2)
	for _, n := range items {
		require.Equal(t, domain.StatusDelivered, n.Status)
	}
}

// TestList_InvalidCursor: malformed cursor → error before the database is
// consulted; the caller knows to drop the cursor and start over.
func TestList_InvalidCursor(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	_, _, err := repo.List(context.Background(), ports.NotificationFilter{}, "not-a-real-cursor", 10)
	require.Error(t, err)
}

// --- Reconciler queries (FOR UPDATE SKIP LOCKED) -------------------------

// forceTimestamps rewrites created_at and/or updated_at for an existing row
// so the reconciler-threshold tests can simulate "this row has been sitting
// here a while". Either timestamp may be the zero value to leave untouched.
func forceTimestamps(t *testing.T, pool *pgxpool.Pool, id domain.NotificationID, createdAt, updatedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	if !createdAt.IsZero() {
		_, err := pool.Exec(ctx, `UPDATE notifications SET created_at = $2 WHERE id = $1`, string(id), createdAt)
		require.NoError(t, err)
	}
	if !updatedAt.IsZero() {
		_, err := pool.Exec(ctx, `UPDATE notifications SET updated_at = $2 WHERE id = $1`, string(id), updatedAt)
		require.NoError(t, err)
	}
}

// forceNextRetryAt sets next_retry_at directly (the reconciler retrying
// query keys on this column).
func forceNextRetryAt(t *testing.T, pool *pgxpool.Pool, id domain.NotificationID, nextRetryAt time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `UPDATE notifications SET next_retry_at = $2 WHERE id = $1`, string(id), nextRetryAt)
	require.NoError(t, err)
}

// forceScheduledAt sets scheduled_at directly so the stuck-queued sweep
// test can prove the query excludes rows whose delayed asynq task has
// not fired yet — re-enqueuing those would duplicate the eventual
// delivery (CLAUDE.md §3.11).
func forceScheduledAt(t *testing.T, pool *pgxpool.Pool, id domain.NotificationID, scheduledAt time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `UPDATE notifications SET scheduled_at = $2 WHERE id = $1`, string(id), scheduledAt)
	require.NoError(t, err)
}

// TestFindOrphanedPending_HappyPath: pending rows older than the threshold
// are returned; recent pending and non-pending rows are not.
func TestFindOrphanedPending_HappyPath(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	threshold := fixedIntegrationNow.Add(-5 * time.Minute)

	// Old pending — returned.
	seedTimestamped(t, repo, integrationNotificationID("60"), fixedIntegrationNow.Add(-10*time.Minute))

	// Recent pending — too fresh, not returned.
	seedTimestamped(t, repo, integrationNotificationID("61"), fixedIntegrationNow.Add(-1*time.Minute))

	// Old queued — wrong status, not returned.
	seedAtStatus(t, repo, pool, integrationNotificationID("62"), domain.StatusQueued)
	forceTimestamps(t, pool, integrationNotificationID("62"), fixedIntegrationNow.Add(-10*time.Minute), time.Time{})

	items, err := repo.FindOrphanedPending(ctx, threshold, 10)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, integrationNotificationID("60"), items[0].ID)
}

// TestFindStuckProcessing_HappyPath: processing rows whose updated_at fell
// behind the threshold are returned.
func TestFindStuckProcessing_HappyPath(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	threshold := fixedIntegrationNow.Add(-5 * time.Minute)

	// Stuck — claimed long ago, updated_at predates threshold.
	seedAtStatus(t, repo, pool, integrationNotificationID("70"), domain.StatusProcessing)
	forceTimestamps(t, pool, integrationNotificationID("70"), time.Time{}, fixedIntegrationNow.Add(-10*time.Minute))

	// Fresh processing — not stuck yet.
	seedAtStatus(t, repo, pool, integrationNotificationID("71"), domain.StatusProcessing)
	forceTimestamps(t, pool, integrationNotificationID("71"), time.Time{}, fixedIntegrationNow.Add(-1*time.Minute))

	// Old delivered — terminal, never stuck.
	seedAtStatus(t, repo, pool, integrationNotificationID("72"), domain.StatusDelivered)
	forceTimestamps(t, pool, integrationNotificationID("72"), time.Time{}, fixedIntegrationNow.Add(-10*time.Minute))

	items, err := repo.FindStuckProcessing(ctx, threshold, 10)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, integrationNotificationID("70"), items[0].ID)
}

// TestFindOverdueRetrying_HappyPath: retrying rows whose next_retry_at has
// elapsed are returned.
func TestFindOverdueRetrying_HappyPath(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	before := fixedIntegrationNow.Add(-1 * time.Minute)

	// Overdue — next_retry_at in the past.
	seedAtStatus(t, repo, pool, integrationNotificationID("80"), domain.StatusRetrying)
	forceNextRetryAt(t, pool, integrationNotificationID("80"), fixedIntegrationNow.Add(-5*time.Minute))

	// Still pending its scheduled retry.
	seedAtStatus(t, repo, pool, integrationNotificationID("81"), domain.StatusRetrying)
	forceNextRetryAt(t, pool, integrationNotificationID("81"), fixedIntegrationNow.Add(10*time.Minute))

	// Retrying but next_retry_at NULL — not returned (would surface as a bug,
	// reconciler skips it).
	seedAtStatus(t, repo, pool, integrationNotificationID("82"), domain.StatusRetrying)

	items, err := repo.FindOverdueRetrying(ctx, before, 10)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, integrationNotificationID("80"), items[0].ID)
}

// TestFindStuckQueued_HappyPath: queued rows whose updated_at is older
// than the threshold are returned (the dual-write race recovery target
// from CLAUDE.md §3.11); recent queued rows and non-queued rows are
// not. Pinned shape against future churn around the sweep.
func TestFindStuckQueued_HappyPath(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	threshold := fixedIntegrationNow.Add(-5 * time.Minute)

	// Stuck queued — updated_at predates the threshold (the asynq task
	// was lost minutes ago, the row never moved on).
	seedAtStatus(t, repo, pool, integrationNotificationID("a0"), domain.StatusQueued)
	forceTimestamps(t, pool, integrationNotificationID("a0"), time.Time{}, fixedIntegrationNow.Add(-10*time.Minute))

	// Recent queued — fresh, race window may still be live; do NOT
	// return so the reconciler does not re-enqueue a row whose worker
	// is about to pick it up.
	seedAtStatus(t, repo, pool, integrationNotificationID("a1"), domain.StatusQueued)
	forceTimestamps(t, pool, integrationNotificationID("a1"), time.Time{}, fixedIntegrationNow.Add(-1*time.Minute))

	// Old pending — different sweep (FindOrphanedPending), not this one.
	seedTimestamped(t, repo, integrationNotificationID("a2"), fixedIntegrationNow.Add(-10*time.Minute))

	// Old delivered — terminal, never stuck.
	seedAtStatus(t, repo, pool, integrationNotificationID("a3"), domain.StatusDelivered)
	forceTimestamps(t, pool, integrationNotificationID("a3"), time.Time{}, fixedIntegrationNow.Add(-10*time.Minute))

	items, err := repo.FindStuckQueued(ctx, threshold, 10)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, integrationNotificationID("a0"), items[0].ID)
}

// TestFindStuckQueued_ExcludesFutureScheduled: a notification scheduled
// for the future sits in 'queued' from creation until its scheduled_at
// fires (asynq holds the task as a delayed entry). Re-enqueuing such a
// row would duplicate the eventual delivery the moment the delayed
// task lands — exactly the regression the scheduled_at predicate in
// FindStuckQueued exists to prevent. This test pins that behavior.
func TestFindStuckQueued_ExcludesFutureScheduled(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	threshold := fixedIntegrationNow.Add(-5 * time.Minute)

	// Future scheduled — created 10 minutes ago (updated_at predates
	// threshold), scheduled to fire 30 minutes from now. Asynq's
	// delayed task is alive; this row is NOT stuck.
	seedAtStatus(t, repo, pool, integrationNotificationID("b0"), domain.StatusQueued)
	forceTimestamps(t, pool, integrationNotificationID("b0"), time.Time{}, fixedIntegrationNow.Add(-10*time.Minute))
	forceScheduledAt(t, pool, integrationNotificationID("b0"), fixedIntegrationNow.Add(30*time.Minute))

	// Overdue scheduled — scheduled_at is more than 5 minutes in the
	// past, the delayed task should have fired already; this IS stuck
	// (asynq scheduler crash, Redis flush, etc.) and must be returned.
	seedAtStatus(t, repo, pool, integrationNotificationID("b1"), domain.StatusQueued)
	forceTimestamps(t, pool, integrationNotificationID("b1"), time.Time{}, fixedIntegrationNow.Add(-15*time.Minute))
	forceScheduledAt(t, pool, integrationNotificationID("b1"), fixedIntegrationNow.Add(-10*time.Minute))

	items, err := repo.FindStuckQueued(ctx, threshold, 10)
	require.NoError(t, err)
	require.Len(t, items, 1,
		"only the overdue-scheduled row must surface; the future-scheduled row must NOT (re-enqueuing it would duplicate the delivery)")
	require.Equal(t, integrationNotificationID("b1"), items[0].ID)
}

// TestReconcilerQueries_RespectLimit: limit caps the batch size.
func TestReconcilerQueries_RespectLimit(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	// Seed 5 stuck-processing rows.
	for i := 0; i < 5; i++ {
		id := integrationNotificationID(fmt.Sprintf("9%d", i))
		seedAtStatus(t, repo, pool, id, domain.StatusProcessing)
		forceTimestamps(t, pool, id, time.Time{}, fixedIntegrationNow.Add(-10*time.Minute))
	}

	items, err := repo.FindStuckProcessing(ctx, fixedIntegrationNow.Add(-5*time.Minute), 2)
	require.NoError(t, err)
	require.Len(t, items, 2, "limit must cap the result set")
}
