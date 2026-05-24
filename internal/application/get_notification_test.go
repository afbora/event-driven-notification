package application_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// TestGetNotification_HappyPath: seeded notification round-trips through the
// use case. The fake's shallow-copy semantics mean we compare by id and
// status rather than asserting pointer equality.
func TestGetNotification_HappyPath(t *testing.T) {
	repo := newFakeNotificationRepo()
	uc := application.NewGetNotification(repo)

	seeded := seedNotificationInStatus(t, repo, "01NOTIF42", domain.StatusDelivered)

	got, err := uc.Execute(context.Background(), application.GetNotificationInput{
		ID: seeded.ID,
	})
	require.NoError(t, err)
	require.Equal(t, seeded.ID, got.ID)
	require.Equal(t, domain.StatusDelivered, got.Status)
	require.Equal(t, seeded.CorrelationID, got.CorrelationID)
}

// TestGetNotification_NotFound: unknown id surfaces ports.ErrNotFound.
func TestGetNotification_NotFound(t *testing.T) {
	uc := application.NewGetNotification(newFakeNotificationRepo())

	_, err := uc.Execute(context.Background(), application.GetNotificationInput{
		ID: "01MISSING000000000000000000",
	})
	require.ErrorIs(t, err, ports.ErrNotFound)
}
