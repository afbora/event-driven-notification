//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/adapters/postgres"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// integrationBatchID returns a deterministic UUID-v7-shaped batch id.
func integrationBatchID(suffix string) domain.BatchID {
	return domain.BatchID("01940000-0000-7000-8000-0000000000" + suffix)
}

// makeIntegrationBatch builds a batch of three notifications with a shared
// correlation id, ready to round-trip through the repository.
func makeIntegrationBatch(t *testing.T, id domain.BatchID) *domain.Batch {
	t.Helper()

	corr := "01940000-0000-7000-8000-0000000000d0"
	notifs := make([]*domain.Notification, 3)
	for i := 0; i < 3; i++ {
		n, err := domain.NewNotification(domain.NewNotificationInput{
			ID:            integrationNotificationID(charPair("a", i)),
			CorrelationID: corr,
			Channel:       domain.ChannelSMS,
			Priority:      domain.PriorityNormal,
			Recipient:     "+90555000000" + intToStr(i+1),
			Content:       "batch item " + intToStr(i),
		}, fixedIntegrationNow)
		require.NoError(t, err)
		notifs[i] = n
	}

	batch, err := domain.NewBatch(domain.NewBatchInput{
		ID:            id,
		CorrelationID: corr,
		Notifications: notifs,
	}, fixedIntegrationNow)
	require.NoError(t, err)
	return batch
}

// charPair + intToStr keep the helper above free of fmt for the simple
// string concat we need — avoids another import.
func charPair(prefix string, i int) string {
	return prefix + intToStr(i)
}

func intToStr(i int) string {
	return string(rune('0' + i))
}

// TestBatchRepository_CreateAndGet: round-trip a batch with 3 notifications.
func TestBatchRepository_CreateAndGet(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewBatchRepository(pool)
	ctx := context.Background()

	original := makeIntegrationBatch(t, integrationBatchID("01"))
	require.NoError(t, repo.Create(ctx, original))

	got, err := repo.Get(ctx, original.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, original.ID, got.ID)
	require.Equal(t, original.CorrelationID, got.CorrelationID)
	require.Len(t, got.Notifications, 3)

	// Every notification has its BatchID set to the batch id.
	for i, n := range got.Notifications {
		require.NotNilf(t, n.BatchID, "notification %d: BatchID is nil", i)
		require.Equalf(t, original.ID, *n.BatchID, "notification %d: BatchID mismatch", i)
		require.Equalf(t, original.CorrelationID, n.CorrelationID, "notification %d: correlation id mismatch", i)
	}
}

// TestBatchRepository_MalformedID covers the parseUUID guard at the
// top of Create and Get — each rejects ids that do not parse as a UUID
// before touching the database. Bundled into one test because the
// assertion shape is identical across both methods.
func TestBatchRepository_MalformedID(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewBatchRepository(pool)
	ctx := context.Background()
	badID := domain.BatchID("not-a-uuid")

	t.Run("Get", func(t *testing.T) {
		_, err := repo.Get(ctx, badID)
		require.Error(t, err)
		require.Contains(t, err.Error(), "parse batch id")
	})

	t.Run("Create", func(t *testing.T) {
		// Minimal Batch struct — the parseUUID guard fires before any
		// field validation or DB write.
		b := &domain.Batch{ID: badID, CorrelationID: "01CORR", CreatedAt: fixedIntegrationNow}
		err := repo.Create(ctx, b)
		require.Error(t, err)
		require.Contains(t, err.Error(), "parse batch id")
	})
}

// TestBatchRepository_Get_NotFound: missing id → ports.ErrNotFound.
func TestBatchRepository_Get_NotFound(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewBatchRepository(pool)
	_, err := repo.Get(context.Background(), integrationBatchID("ff"))
	require.ErrorIs(t, err, ports.ErrNotFound)
}

// TestBatchRepository_Create_AtomicRollback: a bad notification (duplicate
// idempotency_key with a prior batch) rolls the whole batch back.
func TestBatchRepository_Create_AtomicRollback(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	repo := postgres.NewBatchRepository(pool)
	notifRepo := postgres.NewNotificationRepository(pool)
	ctx := context.Background()

	// Seed an existing notification holding the idempotency key we will
	// collide with.
	existing := makeIntegrationNotification(t, integrationNotificationID("e0"))
	existing.IdempotencyKey = "atomic-test-key"
	require.NoError(t, notifRepo.Create(ctx, existing))

	// Build a 3-notification batch whose middle item collides on the key.
	batch := makeIntegrationBatch(t, integrationBatchID("02"))
	batch.Notifications[1].IdempotencyKey = "atomic-test-key"

	err := repo.Create(ctx, batch)
	require.Error(t, err)

	// Batch row did NOT land (transaction rolled back).
	_, err = repo.Get(ctx, batch.ID)
	require.ErrorIs(t, err, ports.ErrNotFound)
}
