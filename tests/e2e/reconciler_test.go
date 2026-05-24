//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	asynqadapter "github.com/afbora/event-driven-notification/internal/adapters/asynq"
	pgadapter "github.com/afbora/event-driven-notification/internal/adapters/postgres"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/infrastructure/clock"
	"github.com/afbora/event-driven-notification/internal/infrastructure/id"

	hibikenasynq "github.com/hibiken/asynq"
)

// TestReconciler_StuckProcessing_MarksFailed: a notification stuck in
// `processing` for longer than the threshold (5 min, CLAUDE.md
// §3.11) is swept into `failed` with reason worker_timeout. This is
// the safety net beneath the safety net — without it, a worker
// crash mid-Send would orphan rows forever.
func TestReconciler_StuckProcessing_MarksFailed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	id1 := domain.NotificationID("019e5b00-0000-7000-8000-00000000d001")
	seedStuckProcessing(ctx, t, h, id1, 10*time.Minute)

	uc := newReconciler(t, h)
	out, err := uc.Execute(ctx, application.ReconcileStuckNotificationsInput{})
	require.NoError(t, err)

	require.Equal(t, 1, out.StuckProcessingFailed,
		"the stuck row must be counted in StuckProcessingFailed; got %+v", out)

	// The notification's status flipped to failed.
	var status, lastError string
	require.NoError(t, h.Pool.QueryRow(ctx,
		"SELECT status, COALESCE(last_error, '') FROM notifications WHERE id = $1", string(id1),
	).Scan(&status, &lastError))
	require.Equal(t, "failed", status, "stuck processing row must end up in failed")
	require.Contains(t, lastError, "worker_timeout",
		"failure reason must mention worker_timeout so operators know why")
}

// TestReconciler_OrphanedPending_Reenqueued: a notification stuck in
// `pending` for longer than the threshold (the dual-write race from
// ADR-0011 — persisted but never enqueued to asynq) is swept into
// `queued` and re-enqueued. Without this, the worker would never
// see it.
func TestReconciler_OrphanedPending_Reenqueued(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	id2 := domain.NotificationID("019e5b00-0000-7000-8000-00000000d002")
	seedOrphanedPending(ctx, t, h, id2, 10*time.Minute)

	uc := newReconciler(t, h)
	out, err := uc.Execute(ctx, application.ReconcileStuckNotificationsInput{})
	require.NoError(t, err)

	require.Equal(t, 1, out.OrphanedPendingReenqueued,
		"the orphan must be counted in OrphanedPendingReenqueued; got %+v", out)

	// Status is now queued (post-recovery transition).
	var status string
	require.NoError(t, h.Pool.QueryRow(ctx,
		"SELECT status FROM notifications WHERE id = $1", string(id2),
	).Scan(&status))
	require.Equal(t, "queued", status, "orphaned pending row must end up in queued")
}

// TestReconciler_ConcurrentInstances_NoDoubleClaim: two reconciler
// instances running against the same data must NOT double-claim
// rows — the FindStuck* queries use FOR UPDATE SKIP LOCKED so
// competing instances see disjoint rows. The test seeds 6 stuck
// rows, runs two reconcilers in parallel, and verifies the total of
// (stuckA + stuckB) is exactly 6 with no row processed twice.
func TestReconciler_ConcurrentInstances_NoDoubleClaim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	const stuckRows = 6
	for i := 0; i < stuckRows; i++ {
		idi := domain.NotificationID(fmt.Sprintf("019e5b00-0000-7000-8000-0000000010%02d", i))
		seedStuckProcessing(ctx, t, h, idi, 10*time.Minute)
	}

	var sumA, sumB atomic.Int32
	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})

	for _, sum := range []*atomic.Int32{&sumA, &sumB} {
		go func(counter *atomic.Int32) {
			defer wg.Done()
			uc := newReconciler(t, h)
			<-start
			out, err := uc.Execute(ctx, application.ReconcileStuckNotificationsInput{})
			require.NoError(t, err)
			counter.Add(int32(out.StuckProcessingFailed))
		}(sum)
	}
	close(start)
	wg.Wait()

	require.Equal(t, int32(stuckRows), sumA.Load()+sumB.Load(),
		"every stuck row must be processed exactly once across both reconcilers; A=%d B=%d",
		sumA.Load(), sumB.Load())

	// And every row is now failed — none was missed.
	var failed int
	require.NoError(t, h.Pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM notifications WHERE status = 'failed' AND last_error LIKE '%worker_timeout%'",
	).Scan(&failed))
	require.Equal(t, stuckRows, failed)
}

// newReconciler builds a fresh ReconcileStuckNotifications wired
// against the harness's pool + redis. Kept here rather than on the
// Harness because it is reconciler-specific and tests usually want
// to run a deterministic single pass.
func newReconciler(t *testing.T, h *Harness) *application.ReconcileStuckNotifications {
	t.Helper()
	repo := pgadapter.NewNotificationRepository(h.Pool)
	logRepo := pgadapter.NewNotificationLogRepository(h.Pool)
	queue := asynqadapter.NewQueue(hibikenasynq.RedisClientOpt{Addr: h.RedisAddr})
	t.Cleanup(func() { _ = queue.Close() })
	return application.NewReconcileStuckNotifications(repo, logRepo, queue, id.New(), clock.New())
}

// seedStuckProcessing inserts a notification, walks it to processing,
// then back-dates its updated_at via raw SQL so the reconciler's
// FindStuckProcessing query (updated_at < now-5m) picks it up.
func seedStuckProcessing(ctx context.Context, t *testing.T, h *Harness, notifID domain.NotificationID, age time.Duration) {
	t.Helper()
	repo := pgadapter.NewNotificationRepository(h.Pool)
	now := time.Now().UTC()

	n, err := domain.NewNotification(domain.NewNotificationInput{
		ID:            notifID,
		CorrelationID: "01HXYZRECONSTUCK00000001",
		Channel:       domain.ChannelSMS,
		Priority:      domain.PriorityNormal,
		Recipient:     "+15555550100",
		Content:       "stuck-test",
	}, now)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, n))

	require.NoError(t, n.MarkQueued(now))
	require.NoError(t, repo.UpdateStatus(ctx, n, domain.StatusPending))
	require.NoError(t, n.MarkProcessing(now))
	require.NoError(t, repo.UpdateStatus(ctx, n, domain.StatusQueued))

	// Back-date so the reconciler's 5-minute threshold is exceeded.
	old := now.Add(-age)
	_, err = h.Pool.Exec(ctx,
		"UPDATE notifications SET updated_at = $1 WHERE id = $2",
		old, string(notifID))
	require.NoError(t, err)
}

// seedOrphanedPending inserts a notification in pending state and
// back-dates its created_at so the reconciler's threshold engages.
func seedOrphanedPending(ctx context.Context, t *testing.T, h *Harness, notifID domain.NotificationID, age time.Duration) {
	t.Helper()
	repo := pgadapter.NewNotificationRepository(h.Pool)
	now := time.Now().UTC()

	n, err := domain.NewNotification(domain.NewNotificationInput{
		ID:            notifID,
		CorrelationID: "01HXYZRECONORPHAN0000001",
		Channel:       domain.ChannelSMS,
		Priority:      domain.PriorityNormal,
		Recipient:     "+15555550101",
		Content:       "orphan-test",
	}, now)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, n))

	// Back-date created_at past the threshold.
	old := now.Add(-age)
	_, err = h.Pool.Exec(ctx,
		"UPDATE notifications SET created_at = $1 WHERE id = $2",
		old, string(notifID))
	require.NoError(t, err)
}
