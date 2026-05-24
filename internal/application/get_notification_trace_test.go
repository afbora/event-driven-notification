package application_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// seedLogEntry appends a notification_logs row for the given notification.
// It writes through the fake so subsequent List calls see the entry.
func seedLogEntry(t *testing.T, logRepo *fakeNotificationLogRepo, notifID domain.NotificationID, event domain.LogEvent, logID domain.LogID) {
	t.Helper()
	entry, err := domain.NewNotificationLog(domain.NewNotificationLogInput{
		ID:             logID,
		NotificationID: notifID,
		CorrelationID:  "01CORRTRACE000000000000000",
		Event:          event,
	}, fixedAppNow)
	require.NoError(t, err)
	require.NoError(t, logRepo.Append(context.Background(), entry))
}

func newGetNotificationTrace(t *testing.T) (
	*application.GetNotificationTrace,
	*fakeNotificationRepo,
	*fakeNotificationLogRepo,
) {
	t.Helper()
	repo := newFakeNotificationRepo()
	logRepo := newFakeNotificationLogRepo()
	uc := application.NewGetNotificationTrace(repo, logRepo)
	return uc, repo, logRepo
}

// TestGetNotificationTrace_HappyPath: notification exists, 3 log entries
// seeded, all 3 returned in append order.
func TestGetNotificationTrace_HappyPath(t *testing.T) {
	uc, repo, logRepo := newGetNotificationTrace(t)
	n := seedNotificationInStatus(t, repo, "01NOTIFTRACE", domain.StatusDelivered)

	seedLogEntry(t, logRepo, n.ID, domain.LogEventCreated, "01LOG01")
	seedLogEntry(t, logRepo, n.ID, domain.LogEventQueued, "01LOG02")
	seedLogEntry(t, logRepo, n.ID, domain.LogEventDelivered, "01LOG03")

	entries, err := uc.Execute(context.Background(), application.GetNotificationTraceInput{
		NotificationID: n.ID,
	})
	require.NoError(t, err)
	require.Len(t, entries, 3)
	require.Equal(t, domain.LogEventCreated, entries[0].Event)
	require.Equal(t, domain.LogEventQueued, entries[1].Event)
	require.Equal(t, domain.LogEventDelivered, entries[2].Event)
}

// TestGetNotificationTrace_NotFound: missing notification surfaces
// ports.ErrNotFound; the log repository is never consulted.
func TestGetNotificationTrace_NotFound(t *testing.T) {
	uc, _, _ := newGetNotificationTrace(t)

	_, err := uc.Execute(context.Background(), application.GetNotificationTraceInput{
		NotificationID: "01MISSING000000000000000000",
	})
	require.ErrorIs(t, err, ports.ErrNotFound)
}

// TestGetNotificationTrace_EmptyTrace: notification exists but no log rows
// have been written yet. Returns an empty slice and a nil error — the
// "no logs" case is not an error.
func TestGetNotificationTrace_EmptyTrace(t *testing.T) {
	uc, repo, _ := newGetNotificationTrace(t)
	n := seedNotificationInStatus(t, repo, "01NOTIFEMPTY", domain.StatusPending)

	entries, err := uc.Execute(context.Background(), application.GetNotificationTraceInput{
		NotificationID: n.ID,
	})
	require.NoError(t, err)
	require.Empty(t, entries)
}

// TestGetNotificationTrace_IsolatedByNotification: logs for another
// notification id must not leak into the trace of this one.
func TestGetNotificationTrace_IsolatedByNotification(t *testing.T) {
	uc, repo, logRepo := newGetNotificationTrace(t)
	mine := seedNotificationInStatus(t, repo, "01NOTIFMINE0", domain.StatusPending)
	other := seedNotificationInStatus(t, repo, "01NOTIFOTHER", domain.StatusPending)

	seedLogEntry(t, logRepo, mine.ID, domain.LogEventCreated, "01LOG01")
	seedLogEntry(t, logRepo, other.ID, domain.LogEventCreated, "01LOG02")
	seedLogEntry(t, logRepo, other.ID, domain.LogEventQueued, "01LOG03")

	entries, err := uc.Execute(context.Background(), application.GetNotificationTraceInput{
		NotificationID: mine.ID,
	})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, mine.ID, entries[0].NotificationID)
}
