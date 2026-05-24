//go:build integration

// Package asynq_test holds integration-tagged tests for the queue adapter.
// Each test spins up a fresh Redis 7 container and exercises the adapter
// against it. Tasks land in real asynq queues; assertions read them back
// via asynq.Inspector.
//
// Run with:
//
//	go test -tags=integration ./internal/adapters/asynq/...
package asynq_test

import (
	"context"
	"testing"
	"time"

	hibikenasynq "github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/redis"
)

// setupRedisForAsynq spins up a fresh Redis 7 container and returns an
// asynq RedisConnOpt pointing at it, plus a cleanup function.
func setupRedisForAsynq(t *testing.T) (hibikenasynq.RedisConnOpt, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := redis.Run(ctx, "redis:7-alpine")
	require.NoError(t, err, "start redis container")

	endpoint, err := container.Endpoint(ctx, "")
	require.NoError(t, err, "container endpoint")

	opt := hibikenasynq.RedisClientOpt{Addr: endpoint}

	cleanup := func() {
		if err := container.Terminate(ctx); err != nil {
			t.Logf("terminate redis container: %v", err)
		}
	}
	return opt, cleanup
}

// awaitInspector blocks briefly until the inspector can list tasks, so a
// fresh container has finished initializing asynq's internal keyspace.
func awaitInspector(t *testing.T, inspector *hibikenasynq.Inspector) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err := inspector.Queues()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("inspector never became ready: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
