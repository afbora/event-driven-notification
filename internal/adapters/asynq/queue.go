package asynq

import (
	"context"
	"errors"
	"fmt"
	"time"

	hibikenasynq "github.com/hibiken/asynq"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// tracerName is the otel.Tracer key the queue uses. Production sets
// the global provider via internal/infrastructure/tracing.Setup; the
// no-op default makes these spans free when tracing is off.
const tracerName = "github.com/afbora/event-driven-notification/internal/adapters/asynq"

// queueNames is the fixed set of priority queues the adapter targets.
// Cancel scans each one because the caller does not know which priority
// the original enqueue used.
var queueNames = []string{
	string(domain.PriorityHigh),
	string(domain.PriorityNormal),
	string(domain.PriorityLow),
}

// Queue is the asynq-backed implementation of ports.Queue. Holds both an
// asynq Client (producer side) and an Inspector (control plane, used by
// Cancel). asynq's NewClient is goroutine-safe; one Queue per process is
// the expected shape.
type Queue struct {
	client    *hibikenasynq.Client
	inspector *hibikenasynq.Inspector
}

// NewQueue wires an asynq RedisConnOpt (the same shape cmd/worker uses) into
// a queue producer plus an inspector.
func NewQueue(redisOpt hibikenasynq.RedisConnOpt) *Queue {
	return &Queue{
		client:    hibikenasynq.NewClient(redisOpt),
		inspector: hibikenasynq.NewInspector(redisOpt),
	}
}

// Close releases the underlying Redis connection pools. Callers typically
// defer it for the lifetime of the process.
func (q *Queue) Close() error {
	clientErr := q.client.Close()
	inspectorErr := q.inspector.Close()
	if clientErr != nil {
		return clientErr
	}
	return inspectorErr
}

// Enqueue schedules a notification for immediate processing on its
// priority queue. When idempotencyKey is non-empty it doubles as the
// asynq task id, so duplicate enqueues of the same logical work within
// the 24-hour uniqueness window are rejected at the queue layer
// (CLAUDE.md §3.9, second layer).
func (q *Queue) Enqueue(ctx context.Context, notificationID domain.NotificationID, priority domain.Priority, idempotencyKey string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "queue.enqueue",
		trace.WithAttributes(
			attributeNotificationID(notificationID),
			attributePriority(priority),
		),
	)
	defer span.End()

	task, opts, err := NewProcessNotificationTask(notificationID, priority, idempotencyKey)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("build task: %w", err)
	}
	if _, err := q.client.EnqueueContext(ctx, task, opts...); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("enqueue notification %s: %w", notificationID, err)
	}
	return nil
}

// EnqueueScheduled defers a notification until `at`. asynq holds the task
// in its scheduled set and only moves it into the priority queue once the
// time arrives. Idempotency keys are not used here because scheduled tasks
// are typically created by a deliberate API call rather than a retry — the
// caller is in charge of de-duplication.
func (q *Queue) EnqueueScheduled(ctx context.Context, notificationID domain.NotificationID, priority domain.Priority, at time.Time) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "queue.enqueue.scheduled",
		trace.WithAttributes(
			attributeNotificationID(notificationID),
			attributePriority(priority),
			attribute.String("notification.scheduled_at", at.Format(time.RFC3339)),
		),
	)
	defer span.End()

	task, opts, err := NewProcessNotificationTask(notificationID, priority, "")
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("build scheduled task: %w", err)
	}
	opts = append(opts, hibikenasynq.ProcessAt(at))
	if _, err := q.client.EnqueueContext(ctx, task, opts...); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("enqueue scheduled notification %s: %w", notificationID, err)
	}
	return nil
}

// attributeNotificationID is a small helper so every span across the
// asynq adapter uses the same attribute key for the notification id.
func attributeNotificationID(id domain.NotificationID) attribute.KeyValue {
	return attribute.String("notification.id", string(id))
}

// attributePriority is the matching helper for the priority label.
func attributePriority(p domain.Priority) attribute.KeyValue {
	return attribute.String("notification.priority", string(p))
}

// QueueDepths returns the number of pending (waiting) tasks in each priority
// queue. "Pending" is exactly the backlog the HighQueueDepth alert watches —
// tasks enqueued but not yet picked up by a worker. Scheduled (future) tasks
// live in asynq's scheduled set and are deliberately excluded, so a
// legitimately deferred notification never inflates the gauge and trips a
// false alert. A queue asynq has never seen yet returns ErrQueueNotFound,
// which we treat as depth 0 (no backlog) rather than an error.
func (q *Queue) QueueDepths(_ context.Context) (map[string]int, error) {
	depths := make(map[string]int, len(queueNames))
	for _, name := range queueNames {
		info, err := q.inspector.GetQueueInfo(name)
		if err != nil {
			if errors.Is(err, hibikenasynq.ErrQueueNotFound) {
				depths[name] = 0
				continue
			}
			return nil, fmt.Errorf("queue info %s: %w", name, err)
		}
		depths[name] = info.Pending
	}
	return depths, nil
}

// Cancel removes any pending or scheduled task for the notification. This
// is best-effort by design (port docs): a task that has already been picked
// up by a worker is not retracted — the worker checks status before sending
// (atomic claim, ADR-0009). The caller does not know which priority queue
// the task is in, so Cancel attempts each one and treats "not found" as
// success.
func (q *Queue) Cancel(_ context.Context, notificationID domain.NotificationID) error {
	taskID := TaskIDFor(notificationID, "")
	for _, name := range queueNames {
		err := q.inspector.DeleteTask(name, taskID)
		if err == nil || errors.Is(err, hibikenasynq.ErrTaskNotFound) || errors.Is(err, hibikenasynq.ErrQueueNotFound) {
			continue
		}
		return fmt.Errorf("cancel notification %s on queue %s: %w", notificationID, name, err)
	}
	return nil
}
