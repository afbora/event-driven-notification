//go:build integration

package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	redisadapter "github.com/afbora/event-driven-notification/internal/adapters/redis"
)

// TestOutboundRateLimiter_AllowUnderLimit: every request inside the limit
// returns allowed=true with zero retryAfter.
func TestOutboundRateLimiter_AllowUnderLimit(t *testing.T) {
	client, cleanup := setupRedis(t)
	defer cleanup()

	// 3 requests per 500ms — generous so we never hit the limit here.
	limiter := redisadapter.NewOutboundRateLimiter(client, 3, 500*time.Millisecond)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		allowed, retryAfter, err := limiter.Allow(ctx, "channel:sms")
		require.NoError(t, err)
		require.True(t, allowed, "request %d under limit must be allowed", i+1)
		require.Zero(t, retryAfter)
	}
}

// TestOutboundRateLimiter_RejectAtLimit: the (limit+1)-th request inside
// the same window is rejected and reports a non-zero retryAfter so the
// caller can re-queue with the right delay.
func TestOutboundRateLimiter_RejectAtLimit(t *testing.T) {
	client, cleanup := setupRedis(t)
	defer cleanup()

	limiter := redisadapter.NewOutboundRateLimiter(client, 3, 500*time.Millisecond)
	ctx := context.Background()

	// Burn through the allowance.
	for i := 0; i < 3; i++ {
		allowed, _, err := limiter.Allow(ctx, "channel:sms")
		require.NoError(t, err)
		require.True(t, allowed)
	}

	// The 4th call in the same window must be rejected.
	allowed, retryAfter, err := limiter.Allow(ctx, "channel:sms")
	require.NoError(t, err)
	require.False(t, allowed, "request beyond limit must be rejected")
	require.Greater(t, retryAfter, time.Duration(0), "retryAfter must point at the next window")
	require.LessOrEqual(t, retryAfter, 500*time.Millisecond, "retryAfter must fit within the window")
}

// TestOutboundRateLimiter_BucketIsolation: separate buckets keep their own
// counts so a saturated SMS channel does not block email.
func TestOutboundRateLimiter_BucketIsolation(t *testing.T) {
	client, cleanup := setupRedis(t)
	defer cleanup()

	limiter := redisadapter.NewOutboundRateLimiter(client, 2, 500*time.Millisecond)
	ctx := context.Background()

	// Saturate SMS.
	for i := 0; i < 2; i++ {
		allowed, _, err := limiter.Allow(ctx, "channel:sms")
		require.NoError(t, err)
		require.True(t, allowed)
	}
	// SMS now rejects.
	allowed, _, err := limiter.Allow(ctx, "channel:sms")
	require.NoError(t, err)
	require.False(t, allowed)

	// Email is untouched — still has full allowance.
	for i := 0; i < 2; i++ {
		allowed, _, err := limiter.Allow(ctx, "channel:email")
		require.NoError(t, err)
		require.True(t, allowed, "email request %d must be allowed despite saturated sms", i+1)
	}
}

// TestOutboundRateLimiter_WindowExpiry: once the window passes, the counter
// resets and the next call is allowed again.
func TestOutboundRateLimiter_WindowExpiry(t *testing.T) {
	client, cleanup := setupRedis(t)
	defer cleanup()

	limiter := redisadapter.NewOutboundRateLimiter(client, 2, 500*time.Millisecond)
	ctx := context.Background()

	// Saturate.
	for i := 0; i < 2; i++ {
		allowed, _, err := limiter.Allow(ctx, "channel:sms")
		require.NoError(t, err)
		require.True(t, allowed)
	}
	allowed, _, err := limiter.Allow(ctx, "channel:sms")
	require.NoError(t, err)
	require.False(t, allowed)

	// Wait past the window then try again — fresh allowance.
	time.Sleep(600 * time.Millisecond)
	allowed, _, err = limiter.Allow(ctx, "channel:sms")
	require.NoError(t, err)
	require.True(t, allowed, "first request after window expiry must be allowed")
}
