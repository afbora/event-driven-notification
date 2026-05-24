//go:build integration

// Package redis_test holds integration-tagged tests for the redis adapter.
// Each test spins up a fresh Redis 7 container via testcontainers-go.
//
// Run with:
//
//	go test -tags=integration ./internal/adapters/redis/...
package redis_test

import (
	"context"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/redis"
)

// setupRedis spins up a fresh Redis 7 container, returns a go-redis client
// plus a cleanup function. Each test owns its own container so cross-test
// state pollution is impossible.
func setupRedis(t *testing.T) (*goredis.Client, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := redis.Run(ctx, "redis:7-alpine")
	require.NoError(t, err, "start redis container")

	endpoint, err := container.Endpoint(ctx, "")
	require.NoError(t, err, "container endpoint")

	client := goredis.NewClient(&goredis.Options{
		Addr: endpoint,
	})

	// Make sure the client can actually round-trip before handing it back.
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	require.NoError(t, client.Ping(pingCtx).Err(), "ping redis")

	cleanup := func() {
		if err := client.Close(); err != nil {
			t.Logf("close redis client: %v", err)
		}
		if err := container.Terminate(ctx); err != nil {
			t.Logf("terminate redis container: %v", err)
		}
	}
	return client, cleanup
}

// TestRedisScaffolding is a smoke test for the scaffolding itself — proves
// the container starts, the client connects, and basic SET/GET round-trip.
// Failures in later adapter tests can then be attributed to the adapter,
// not the scaffold.
func TestRedisScaffolding(t *testing.T) {
	client, cleanup := setupRedis(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, client.Set(ctx, "ping", "pong", 0).Err())

	got, err := client.Get(ctx, "ping").Result()
	require.NoError(t, err)
	require.Equal(t, "pong", got)
}
