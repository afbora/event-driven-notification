package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// TestGetBatch_ReturnsBatchFromRepository: the use case is a thin
// passthrough — the batch the repository holds is returned verbatim,
// member notifications included. This is the HTTP-facing read for
// `GET /api/v1/notifications/batch/{id}`.
func TestGetBatch_ReturnsBatchFromRepository(t *testing.T) {
	repo := newFakeBatchRepo()

	bID := domain.BatchID("01940000-0000-7000-8000-00000000bbbb")

	mkNotif := func(id, recipient string) *domain.Notification {
		n, err := domain.NewNotification(domain.NewNotificationInput{
			ID:            domain.NotificationID(id),
			CorrelationID: "01HXYZBATCHCORR000001",
			Channel:       domain.ChannelSMS,
			Priority:      domain.PriorityNormal,
			Recipient:     recipient,
			Content:       "hello",
		}, fixedAppNow)
		require.NoError(t, err)
		return n
	}

	stored := &domain.Batch{
		ID:            bID,
		CorrelationID: "01HXYZBATCHCORR000001",
		Notifications: []*domain.Notification{
			mkNotif("01940000-0000-7000-8000-000000000a01", "+905551110001"),
			mkNotif("01940000-0000-7000-8000-000000000a02", "+905551110002"),
		},
		CreatedAt: fixedAppNow,
	}
	require.NoError(t, repo.Create(context.Background(), stored))

	uc := application.NewGetBatch(repo)
	got, err := uc.Execute(context.Background(), application.GetBatchInput{ID: bID})
	require.NoError(t, err)
	require.Equal(t, bID, got.ID)
	require.Equal(t, "01HXYZBATCHCORR000001", got.CorrelationID)
	require.Len(t, got.Notifications, 2)
}

// TestGetBatch_NotFoundPropagates: an unknown id surfaces
// ports.ErrNotFound — the HTTP translator turns it into a 404.
func TestGetBatch_NotFoundPropagates(t *testing.T) {
	repo := newFakeBatchRepo()
	uc := application.NewGetBatch(repo)

	_, err := uc.Execute(context.Background(), application.GetBatchInput{
		ID: domain.BatchID("01940000-0000-7000-8000-0000000000ff"),
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ports.ErrNotFound),
		"unknown id must surface ports.ErrNotFound for the HTTP layer to translate")
}
