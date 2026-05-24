//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/adapters/postgres"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// integrationLogID returns a UUID-v7-shaped log id.
func integrationLogID(suffix string) domain.LogID {
	return domain.LogID("01940000-0000-7000-8000-0000000000" + suffix)
}

// seedNotificationFor seeds a single notification so log rows can satisfy
// the FK on notification_logs.notification_id.
func seedNotificationFor(t *testing.T, repo *postgres.NotificationRepository, id domain.NotificationID) {
	t.Helper()
	n := makeIntegrationNotification(t, id)
	require.NoError(t, repo.Create(context.Background(), n))
}

// TestNotificationLogRepository_AppendAndList: 3 log rows for one
// notification, returned in append order, round-trip clean.
func TestNotificationLogRepository_AppendAndList(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	notifRepo := postgres.NewNotificationRepository(pool)
	logRepo := postgres.NewNotificationLogRepository(pool)
	ctx := context.Background()

	notifID := integrationNotificationID("c1")
	seedNotificationFor(t, notifRepo, notifID)

	events := []domain.LogEvent{
		domain.LogEventCreated,
		domain.LogEventQueued,
		domain.LogEventDelivered,
	}
	for i, ev := range events {
		entry, err := domain.NewNotificationLog(domain.NewNotificationLogInput{
			ID:             integrationLogID(charPair("c", i+2)),
			NotificationID: notifID,
			CorrelationID:  "01940000-0000-7000-8000-0000000000c0",
			Event:          ev,
		}, fixedIntegrationNow.Add(time.Duration(i)*time.Second))
		require.NoError(t, err)
		require.NoError(t, logRepo.Append(ctx, entry))
	}

	got, err := logRepo.List(ctx, notifID)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, domain.LogEventCreated, got[0].Event)
	require.Equal(t, domain.LogEventQueued, got[1].Event)
	require.Equal(t, domain.LogEventDelivered, got[2].Event)
}

// TestNotificationLogRepository_AppendWithDetails: JSONB details round-trip.
func TestNotificationLogRepository_AppendWithDetails(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	notifRepo := postgres.NewNotificationRepository(pool)
	logRepo := postgres.NewNotificationLogRepository(pool)
	ctx := context.Background()

	notifID := integrationNotificationID("d1")
	seedNotificationFor(t, notifRepo, notifID)

	entry, err := domain.NewNotificationLog(domain.NewNotificationLogInput{
		ID:             integrationLogID("d2"),
		NotificationID: notifID,
		CorrelationID:  "01940000-0000-7000-8000-0000000000c0",
		Event:          domain.LogEventFailed,
		Details: map[string]any{
			"provider_code": float64(503),
			"reason":        "provider 503",
			"retryable":     true,
		},
	}, fixedIntegrationNow)
	require.NoError(t, err)
	require.NoError(t, logRepo.Append(ctx, entry))

	got, err := logRepo.List(ctx, notifID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].Details)
	require.Equal(t, float64(503), got[0].Details["provider_code"])
	require.Equal(t, "provider 503", got[0].Details["reason"])
	require.Equal(t, true, got[0].Details["retryable"])
}

// TestNotificationLogRepository_ListEmptyForUnknown: list for an id that
// has no log rows returns empty, not error.
func TestNotificationLogRepository_ListEmptyForUnknown(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	logRepo := postgres.NewNotificationLogRepository(pool)
	got, err := logRepo.List(context.Background(), integrationNotificationID("ff"))
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestNotificationLogRepository_IsolationByNotification: log rows for one
// notification do not leak into the trace of another.
func TestNotificationLogRepository_IsolationByNotification(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	notifRepo := postgres.NewNotificationRepository(pool)
	logRepo := postgres.NewNotificationLogRepository(pool)
	ctx := context.Background()

	mine := integrationNotificationID("e1")
	other := integrationNotificationID("e2")
	seedNotificationFor(t, notifRepo, mine)
	seedNotificationFor(t, notifRepo, other)

	// 1 entry for `mine`, 2 entries for `other` — total 3, but `mine`'s
	// trace must only contain one.
	owners := []domain.NotificationID{mine, other, other}
	for i, owner := range owners {
		entry, err := domain.NewNotificationLog(domain.NewNotificationLogInput{
			ID:             integrationLogID(charPair("e", i+3)),
			NotificationID: owner,
			CorrelationID:  "01940000-0000-7000-8000-0000000000c0",
			Event:          domain.LogEventCreated,
		}, fixedIntegrationNow)
		require.NoError(t, err)
		require.NoError(t, logRepo.Append(ctx, entry))
	}

	mineLogs, err := logRepo.List(ctx, mine)
	require.NoError(t, err)
	require.Len(t, mineLogs, 1, "mine should have 1 row, not other's 2")
	require.Equal(t, mine, mineLogs[0].NotificationID)

	otherLogs, err := logRepo.List(ctx, other)
	require.NoError(t, err)
	require.Len(t, otherLogs, 2)
}
