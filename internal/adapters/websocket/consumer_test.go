//go:build integration

package websocket_test

import (
	"context"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	redisadapter "github.com/afbora/event-driven-notification/internal/adapters/redis"
	"github.com/afbora/event-driven-notification/internal/adapters/websocket"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// setupRedisForConsumer spins up a fresh Redis 7 container and returns a
// go-redis client plus a cleanup function. Each test owns its own
// container to keep state isolation cheap.
func setupRedisForConsumer(t *testing.T) (*goredis.Client, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := tcredis.Run(ctx, "redis:7-alpine")
	require.NoError(t, err, "start redis container")

	endpoint, err := container.Endpoint(ctx, "")
	require.NoError(t, err, "container endpoint")

	client := goredis.NewClient(&goredis.Options{Addr: endpoint})
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	require.NoError(t, client.Ping(pingCtx).Err(), "ping redis")

	cleanup := func() {
		_ = client.Close()
		_ = container.Terminate(ctx)
	}
	return client, cleanup
}

// recordingClient is the websocket.Client implementation used in this test —
// captures Send calls so we can assert the message landed.
type recordingClient struct {
	id  string
	mu  sync.Mutex
	got []websocket.StatusUpdate
}

func newRecordingClient(id string) *recordingClient {
	return &recordingClient{id: id}
}

func (c *recordingClient) ID() string { return c.id }

func (c *recordingClient) Send(msg websocket.StatusUpdate) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, msg)
	return nil
}

func (c *recordingClient) Received() []websocket.StatusUpdate {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]websocket.StatusUpdate, len(c.got))
	copy(out, c.got)
	return out
}

// TestConsumer_ForwardsToHub: a status message published on the Redis
// pub/sub channel is decoded by the Consumer and delivered to the matching
// subscribed Hub client.
func TestConsumer_ForwardsToHub(t *testing.T) {
	client, cleanup := setupRedisForConsumer(t)
	defer cleanup()

	hub := websocket.NewHub()
	wsClient := newRecordingClient("c1")
	notifID := domain.NotificationID("01940000-0000-7000-8000-000000000001")
	hub.Subscribe(wsClient, notifID)

	consumer := websocket.NewConsumer(client, hub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run the consumer in the background until the test cancels it.
	done := make(chan struct{})
	go func() {
		_ = consumer.Run(ctx)
		close(done)
	}()

	// Allow the subscription to register.
	require.Eventually(t, func() bool {
		return consumer.IsReady()
	}, 2*time.Second, 25*time.Millisecond, "consumer subscription never became ready")

	// Publish via the broadcaster — the very component the consumer is
	// supposed to consume from in production.
	broadcaster := redisadapter.NewStatusBroadcaster(client)
	require.NoError(t, broadcaster.Publish(ctx, notifID, domain.StatusDelivered))

	require.Eventually(t, func() bool {
		return len(wsClient.Received()) == 1
	}, 2*time.Second, 25*time.Millisecond, "client never received the broadcast")

	got := wsClient.Received()[0]
	require.Equal(t, string(notifID), got.NotificationID)
	require.Equal(t, string(domain.StatusDelivered), got.Status)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumer did not exit after context cancellation")
	}
}

// TestConsumer_ContextCancellationExits: cancelling the context returns
// Run cleanly with the canceled-context error.
func TestConsumer_ContextCancellationExits(t *testing.T) {
	client, cleanup := setupRedisForConsumer(t)
	defer cleanup()

	hub := websocket.NewHub()
	consumer := websocket.NewConsumer(client, hub)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()

	require.Eventually(t, consumer.IsReady, 2*time.Second, 25*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("consumer did not exit")
	}
}

// TestConsumer_IgnoresMalformedMessage: a publish that does not look like
// the expected JSON shape is silently dropped — no panic, the consumer
// stays alive and processes the next message normally.
func TestConsumer_IgnoresMalformedMessage(t *testing.T) {
	client, cleanup := setupRedisForConsumer(t)
	defer cleanup()

	hub := websocket.NewHub()
	wsClient := newRecordingClient("c-bad")
	notifID := domain.NotificationID("01940000-0000-7000-8000-000000000002")
	hub.Subscribe(wsClient, notifID)

	consumer := websocket.NewConsumer(client, hub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = consumer.Run(ctx) }()
	require.Eventually(t, consumer.IsReady, 2*time.Second, 25*time.Millisecond)

	// Garbage payload — must not break the consumer.
	require.NoError(t, client.Publish(ctx, redisadapter.StatusChannel, "not-json").Err())

	// Then a valid one; it should still arrive.
	broadcaster := redisadapter.NewStatusBroadcaster(client)
	require.NoError(t, broadcaster.Publish(ctx, notifID, domain.StatusFailed))

	require.Eventually(t, func() bool {
		return len(wsClient.Received()) == 1
	}, 2*time.Second, 25*time.Millisecond)
}
