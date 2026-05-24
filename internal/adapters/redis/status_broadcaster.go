package redis

import (
	"context"
	"encoding/json"
	"fmt"

	goredis "github.com/redis/go-redis/v9"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// StatusChannel is the Redis pub/sub channel that carries notification
// status updates. Exported so the Phase 4 WebSocket hub can subscribe to
// the same channel without duplicating the string.
const StatusChannel = "notification.status"

// statusMessage is the on-wire payload published to StatusChannel.
// Subscribers (the WebSocket hub) decode it and fan it out to clients
// that registered for the matching notification id.
type statusMessage struct {
	NotificationID string `json:"notification_id"`
	Status         string `json:"status"`
}

// StatusBroadcaster is the Redis-backed implementation of
// ports.StatusBroadcaster (ADR-0006). Publishing is fire-and-forget on
// the producer side — the worker calls Publish, Redis delivers to every
// subscribed API instance, and each instance fans out to its own local
// WebSocket clients. No acknowledgement, no retry: if the message is
// dropped, the next status transition (or a manual refresh) recovers.
type StatusBroadcaster struct {
	client *goredis.Client
}

// NewStatusBroadcaster wires a go-redis client into the broadcaster.
func NewStatusBroadcaster(client *goredis.Client) *StatusBroadcaster {
	return &StatusBroadcaster{client: client}
}

// Publish emits one status update for the given notification.
func (b *StatusBroadcaster) Publish(ctx context.Context, notificationID domain.NotificationID, status domain.Status) error {
	payload, err := json.Marshal(statusMessage{
		NotificationID: string(notificationID),
		Status:         string(status),
	})
	if err != nil {
		return fmt.Errorf("marshal status message: %w", err)
	}
	if err := b.client.Publish(ctx, StatusChannel, payload).Err(); err != nil {
		return fmt.Errorf("redis publish %s: %w", notificationID, err)
	}
	return nil
}
