package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
)

func newListNotifications(t *testing.T) (*application.ListNotifications, *fakeNotificationRepo) {
	t.Helper()
	repo := newFakeNotificationRepo()
	uc := application.NewListNotifications(repo)
	return uc, repo
}

// TestListNotifications_NoFilters: empty input → empty filter, default limit
// (20), pass-through to repo, output reflects repo's return.
func TestListNotifications_NoFilters(t *testing.T) {
	uc, repo := newListNotifications(t)

	expected := []*domain.Notification{
		seedNotificationInStatus(t, repo, "01NOTIF01", domain.StatusPending),
		seedNotificationInStatus(t, repo, "01NOTIF02", domain.StatusDelivered),
	}
	repo.SetListResult(expected, "next-page-cursor")

	out, err := uc.Execute(context.Background(), application.ListNotificationsInput{})
	require.NoError(t, err)
	require.Equal(t, expected, out.Notifications)
	require.Equal(t, "next-page-cursor", out.NextCursor)

	// Repo was called with an empty filter and the default limit.
	require.Len(t, repo.listCalls, 1)
	require.Nil(t, repo.listCalls[0].Filter.Status)
	require.Nil(t, repo.listCalls[0].Filter.Channel)
	require.Nil(t, repo.listCalls[0].Filter.BatchID)
	require.Empty(t, repo.listCalls[0].Cursor)
	require.Equal(t, 20, repo.listCalls[0].Limit, "default limit")
}

// TestListNotifications_AllFiltersPropagate: every populated input field
// reaches the repo in the expected slot.
func TestListNotifications_AllFiltersPropagate(t *testing.T) {
	uc, repo := newListNotifications(t)
	repo.SetListResult(nil, "")

	batchID := domain.BatchID("01BATCH99")
	createdAfter := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	createdBefore := time.Date(2026, 5, 24, 0, 0, 0, 0, time.UTC)

	_, err := uc.Execute(context.Background(), application.ListNotificationsInput{
		Status:        "delivered",
		Channel:       "sms",
		BatchID:       &batchID,
		CreatedAfter:  &createdAfter,
		CreatedBefore: &createdBefore,
		Cursor:        "previous-cursor",
		Limit:         50,
	})
	require.NoError(t, err)

	require.Len(t, repo.listCalls, 1)
	call := repo.listCalls[0]
	require.NotNil(t, call.Filter.Status)
	require.Equal(t, domain.StatusDelivered, *call.Filter.Status)
	require.NotNil(t, call.Filter.Channel)
	require.Equal(t, domain.ChannelSMS, *call.Filter.Channel)
	require.Equal(t, &batchID, call.Filter.BatchID)
	require.Equal(t, &createdAfter, call.Filter.CreatedAfter)
	require.Equal(t, &createdBefore, call.Filter.CreatedBefore)
	require.Equal(t, "previous-cursor", call.Cursor)
	require.Equal(t, 50, call.Limit)
}

// TestListNotifications_InvalidStatus: domain parser rejects the value, the
// use case returns its error, and the repo is never consulted.
func TestListNotifications_InvalidStatus(t *testing.T) {
	uc, repo := newListNotifications(t)

	_, err := uc.Execute(context.Background(), application.ListNotificationsInput{
		Status: "sent",
	})
	require.ErrorIs(t, err, domain.ErrInvalidStatus)
	require.Empty(t, repo.listCalls, "repo must not be called on validation failure")
}

// TestListNotifications_InvalidChannel: same shape as the status case.
func TestListNotifications_InvalidChannel(t *testing.T) {
	uc, repo := newListNotifications(t)

	_, err := uc.Execute(context.Background(), application.ListNotificationsInput{
		Channel: "fax",
	})
	require.ErrorIs(t, err, domain.ErrInvalidChannel)
	require.Empty(t, repo.listCalls)
}

// TestListNotifications_LimitClamp: limits outside (0, 100] snap back to the
// default of 20 so callers cannot exhaust the database with a wide query.
func TestListNotifications_LimitClamp(t *testing.T) {
	cases := []struct {
		name    string
		limit   int
		wantOut int
	}{
		{"zero falls to default", 0, 20},
		{"negative falls to default", -5, 20},
		{"above ceiling falls to default", 1000, 20},
		{"in range passes through", 75, 75},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uc, repo := newListNotifications(t)
			repo.SetListResult(nil, "")

			_, err := uc.Execute(context.Background(), application.ListNotificationsInput{
				Limit: tc.limit,
			})
			require.NoError(t, err)
			require.Equal(t, tc.wantOut, repo.listCalls[0].Limit)
		})
	}
}
