//go:build integration

package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	redisadapter "github.com/afbora/event-driven-notification/internal/adapters/redis"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// TestIdempotencyStore_SetAndGet: full round-trip — Set stores body,
// content type, and the request hash; Get returns them all with
// found=true.
func TestIdempotencyStore_SetAndGet(t *testing.T) {
	client, cleanup := setupRedis(t)
	defer cleanup()

	store := redisadapter.NewIdempotencyStore(client)
	ctx := context.Background()

	entry := ports.IdempotencyEntry{
		Body:        []byte(`{"id":"01940000-0000-7000-8000-000000000001","status":"accepted"}`),
		ContentType: "application/json",
		RequestHash: []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
	}
	require.NoError(t, store.Set(ctx, "key-001", entry, 1*time.Minute))

	got, found, err := store.Get(ctx, "key-001")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, entry.Body, got.Body)
	require.Equal(t, entry.ContentType, got.ContentType)
	require.Equal(t, entry.RequestHash, got.RequestHash,
		"request hash must survive the JSON round trip so the HTTP layer can detect key/body conflicts")
}

// TestIdempotencyStore_Get_NotFound: unknown key returns found=false and
// no error — the HTTP layer treats this as "no cached response, proceed".
func TestIdempotencyStore_Get_NotFound(t *testing.T) {
	client, cleanup := setupRedis(t)
	defer cleanup()

	store := redisadapter.NewIdempotencyStore(client)

	got, found, err := store.Get(context.Background(), "unknown-key")
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, got.Body)
	require.Empty(t, got.ContentType)
	require.Nil(t, got.RequestHash)
}

// TestIdempotencyStore_TTLExpires: a tight TTL means the entry vanishes
// once enough time has passed. Confirms Set actually respects the TTL.
func TestIdempotencyStore_TTLExpires(t *testing.T) {
	client, cleanup := setupRedis(t)
	defer cleanup()

	store := redisadapter.NewIdempotencyStore(client)
	ctx := context.Background()

	require.NoError(t, store.Set(ctx, "ttl-key",
		ports.IdempotencyEntry{Body: []byte("x"), ContentType: "text/plain"},
		500*time.Millisecond))

	// Right after Set — still there.
	_, found, err := store.Get(ctx, "ttl-key")
	require.NoError(t, err)
	require.True(t, found)

	// After TTL — gone.
	time.Sleep(1 * time.Second)
	_, found, err = store.Get(ctx, "ttl-key")
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

	require.NoError(t, store.Set(ctx, "first",
		ports.IdempotencyEntry{Body: []byte("alpha"), ContentType: "text/plain"},
		time.Minute))
	require.NoError(t, store.Set(ctx, "second",
		ports.IdempotencyEntry{Body: []byte("beta"), ContentType: "text/plain"},
		time.Minute))

	got, _, err := store.Get(ctx, "first")
	require.NoError(t, err)
	require.Equal(t, []byte("alpha"), got.Body)

	got, _, err = store.Get(ctx, "second")
	require.NoError(t, err)
	require.Equal(t, []byte("beta"), got.Body)
}

// TestIdempotencyStore_LegacyEntryWithoutHash: an entry persisted by an
// older deployment carries no RequestHash. The store must round-trip
// it as an empty hash so the HTTP layer can detect the legacy shape
// and fall back to the pre-fingerprint replay behavior — the upgrade
// path (CLAUDE.md §3.9) does not break in-flight cache entries.
func TestIdempotencyStore_LegacyEntryWithoutHash(t *testing.T) {
	client, cleanup := setupRedis(t)
	defer cleanup()

	store := redisadapter.NewIdempotencyStore(client)
	ctx := context.Background()

	legacy := ports.IdempotencyEntry{
		Body:        []byte(`{"id":"legacy"}`),
		ContentType: "application/json",
		// RequestHash deliberately nil.
	}
	require.NoError(t, store.Set(ctx, "legacy-key", legacy, time.Minute))

	got, found, err := store.Get(ctx, "legacy-key")
	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, got.RequestHash,
		"legacy entries (no request hash) must round-trip as empty so the HTTP layer recognizes them")
	require.Equal(t, legacy.Body, got.Body)
}
