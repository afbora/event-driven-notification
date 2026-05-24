//go:build integration

package redis_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	redisadapter "github.com/afbora/event-driven-notification/internal/adapters/redis"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// awaitSubscription blocks until Redis confirms the subscription so the
// publish that follows can't race the subscriber being ready (pub/sub is
// fire-and-forget — a missed subscribe means a dropped message).
func awaitSubscription(t *testing.T, sub *goredis.PubSub) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := sub.Receive(ctx)
	require.NoError(t, err, "subscribe confirmation")
}

// TestStatusBroadcaster_PublishRoundTrip: one publish, one receive, payload
// shape verified (notification_id + status).
func TestStatusBroadcaster_PublishRoundTrip(t *testing.T) {
	client, cleanup := setupRedis(t)
	defer cleanup()

	broadcaster := redisadapter.NewStatusBroadcaster(client)
	ctx := context.Background()

	sub := client.Subscribe(ctx, redisadapter.StatusChannel)
	defer func() { _ = sub.Close() }()
	awaitSubscription(t, sub)

	notifID := domain.NotificationID("01940000-0000-7000-8000-000000000001")
	require.NoError(t, broadcaster.Publish(ctx, notifID, domain.StatusDelivered))

	recvCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	msg, err := sub.ReceiveMessage(recvCtx)
	require.NoError(t, err)

	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(msg.Payload), &payload))
	require.Equal(t, string(notifID), payload["notification_id"])
	require.Equal(t, string(domain.StatusDelivered), payload["status"])
}

// TestStatusBroadcaster_MultipleMessages: 3 publishes → 3 messages in order.
func TestStatusBroadcaster_MultipleMessages(t *testing.T) {
	client, cleanup := setupRedis(t)
	defer cleanup()

	broadcaster := redisadapter.NewStatusBroadcaster(client)
	ctx := context.Background()

	sub := client.Subscribe(ctx, redisadapter.StatusChannel)
	defer func() { _ = sub.Close() }()
	awaitSubscription(t, sub)

	statuses := []domain.Status{
		domain.StatusQueued,
		domain.StatusProcessing,
		domain.StatusDelivered,
	}
	notifID := domain.NotificationID("01940000-0000-7000-8000-000000000002")

	for _, s := range statuses {
		require.NoError(t, broadcaster.Publish(ctx, notifID, s))
	}

	recvCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for i, want := range statuses {
		msg, err := sub.ReceiveMessage(recvCtx)
		require.NoError(t, err)
		var payload map[string]string
		require.NoError(t, json.Unmarshal([]byte(msg.Payload), &payload))
		require.Equalf(t, string(want), payload["status"], "message %d", i)
	}
}

// TestStatusBroadcaster_MultipleSubscribers: each subscriber receives the
// same message (fan-out). Phase 4 horizontal WebSocket scaling depends on
// this — every API instance subscribes, every one gets every status update.
func TestStatusBroadcaster_MultipleSubscribers(t *testing.T) {
	client, cleanup := setupRedis(t)
	defer cleanup()

	broadcaster := redisadapter.NewStatusBroadcaster(client)
	ctx := context.Background()

	sub1 := client.Subscribe(ctx, redisadapter.StatusChannel)
	defer func() { _ = sub1.Close() }()
	awaitSubscription(t, sub1)

	sub2 := client.Subscribe(ctx, redisadapter.StatusChannel)
	defer func() { _ = sub2.Close() }()
	awaitSubscription(t, sub2)

	notifID := domain.NotificationID("01940000-0000-7000-8000-000000000003")
	require.NoError(t, broadcaster.Publish(ctx, notifID, domain.StatusFailed))

	recvCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	for i, sub := range []*goredis.PubSub{sub1, sub2} {
		msg, err := sub.ReceiveMessage(recvCtx)
		require.NoError(t, err, "subscriber %d receive", i+1)
		var payload map[string]string
		require.NoError(t, json.Unmarshal([]byte(msg.Payload), &payload))
		require.Equalf(t, string(domain.StatusFailed), payload["status"], "subscriber %d", i+1)
	}
}
