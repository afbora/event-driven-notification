package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// rateLimitKeyPrefix namespaces every bucket key so it never collides with
// the idempotency store or asynq's own keys.
const rateLimitKeyPrefix = "ratelimit:"

// rateLimitScript implements a fixed-window counter atomically.
// KEYS[1] = full bucket key.
// ARGV[1] = limit (int).
// ARGV[2] = window milliseconds (int).
// Returns: {allowed (0/1), retry_after_ms (int)}.
//
// On the first hit of a new window the script seeds the counter at 1 and
// sets PEXPIRE; subsequent hits just INCR. When the counter exceeds the
// limit, the script returns the remaining window (via PTTL) so the caller
// knows how long to back off. Millisecond precision matters so sub-second
// limits work as configured.
const rateLimitScript = `
local current = redis.call('INCR', KEYS[1])
if current == 1 then
    redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
local limit = tonumber(ARGV[1])
if current > limit then
    local ttl = redis.call('PTTL', KEYS[1])
    if ttl < 0 then ttl = 0 end
    return {0, ttl}
end
return {1, 0}
`

// OutboundRateLimiter is the Redis-backed implementation of
// ports.RateLimiter for the outbound limit (CLAUDE.md §2.6: 100 messages
// per second per channel, applied at the worker before each provider call).
//
// Limit and window are injected so tests can use tight values and runtime
// config can swap them.
type OutboundRateLimiter struct {
	client *goredis.Client
	limit  int
	window time.Duration
	script *goredis.Script
}

// NewOutboundRateLimiter wires the client, configures the fixed window, and
// pre-loads the Lua script for cheap repeat invocations.
func NewOutboundRateLimiter(client *goredis.Client, limit int, window time.Duration) *OutboundRateLimiter {
	return &OutboundRateLimiter{
		client: client,
		limit:  limit,
		window: window,
		script: goredis.NewScript(rateLimitScript),
	}
}

// Allow consumes one token from the bucket's window. Returns allowed=false
// with retryAfter set to the remaining window when the limit has been hit.
// retryAfter is meaningful only when allowed == false.
func (l *OutboundRateLimiter) Allow(ctx context.Context, bucket string) (bool, time.Duration, error) {
	key := rateLimitKeyPrefix + bucket
	windowMs := l.window.Milliseconds()
	if windowMs < 1 {
		windowMs = 1
	}

	res, err := l.script.Run(ctx, l.client, []string{key}, l.limit, windowMs).Result()
	if err != nil {
		return false, 0, fmt.Errorf("rate limit script %s: %w", bucket, err)
	}

	arr, ok := res.([]any)
	if !ok || len(arr) != 2 {
		return false, 0, fmt.Errorf("rate limit script returned unexpected value: %T %v", res, res)
	}

	allowedRaw, ok := arr[0].(int64)
	if !ok {
		return false, 0, fmt.Errorf("rate limit script: bad allowed field: %T", arr[0])
	}
	retryMs, ok := arr[1].(int64)
	if !ok {
		return false, 0, fmt.Errorf("rate limit script: bad retry_after field: %T", arr[1])
	}
	return allowedRaw == 1, time.Duration(retryMs) * time.Millisecond, nil
}
