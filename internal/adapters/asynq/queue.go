package asynq

import (
	"context"
	"fmt"

	hibikenasynq "github.com/hibiken/asynq"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// Queue is the asynq-backed implementation of ports.Queue. EnqueueScheduled
// and Cancel land in subsequent tasks (PLAN.md task 26, 27); for now Enqueue
// covers the immediate-delivery path.
//
// asynq's NewClient is goroutine-safe and reuses one Redis connection pool
// internally; one Queue per process is the expected shape.
type Queue struct {
	client *hibikenasynq.Client
}

// NewQueue wires an asynq RedisConnOpt (the same shape cmd/worker uses) into
// a queue producer.
func NewQueue(redisOpt hibikenasynq.RedisConnOpt) *Queue {
	return &Queue{client: hibikenasynq.NewClient(redisOpt)}
}

// Close releases the underlying Redis connection pool. Callers typically
// defer it for the lifetime of the process.
func (q *Queue) Close() error {
	return q.client.Close()
}

// Enqueue schedules a notification for immediate processing on its
// priority queue. When idempotencyKey is non-empty it doubles as the
// asynq task id, so duplicate enqueues of the same logical work within
// the 24-hour uniqueness window are rejected at the queue layer
// (CLAUDE.md §3.9, second layer).
func (q *Queue) Enqueue(ctx context.Context, notificationID domain.NotificationID, priority domain.Priority, idempotencyKey string) error {
	task, opts, err := NewProcessNotificationTask(notificationID, priority, idempotencyKey)
	if err != nil {
		return fmt.Errorf("build task: %w", err)
	}
	if _, err := q.client.EnqueueContext(ctx, task, opts...); err != nil {
		return fmt.Errorf("enqueue notification %s: %w", notificationID, err)
	}
	return nil
}
