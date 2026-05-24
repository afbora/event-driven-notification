package application_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// makeNotificationForReconciler builds a notification in the requested status
// and persists it through the fake repo, so reconciler updates round-trip
// correctly through UpdateStatus.
func makeNotificationForReconciler(t *testing.T, repo *fakeNotificationRepo, id domain.NotificationID, target domain.Status) *domain.Notification {
	t.Helper()
	return seedNotificationInStatus(t, repo, id, target)
}

func newReconcileStuck(t *testing.T) (
	*application.ReconcileStuckNotifications,
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
	uc := application.NewReconcileStuckNotifications(repo, logRepo, queue, idGen, clock)
	return uc, repo, logRepo, queue
}

// TestReconcileStuckNotifications_Nothing: empty results across the board →
// every counter zero, no log entries, no enqueues.
func TestReconcileStuckNotifications_Nothing(t *testing.T) {
	uc, repo, logRepo, queue := newReconcileStuck(t)
	repo.SetReconcilerResults(nil, nil, nil)

	out, err := uc.Execute(context.Background(), application.ReconcileStuckNotificationsInput{})
	require.NoError(t, err)
	require.Equal(t, 0, out.OrphanedPendingReenqueued)
	require.Equal(t, 0, out.StuckProcessingFailed)
	require.Equal(t, 0, out.OverdueRetryingReenqueued)
	require.Empty(t, logRepo.entries)
	require.Empty(t, queue.items)
}

// TestReconcileStuckNotifications_OrphanedPending: one notification sat in
// pending past the threshold → marked queued, log written, enqueued.
func TestReconcileStuckNotifications_OrphanedPending(t *testing.T) {
	uc, repo, logRepo, queue := newReconcileStuck(t)
	n := makeNotificationForReconciler(t, repo, "01NOTIFORPH", domain.StatusPending)
	repo.SetReconcilerResults([]*domain.Notification{n}, nil, nil)

	out, err := uc.Execute(context.Background(), application.ReconcileStuckNotificationsInput{})
	require.NoError(t, err)
	require.Equal(t, 1, out.OrphanedPendingReenqueued)

	// Status transitioned to queued.
	require.Equal(t, domain.StatusQueued, repo.store[n.ID].Status)

	// Queued log row + queue.Enqueue.
	require.Len(t, logRepo.entries, 1)
	require.Equal(t, domain.LogEventQueued, logRepo.entries[0].Event)
	require.Len(t, queue.items, 1)
	require.Equal(t, n.ID, queue.items[0].NotificationID)
}

// TestReconcileStuckNotifications_StuckProcessing: notification in processing
// past the threshold → marked failed with reason worker_timeout, log written,
// no enqueue (terminal state).
func TestReconcileStuckNotifications_StuckProcessing(t *testing.T) {
	uc, repo, logRepo, queue := newReconcileStuck(t)
	n := makeNotificationForReconciler(t, repo, "01NOTIFSTUCK", domain.StatusProcessing)
	repo.SetReconcilerResults(nil, []*domain.Notification{n}, nil)

	out, err := uc.Execute(context.Background(), application.ReconcileStuckNotificationsInput{})
	require.NoError(t, err)
	require.Equal(t, 1, out.StuckProcessingFailed)

	final := repo.store[n.ID]
	require.Equal(t, domain.StatusFailed, final.Status)
	require.Equal(t, "worker_timeout", final.LastError)

	require.Len(t, logRepo.entries, 1)
	require.Equal(t, domain.LogEventFailed, logRepo.entries[0].Event)

	// No enqueue — terminal state.
	require.Empty(t, queue.items)
}

// TestReconcileStuckNotifications_OverdueRetrying: notification in retrying
// past NextRetryAt → re-enqueued, status unchanged (still retrying).
func TestReconcileStuckNotifications_OverdueRetrying(t *testing.T) {
	uc, repo, logRepo, queue := newReconcileStuck(t)
	n := makeNotificationForReconciler(t, repo, "01NOTIFOVERD", domain.StatusRetrying)
	repo.SetReconcilerResults(nil, nil, []*domain.Notification{n})

	out, err := uc.Execute(context.Background(), application.ReconcileStuckNotificationsInput{})
	require.NoError(t, err)
	require.Equal(t, 1, out.OverdueRetryingReenqueued)

	// Status stays retrying — re-enqueue alone, no status transition.
	require.Equal(t, domain.StatusRetrying, repo.store[n.ID].Status)

	// Queue receives a re-enqueue; no log entry for an unchanged status.
	require.Len(t, queue.items, 1)
	require.Equal(t, n.ID, queue.items[0].NotificationID)
	require.Empty(t, logRepo.entries)
}

// TestReconcileStuckNotifications_AllThree: every bucket populated in a
// single sweep — each path runs independently.
func TestReconcileStuckNotifications_AllThree(t *testing.T) {
	uc, repo, logRepo, queue := newReconcileStuck(t)
	orph := makeNotificationForReconciler(t, repo, "01NOTIFORPH", domain.StatusPending)
	stuck := makeNotificationForReconciler(t, repo, "01NOTIFSTUC", domain.StatusProcessing)
	overd := makeNotificationForReconciler(t, repo, "01NOTIFOVRD", domain.StatusRetrying)

	repo.SetReconcilerResults(
		[]*domain.Notification{orph},
		[]*domain.Notification{stuck},
		[]*domain.Notification{overd},
	)

	out, err := uc.Execute(context.Background(), application.ReconcileStuckNotificationsInput{})
	require.NoError(t, err)
	require.Equal(t, 1, out.OrphanedPendingReenqueued)
	require.Equal(t, 1, out.StuckProcessingFailed)
	require.Equal(t, 1, out.OverdueRetryingReenqueued)

	require.Equal(t, domain.StatusQueued, repo.store[orph.ID].Status)
	require.Equal(t, domain.StatusFailed, repo.store[stuck.ID].Status)
	require.Equal(t, domain.StatusRetrying, repo.store[overd.ID].Status)

	// 2 log rows: queued (orphaned) + failed (stuck). No log for the
	// overdue path because the status did not change.
	require.Len(t, logRepo.entries, 2)
	// 2 enqueues: orphaned + overdue.
	require.Len(t, queue.items, 2)
}
