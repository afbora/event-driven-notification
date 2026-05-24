//go:build integration

package asynq_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	hibikenasynq "github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"

	asynqadapter "github.com/afbora/event-driven-notification/internal/adapters/asynq"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// TestQueue_Enqueue_HighPriority: a notification enqueued at high priority
// lands in the "high" queue, with payload carrying the notification id.
func TestQueue_Enqueue_HighPriority(t *testing.T) {
	redisOpt, cleanup := setupRedisForAsynq(t)
	defer cleanup()

	q := asynqadapter.NewQueue(redisOpt)
	defer func() { _ = q.Close() }()

	inspector := hibikenasynq.NewInspector(redisOpt)
	defer func() { _ = inspector.Close() }()
	awaitInspector(t, inspector)

	notifID := domain.NotificationID("01940000-0000-7000-8000-000000000001")
	require.NoError(t, q.Enqueue(context.Background(), notifID, domain.PriorityHigh, ""))

	infos, err := inspector.ListPendingTasks("high")
	require.NoError(t, err)
	require.Len(t, infos, 1, "exactly one task should land in the high queue")

	var payload asynqadapter.ProcessNotificationPayload
	require.NoError(t, json.Unmarshal(infos[0].Payload, &payload))
	require.Equal(t, string(notifID), payload.NotificationID)
	require.Equal(t, asynqadapter.TypeProcessNotification, infos[0].Type)
}

// TestQueue_Enqueue_RoutingByPriority: each priority maps to its own queue;
// a high-priority task does not appear in the normal or low queues.
func TestQueue_Enqueue_RoutingByPriority(t *testing.T) {
	redisOpt, cleanup := setupRedisForAsynq(t)
	defer cleanup()

	q := asynqadapter.NewQueue(redisOpt)
	defer func() { _ = q.Close() }()

	inspector := hibikenasynq.NewInspector(redisOpt)
	defer func() { _ = inspector.Close() }()
	awaitInspector(t, inspector)

	ctx := context.Background()
	require.NoError(t, q.Enqueue(ctx, domain.NotificationID("01940000-0000-7000-8000-000000000011"), domain.PriorityHigh, ""))
	require.NoError(t, q.Enqueue(ctx, domain.NotificationID("01940000-0000-7000-8000-000000000012"), domain.PriorityNormal, ""))
	require.NoError(t, q.Enqueue(ctx, domain.NotificationID("01940000-0000-7000-8000-000000000013"), domain.PriorityLow, ""))

	for _, tc := range []struct {
		queue string
		want  int
	}{
		{"high", 1},
		{"normal", 1},
		{"low", 1},
	} {
		infos, err := inspector.ListPendingTasks(tc.queue)
		require.NoError(t, err)
		require.Lenf(t, infos, tc.want, "%s queue", tc.queue)
	}
}

// TestQueue_EnqueueScheduled: a future-delivery task lands in asynq's
// scheduled set, not in the pending queue. asynq promotes it to pending
// when `at` arrives — but for the integration test we only assert it
// starts in the scheduled set so the test does not depend on time elapsing.
func TestQueue_EnqueueScheduled(t *testing.T) {
	redisOpt, cleanup := setupRedisForAsynq(t)
	defer cleanup()

	q := asynqadapter.NewQueue(redisOpt)
	defer func() { _ = q.Close() }()

	inspector := hibikenasynq.NewInspector(redisOpt)
	defer func() { _ = inspector.Close() }()
	awaitInspector(t, inspector)

	notifID := domain.NotificationID("01940000-0000-7000-8000-000000000031")
	at := time.Now().Add(1 * time.Hour)
	require.NoError(t, q.EnqueueScheduled(context.Background(), notifID, domain.PriorityNormal, at))

	// Not pending — still waiting on the schedule.
	pending, err := inspector.ListPendingTasks("normal")
	require.NoError(t, err)
	require.Empty(t, pending, "scheduled task should not be pending yet")

	// Lives in the scheduled set.
	scheduled, err := inspector.ListScheduledTasks("normal")
	require.NoError(t, err)
	require.Len(t, scheduled, 1)
	require.Equal(t, asynqadapter.TypeProcessNotification, scheduled[0].Type)
}

// TestQueue_Cancel_PendingTask: a pending task is removed by Cancel and the
// queue ends up empty. No error.
func TestQueue_Cancel_PendingTask(t *testing.T) {
	redisOpt, cleanup := setupRedisForAsynq(t)
	defer cleanup()

	q := asynqadapter.NewQueue(redisOpt)
	defer func() { _ = q.Close() }()

	inspector := hibikenasynq.NewInspector(redisOpt)
	defer func() { _ = inspector.Close() }()
	awaitInspector(t, inspector)

	ctx := context.Background()
	notifID := domain.NotificationID("01940000-0000-7000-8000-000000000041")

	require.NoError(t, q.Enqueue(ctx, notifID, domain.PriorityNormal, ""))

	infos, err := inspector.ListPendingTasks("normal")
	require.NoError(t, err)
	require.Len(t, infos, 1)

	require.NoError(t, q.Cancel(ctx, notifID))

	infos, err = inspector.ListPendingTasks("normal")
	require.NoError(t, err)
	require.Empty(t, infos, "cancel must remove the pending task")
}

// TestQueue_Cancel_ScheduledTask: a scheduled (future) task is also removed.
func TestQueue_Cancel_ScheduledTask(t *testing.T) {
	redisOpt, cleanup := setupRedisForAsynq(t)
	defer cleanup()

	q := asynqadapter.NewQueue(redisOpt)
	defer func() { _ = q.Close() }()

	inspector := hibikenasynq.NewInspector(redisOpt)
	defer func() { _ = inspector.Close() }()
	awaitInspector(t, inspector)

	ctx := context.Background()
	notifID := domain.NotificationID("01940000-0000-7000-8000-000000000042")

	require.NoError(t, q.EnqueueScheduled(ctx, notifID, domain.PriorityNormal, time.Now().Add(time.Hour)))

	scheduled, err := inspector.ListScheduledTasks("normal")
	require.NoError(t, err)
	require.Len(t, scheduled, 1)

	require.NoError(t, q.Cancel(ctx, notifID))

	scheduled, err = inspector.ListScheduledTasks("normal")
	require.NoError(t, err)
	require.Empty(t, scheduled, "cancel must remove the scheduled task")
}

// TestQueue_Cancel_UnknownNotification: canceling a non-existent task is a
// no-op — no error, nothing to clean up.
func TestQueue_Cancel_UnknownNotification(t *testing.T) {
	redisOpt, cleanup := setupRedisForAsynq(t)
	defer cleanup()

	q := asynqadapter.NewQueue(redisOpt)
	defer func() { _ = q.Close() }()

	inspector := hibikenasynq.NewInspector(redisOpt)
	defer func() { _ = inspector.Close() }()
	awaitInspector(t, inspector)

	err := q.Cancel(context.Background(), domain.NotificationID("01940000-0000-7000-8000-000000000043"))
	require.NoError(t, err, "missing task must not surface as an error (best-effort cancel)")
}

// TestQueue_Enqueue_IdempotencyKeyDeduplicates: enqueueing the same task id
// twice within the uniqueness window must reject the duplicate (CLAUDE.md
// §3.9 second layer).
func TestQueue_Enqueue_IdempotencyKeyDeduplicates(t *testing.T) {
	redisOpt, cleanup := setupRedisForAsynq(t)
	defer cleanup()

	q := asynqadapter.NewQueue(redisOpt)
	defer func() { _ = q.Close() }()

	inspector := hibikenasynq.NewInspector(redisOpt)
	defer func() { _ = inspector.Close() }()
	awaitInspector(t, inspector)

	ctx := context.Background()
	notifID := domain.NotificationID("01940000-0000-7000-8000-000000000021")

	require.NoError(t, q.Enqueue(ctx, notifID, domain.PriorityNormal, "shared-key"))
	err := q.Enqueue(ctx, notifID, domain.PriorityNormal, "shared-key")
	require.Error(t, err, "duplicate task id within uniqueness window must reject")

	infos, err := inspector.ListPendingTasks("normal")
	require.NoError(t, err)
	require.Len(t, infos, 1, "only the first enqueue should be persisted")
}
