// Package redis is the redis adapter — concrete implementations of the
// idempotency, rate-limiter, and status-broadcaster ports declared in
// internal/ports/. The package depends on github.com/redis/go-redis/v9
// (aliased to goredis to avoid the package-name clash with this package).
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// idempotencyKeyPrefix namespaces every key written by this store so it
// never collides with the rate-limiter or asynq's own keys.
const idempotencyKeyPrefix = "idempotency:"

// idempotencyValue is the on-wire shape stored at each key. JSON because
// the field set is small and the encoding cost is negligible compared to
// the network round trip; binary body is base64-encoded by the JSON encoder.
type idempotencyValue struct {
	Body        []byte `json:"body"`
	ContentType string `json:"content_type"`
}

// IdempotencyStore is the Redis-backed implementation of
// ports.IdempotencyStore. Each entry is a single key carrying the cached
// response body and Content-Type, written with an expiration so the HTTP
// layer's idempotency window (CLAUDE.md §3.9, default 24 h) is enforced
// by Redis itself.
type IdempotencyStore struct {
	client *goredis.Client
}

// NewIdempotencyStore wires a go-redis client into the store.
func NewIdempotencyStore(client *goredis.Client) *IdempotencyStore {
	return &IdempotencyStore{client: client}
}

// Set persists the response for the given key with the supplied TTL.
func (s *IdempotencyStore) Set(ctx context.Context, key string, body []byte, contentType string, ttl time.Duration) error {
	encoded, err := json.Marshal(idempotencyValue{Body: body, ContentType: contentType})
	if err != nil {
		return fmt.Errorf("marshal idempotency value: %w", err)
	}
	if err := s.client.Set(ctx, idempotencyKeyPrefix+key, encoded, ttl).Err(); err != nil {
		return fmt.Errorf("redis set %s: %w", key, err)
	}
	return nil
}

// Get returns the cached body and Content-Type, or found=false when no
// entry exists. A missing key is not an error — the caller proceeds with
// the normal request path.
func (s *IdempotencyStore) Get(ctx context.Context, key string) ([]byte, string, bool, error) {
	raw, err := s.client.Get(ctx, idempotencyKeyPrefix+key).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, "", false, nil
		}
		return nil, "", false, fmt.Errorf("redis get %s: %w", key, err)
	}
	var val idempotencyValue
	if err := json.Unmarshal(raw, &val); err != nil {
		return nil, "", false, fmt.Errorf("unmarshal idempotency value for %s: %w", key, err)
	}
	return val.Body, val.ContentType, true, nil
}
