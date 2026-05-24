// Package asynq is the queue adapter — the concrete implementation of
// ports.Queue (and, in cmd/worker, the asynq Server that consumes tasks).
// This file declares the on-wire task types and their JSON payload shapes.
//
// asynq tasks are dispatched as <type, payload> tuples: the type is a
// dot-separated string (e.g. "notification:process") the consumer mux
// routes by, and the payload is application-defined JSON.
package asynq

import (
	"encoding/json"
	"fmt"
	"time"

	hibikenasynq "github.com/hibiken/asynq"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// Task types. One per business operation. New types are introduced when a
// new background workload exists — never overloaded as a switch inside one
// type (CLAUDE.md anti-patterns).
const (
	// TypeProcessNotification carries one notification id for the worker
	// to claim, send through the provider, and update status on.
	TypeProcessNotification = "notification:process"
)

// Retry policy constants come from CLAUDE.md §5 / ADR-0003: 5 attempts
// before the task lands in the dead-letter queue. asynq applies its own
// exponential backoff inside that envelope.
const (
	maxRetryAttempts     = 5
	idempotencyWindow24h = 24 * time.Hour
)

// ProcessNotificationPayload is the body carried in TypeProcessNotification
// tasks. The worker reads the id, hands it to the ProcessNotification use
// case, and lets the use case do the rest. We keep the payload minimal so
// stale snapshots of priority / content cannot diverge from the database;
// the worker re-reads the row before processing.
type ProcessNotificationPayload struct {
	NotificationID string `json:"notification_id"`
}

// TaskIDFor returns the deterministic asynq task id for a notification.
// Cancel relies on this exact id to find and remove the pending task; the
// reconciler relies on it for its idempotent re-enqueue behavior. When the
// caller provides an explicit idempotency key, it takes precedence — that's
// the API consumer's own dedup story (CLAUDE.md §3.9).
func TaskIDFor(notificationID domain.NotificationID, idempotencyKey string) string {
	if idempotencyKey != "" {
		return idempotencyKey
	}
	return "notif:" + string(notificationID)
}

// NewProcessNotificationTask builds an asynq.Task plus its enqueue options
// for a notification. priority maps to the asynq queue name (asynq routes
// queue-by-name with configured weights). The task id is always set so
// Cancel can find pending tasks; idempotencyKey additionally enables the
// 24-hour uniqueness window (CLAUDE.md §3.9 second layer).
func NewProcessNotificationTask(
	notificationID domain.NotificationID,
	priority domain.Priority,
	idempotencyKey string,
) (*hibikenasynq.Task, []hibikenasynq.Option, error) {
	payload, err := json.Marshal(ProcessNotificationPayload{
		NotificationID: string(notificationID),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal process notification payload: %w", err)
	}

	opts := []hibikenasynq.Option{
		hibikenasynq.Queue(string(priority)),
		hibikenasynq.MaxRetry(maxRetryAttempts),
		hibikenasynq.TaskID(TaskIDFor(notificationID, idempotencyKey)),
	}
	if idempotencyKey != "" {
		opts = append(opts, hibikenasynq.Unique(idempotencyWindow24h))
	}

	return hibikenasynq.NewTask(TypeProcessNotification, payload), opts, nil
}
