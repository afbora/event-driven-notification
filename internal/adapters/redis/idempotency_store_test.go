//go:build integration

package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	redisadapter "github.com/afbora/event-driven-notification/internal/adapters/redis"
)

// TestIdempotencyStore_SetAndGet: full round-trip — Set stores body and
// content type, Get returns them both with found=true.
func TestIdempotencyStore_SetAndGet(t *testing.T) {
	client, cleanup := setupRedis(t)
	defer cleanup()

	store := redisadapter.NewIdempotencyStore(client)
	ctx := context.Background()

	body := []byte(`{"id":"01940000-0000-7000-8000-000000000001","status":"accepted"}`)
	require.NoError(t, store.Set(ctx, "key-001", body, "application/json", 1*time.Minute))

	gotBody, gotCT, found, err := store.Get(ctx, "key-001")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, body, gotBody)
	require.Equal(t, "application/json", gotCT)
}

// TestIdempotencyStore_Get_NotFound: unknown key returns found=false and
// no error — the HTTP layer treats this as "no cached response, proceed".
func TestIdempotencyStore_Get_NotFound(t *testing.T) {
	client, cleanup := setupRedis(t)
	defer cleanup()

	store := redisadapter.NewIdempotencyStore(client)

	body, ct, found, err := store.Get(context.Background(), "unknown-key")
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, body)
	require.Empty(t, ct)
}

// TestIdempotencyStore_TTLExpires: a tight TTL means the entry vanishes
// once enough time has passed. Confirms Set actually respects the TTL.
func TestIdempotencyStore_TTLExpires(t *testing.T) {
	client, cleanup := setupRedis(t)
	defer cleanup()

	store := redisadapter.NewIdempotencyStore(client)
	ctx := context.Background()

	require.NoError(t, store.Set(ctx, "ttl-key", []byte("x"), "text/plain", 500*time.Millisecond))

	// Right after Set — still there.
	_, _, found, err := store.Get(ctx, "ttl-key")
	require.NoError(t, err)
	require.True(t, found)

	// After TTL — gone.
	time.Sleep(1 * time.Second)
	_, _, found, err = store.Get(ctx, "ttl-key")
	require.NoError(t, err)
	require.False(t, found, "entry should expire after TTL")
}

// TestIdempotencyStore_KeysIsolated: setting one key does not leak into
// another. Cheap regression guard for any future keyspace mixup.
func TestIdempotencyStore_KeysIsolated(t *testing.T) {
	client, cleanup := setupRedis(t)
	defer cleanup()

	store := redisadapter.NewIdempotencyStore(client)
	ctx := context.Background()

	require.NoError(t, store.Set(ctx, "first", []byte("alpha"), "text/plain", time.Minute))
	require.NoError(t, store.Set(ctx, "second", []byte("beta"), "text/plain", time.Minute))

	body, _, _, err := store.Get(ctx, "first")
	require.NoError(t, err)
	require.Equal(t, []byte("alpha"), body)

	body, _, _, err = store.Get(ctx, "second")
	require.NoError(t, err)
	require.Equal(t, []byte("beta"), body)
}
