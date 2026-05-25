package application_test

import (
	"context"
	"errors"
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
	repo.SetReconcilerResults(nil, nil, nil, nil)

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
	repo.SetReconcilerResults([]*domain.Notification{n}, nil, nil, nil)

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
	repo.SetReconcilerResults(nil, []*domain.Notification{n}, nil, nil)

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

// TestReconcileStuckNotifications_StuckQueued: closes the dual-write
// race documented in CLAUDE.md §3.11 — when a worker dequeues a task
// before CreateNotification has flipped the row from pending to
// queued, the atomic claim (status IN ('queued','retrying')) is a
// no-op, asynq consumes the task, the API then writes queued, and
// the row sits in queued forever with no task on the queue.
//
// Recovery is a re-enqueue without changing status: the row is
// already in the correct state, only the asynq task is missing. No
// log row is written either — there is no new event, just a missed
// delivery being recovered.
func TestReconcileStuckNotifications_StuckQueued(t *testing.T) {
	uc, repo, logRepo, queue := newReconcileStuck(t)
	n := makeNotificationForReconciler(t, repo, "01NOTIFQRACE", domain.StatusQueued)
	repo.SetReconcilerResults(nil, nil, nil, []*domain.Notification{n})

	out, err := uc.Execute(context.Background(), application.ReconcileStuckNotificationsInput{})
	require.NoError(t, err)

	require.Equal(t, 1, out.StuckQueuedReenqueued,
		"the sweep must count the recovered row")
	require.Equal(t, domain.StatusQueued, repo.store[n.ID].Status,
		"stuck-queued recovery must NOT transition status — the row is already correct")
	require.Len(t, queue.items, 1,
		"the recovery is a single asynq re-enqueue")
	require.Equal(t, n.ID, queue.items[0].NotificationID)
	require.Empty(t, logRepo.entries,
		"no log row is written: there is no new event, just a missed delivery being recovered")
}

// TestReconcileStuckNotifications_StuckQueued_RepoError: the fourth
// sweep's repo lookup error must surface as a wrapped error from
// Execute, not be swallowed. Pins the wrap prefix so an operator
// reading logs can trace which sweep failed.
func TestReconcileStuckNotifications_StuckQueued_RepoError(t *testing.T) {
	uc, repo, _, queue := newReconcileStuck(t)
	repo.stuckQueuedErr = errors.New("postgres: connection refused")

	out, err := uc.Execute(context.Background(), application.ReconcileStuckNotificationsInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "find stuck queued:",
		"the error wrap must name the sweep so the failure is locatable in logs")
	require.Equal(t, 0, out.StuckQueuedReenqueued,
		"counter must stay at zero when the sweep aborts")
	require.Empty(t, queue.items,
		"no re-enqueue on the failure path")
}

// TestReconcileStuckNotifications_StuckQueued_HandlerError: when the
// queue rejects the re-enqueue on a stuck-queued row, the error must
// propagate out of Execute via handleStuckQueued's wrap. Pins both
// the inner handler's wrap prefix and Execute's loop-abort semantics.
func TestReconcileStuckNotifications_StuckQueued_HandlerError(t *testing.T) {
	uc, repo, _, queue := newReconcileStuck(t)
	n := makeNotificationForReconciler(t, repo, "01NOTIFQERR", domain.StatusQueued)
	repo.SetReconcilerResults(nil, nil, nil, []*domain.Notification{n})
	queue.enqErr = errors.New("asynq: redis down")

	out, err := uc.Execute(context.Background(), application.ReconcileStuckNotificationsInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "re-enqueue stuck queued",
		"handleStuckQueued's wrap must name the recovery path")
	require.Contains(t, err.Error(), string(n.ID),
		"the wrap must carry the offending notification id for log triage")
	require.Equal(t, 0, out.StuckQueuedReenqueued,
		"counter increments only after a successful enqueue, not before")
}

// TestReconcileStuckNotifications_OrphanedPending_RepoError: a
// FindOrphanedPending failure must surface as a wrapped error from
// Execute and abort the pass before any subsequent sweep runs. Pins
// the wrap prefix so an operator reading logs knows which sweep
// failed.
func TestReconcileStuckNotifications_OrphanedPending_RepoError(t *testing.T) {
	uc, repo, _, queue := newReconcileStuck(t)
	repo.orphanedPendingErr = errors.New("postgres: connection refused")

	out, err := uc.Execute(context.Background(), application.ReconcileStuckNotificationsInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "find orphaned pending:")
	require.Equal(t, 0, out.OrphanedPendingReenqueued)
	require.Empty(t, queue.items)
}

// TestReconcileStuckNotifications_OrphanedPending_HandlerError: the
// inner handler can fail mid-iteration (here forced via logRepo's
// Append error injection — recordEvent propagates it up through
// handleOrphanedPending). Execute must abort and return the wrapped
// failure without incrementing the counter.
func TestReconcileStuckNotifications_OrphanedPending_HandlerError(t *testing.T) {
	uc, repo, logRepo, queue := newReconcileStuck(t)
	n := makeNotificationForReconciler(t, repo, "01NOTIFOPERR", domain.StatusPending)
	repo.SetReconcilerResults([]*domain.Notification{n}, nil, nil, nil)
	logRepo.appendErr = errors.New("logrepo: write timeout")

	out, err := uc.Execute(context.Background(), application.ReconcileStuckNotificationsInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "append log entry:",
		"the handler wraps the log error with its own context")
	require.Equal(t, 0, out.OrphanedPendingReenqueued)
	require.Empty(t, queue.items, "no re-enqueue when the log write fails first")
}

// TestReconcileStuckNotifications_StuckProcessing_RepoError mirrors
// OrphanedPending_RepoError for the second sweep — different wrap
// prefix, same loop-abort semantics.
func TestReconcileStuckNotifications_StuckProcessing_RepoError(t *testing.T) {
	uc, repo, _, queue := newReconcileStuck(t)
	repo.stuckProcessingErr = errors.New("postgres: deadlock detected")

	out, err := uc.Execute(context.Background(), application.ReconcileStuckNotificationsInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "find stuck processing:")
	require.Equal(t, 0, out.StuckProcessingFailed)
	require.Empty(t, queue.items)
}

// TestReconcileStuckNotifications_StuckProcessing_HandlerError covers
// the inner-handler error path for the processing sweep — the failure
// surfaces through recordEvent (logRepo.Append) and the counter stays
// at zero.
func TestReconcileStuckNotifications_StuckProcessing_HandlerError(t *testing.T) {
	uc, repo, logRepo, queue := newReconcileStuck(t)
	n := makeNotificationForReconciler(t, repo, "01NOTIFSPERR", domain.StatusProcessing)
	repo.SetReconcilerResults(nil, []*domain.Notification{n}, nil, nil)
	logRepo.appendErr = errors.New("logrepo: write timeout")

	out, err := uc.Execute(context.Background(), application.ReconcileStuckNotificationsInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "append log entry:")
	require.Equal(t, 0, out.StuckProcessingFailed)
	require.Empty(t, queue.items)
}

// TestReconcileStuckNotifications_OverdueRetrying_RepoError pins the
// third sweep's wrap prefix and abort semantics.
func TestReconcileStuckNotifications_OverdueRetrying_RepoError(t *testing.T) {
	uc, repo, _, queue := newReconcileStuck(t)
	repo.overdueRetryingErr = errors.New("postgres: query cancelled")

	out, err := uc.Execute(context.Background(), application.ReconcileStuckNotificationsInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "find overdue retrying:")
	require.Equal(t, 0, out.OverdueRetryingReenqueued)
	require.Empty(t, queue.items)
}

// TestReconcileStuckNotifications_OverdueRetrying_HandlerError: the
// overdue-retrying handler is pure re-enqueue (no log write); the
// only path that can fail is queue.Enqueue. Pins handleOverdueRetrying's
// wrap and Execute's loop-abort semantics.
func TestReconcileStuckNotifications_OverdueRetrying_HandlerError(t *testing.T) {
	uc, repo, _, queue := newReconcileStuck(t)
	n := makeNotificationForReconciler(t, repo, "01NOTIFORERR", domain.StatusRetrying)
	repo.SetReconcilerResults(nil, nil, []*domain.Notification{n}, nil)
	queue.enqErr = errors.New("asynq: redis down")

	out, err := uc.Execute(context.Background(), application.ReconcileStuckNotificationsInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "re-enqueue overdue",
		"handleOverdueRetrying's wrap must name the recovery path")
	require.Contains(t, err.Error(), string(n.ID),
		"the wrap must carry the offending notification id")
	require.Equal(t, 0, out.OverdueRetryingReenqueued)
}

// TestReconcileStuckNotifications_OverdueRetrying: notification in retrying
// past NextRetryAt → re-enqueued, status unchanged (still retrying).
func TestReconcileStuckNotifications_OverdueRetrying(t *testing.T) {
	uc, repo, logRepo, queue := newReconcileStuck(t)
	n := makeNotificationForReconciler(t, repo, "01NOTIFOVERD", domain.StatusRetrying)
	repo.SetReconcilerResults(nil, nil, []*domain.Notification{n}, nil)

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
		nil,
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
