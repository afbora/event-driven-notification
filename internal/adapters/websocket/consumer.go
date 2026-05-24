package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	goredis "github.com/redis/go-redis/v9"

	redisadapter "github.com/afbora/event-driven-notification/internal/adapters/redis"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// Consumer subscribes to the Redis pub/sub status channel and forwards
// every decoded message to a Hub. One Consumer runs per API instance
// (cmd/api wires it up); when the worker publishes a status update, every
// instance's Consumer hands it to its local Hub, which fans it out to its
// own WebSocket clients (ADR-0006 horizontal-scaling story).
type Consumer struct {
	client *goredis.Client
	hub    *Hub
	ready  atomic.Bool
}

// NewConsumer wires a go-redis client and a Hub.
func NewConsumer(client *goredis.Client, hub *Hub) *Consumer {
	return &Consumer{client: client, hub: hub}
}

// IsReady reports whether the subscription to Redis has been confirmed.
// Useful in tests to wait for the consumer to be live before publishing —
// pub/sub is fire-and-forget, so a publish that races a subscribe is lost.
func (c *Consumer) IsReady() bool {
	return c.ready.Load()
}

// Run subscribes to the StatusChannel and forwards every decoded message
// to the Hub until ctx is cancelled. Returns ctx.Err() on clean exit.
// Malformed messages are dropped silently — a single bad payload must not
// take down the consumer for a whole API instance.
func (c *Consumer) Run(ctx context.Context) error {
	sub := c.client.Subscribe(ctx, redisadapter.StatusChannel)
	defer func() { _ = sub.Close() }()

	// Wait for Redis to confirm the subscription before flipping the ready
	// flag — Receive blocks until the SUBSCRIBE reply lands.
	if _, err := sub.Receive(ctx); err != nil {
		return fmt.Errorf("subscribe confirmation: %w", err)
	}
	c.ready.Store(true)

	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return fmt.Errorf("redis pub/sub channel closed unexpectedly")
			}
			c.dispatch(msg.Payload)
		}
	}
}

// dispatch decodes the on-wire JSON and forwards it to the Hub. Decode
// failures are swallowed; we deliberately do not surface them so a single
// bad message cannot crash the consumer loop. Phase 6 metrics will count
// these as `notifications_broadcast_decode_errors_total`.
func (c *Consumer) dispatch(payload string) {
	var msg StatusUpdate
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		return
	}
	if msg.NotificationID == "" || msg.Status == "" {
		return
	}
	c.hub.Broadcast(
		domain.NotificationID(msg.NotificationID),
		domain.Status(msg.Status),
	)
}
