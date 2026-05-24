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

func newCancelNotification(t *testing.T) (
	*application.CancelNotification,
	*fakeNotificationRepo,
	*fakeNotificationLogRepo,
	*fakeQueue,
) {
	t.Helper()
	repo := newFakeNotificationRepo()
	logRepo := newFakeNotificationLogRepo()
	queue := newFakeQueue()
	idGen := newDefaultFakeIDs()
	clock := newFakeClock(fixedAppNow)
	uc := application.NewCancelNotification(repo, logRepo, queue, idGen, clock)
	return uc, repo, logRepo, queue
}

// seedNotificationInStatus creates a fresh notification and walks it through
// the FSM to the requested target, then stuffs it into the fake repo. The
// helper centralizes the setup so individual tests read like "given a
// notification in status X, when we cancel, then ...".
func seedNotificationInStatus(t *testing.T, repo *fakeNotificationRepo, id domain.NotificationID, target domain.Status) *domain.Notification {
	t.Helper()
	n, err := domain.NewNotification(domain.NewNotificationInput{
		ID:            id,
		CorrelationID: "01CORRSEED000000000000000000",
		Channel:       domain.ChannelSMS,
		Priority:      domain.PriorityNormal,
		Recipient:     "+905551234567",
		Content:       "test",
	}, fixedAppNow)
	require.NoError(t, err)

	switch target {
	case domain.StatusPending:
		// already pending
	case domain.StatusQueued:
		require.NoError(t, n.MarkQueued(fixedAppNow))
	case domain.StatusProcessing:
		require.NoError(t, n.MarkQueued(fixedAppNow))
		require.NoError(t, n.MarkProcessing(fixedAppNow))
	case domain.StatusDelivered:
		require.NoError(t, n.MarkQueued(fixedAppNow))
		require.NoError(t, n.MarkProcessing(fixedAppNow))
		require.NoError(t, n.MarkDelivered(fixedAppNow))
	case domain.StatusRetrying:
		require.NoError(t, n.MarkQueued(fixedAppNow))
		require.NoError(t, n.MarkProcessing(fixedAppNow))
		require.NoError(t, n.MarkRetrying(fixedAppNow, "test", fixedAppNow))
	default:
		t.Fatalf("seedNotificationInStatus: unsupported target %q", target)
	}

	repo.store[id] = n
	return n
}

// TestCancelNotification_HappyPath: pending → cancelled, log entry written,
// queue.Cancel invoked as a best-effort signal to the queue layer.
func TestCancelNotification_HappyPath(t *testing.T) {
	uc, repo, logRepo, queue := newCancelNotification(t)
	n := seedNotificationInStatus(t, repo, "01NOTIF99", domain.StatusPending)

	updated, err := uc.Execute(context.Background(), application.CancelNotificationInput{
		ID: n.ID,
	})
	require.NoError(t, err)

	// Status moved to cancelled, both in the returned value and in the
	// fake repo (UpdateStatus was actually invoked).
	require.Equal(t, domain.StatusCancelled, updated.Status)
	require.Equal(t, domain.StatusCancelled, repo.store[n.ID].Status)

	// notification_logs entry with event=cancelled.
	require.Len(t, logRepo.entries, 1)
	require.Equal(t, domain.LogEventCancelled, logRepo.entries[0].Event)
	require.Equal(t, n.ID, logRepo.entries[0].NotificationID)
	require.Equal(t, n.CorrelationID, logRepo.entries[0].CorrelationID)

	// queue.Cancel was called (best-effort hint to the queue layer).
	require.Equal(t, []domain.NotificationID{n.ID}, queue.cancelled)
}

// TestCancelNotification_FromQueued covers another cancellable state. The
// FSM allows it; the use case must too.
func TestCancelNotification_FromQueued(t *testing.T) {
	uc, repo, _, _ := newCancelNotification(t)
	n := seedNotificationInStatus(t, repo, "01NOTIF99", domain.StatusQueued)

	_, err := uc.Execute(context.Background(), application.CancelNotificationInput{ID: n.ID})
	require.NoError(t, err)
	require.Equal(t, domain.StatusCancelled, repo.store[n.ID].Status)
}

// TestCancelNotification_FromRetrying covers the third legal source state.
func TestCancelNotification_FromRetrying(t *testing.T) {
	uc, repo, _, _ := newCancelNotification(t)
	n := seedNotificationInStatus(t, repo, "01NOTIF99", domain.StatusRetrying)

	_, err := uc.Execute(context.Background(), application.CancelNotificationInput{ID: n.ID})
	require.NoError(t, err)
	require.Equal(t, domain.StatusCancelled, repo.store[n.ID].Status)
}

// TestCancelNotification_NotFound: missing id surfaces ports.ErrNotFound;
// no log entry, no queue.Cancel call.
func TestCancelNotification_NotFound(t *testing.T) {
	uc, _, logRepo, queue := newCancelNotification(t)

	_, err := uc.Execute(context.Background(), application.CancelNotificationInput{
		ID: domain.NotificationID("01MISSINGNOTIF000000000000"),
	})
	require.ErrorIs(t, err, ports.ErrNotFound)
	require.Empty(t, logRepo.entries)
	require.Empty(t, queue.cancelled)
}

// TestCancelNotification_TerminalRejected: canceling a delivered notification
// must surface ErrInvalidTransition (terminal states reject every outgoing
// edge per the FSM). No side effects on a domain rejection.
func TestCancelNotification_TerminalRejected(t *testing.T) {
	uc, repo, logRepo, queue := newCancelNotification(t)
	n := seedNotificationInStatus(t, repo, "01NOTIF99", domain.StatusDelivered)

	_, err := uc.Execute(context.Background(), application.CancelNotificationInput{ID: n.ID})
	require.True(t, errors.Is(err, domain.ErrInvalidTransition))
	require.Equal(t, domain.StatusDelivered, repo.store[n.ID].Status, "status must not change on failed transition")
	require.Empty(t, logRepo.entries)
	require.Empty(t, queue.cancelled)
}

// TestCancelNotification_ProcessingRejected: processing → cancelled is
// forbidden by the FSM (mid-flight provider call cannot be retracted).
func TestCancelNotification_ProcessingRejected(t *testing.T) {
	uc, repo, _, _ := newCancelNotification(t)
	n := seedNotificationInStatus(t, repo, "01NOTIF99", domain.StatusProcessing)

	_, err := uc.Execute(context.Background(), application.CancelNotificationInput{ID: n.ID})
	require.True(t, errors.Is(err, domain.ErrInvalidTransition))
	require.Equal(t, domain.StatusProcessing, repo.store[n.ID].Status)
}
