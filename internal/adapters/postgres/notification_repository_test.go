//go:build integration

package postgres_test

import (
	"context"
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
