package application_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/application"
)

// TestRetryDelayFor_RateLimitedSentinel: ErrOutboundRateLimited maps
// to the rate-limit backoff (1s) regardless of attempt count. Pinned
// because the routing is the load-bearing distinction between the
// two retry shapes ADR-0015 documents.
func TestRetryDelayFor_RateLimitedSentinel(t *testing.T) {
	for _, attempts := range []int{1, 2, 5, 10} {
		d := application.RetryDelayFor(attempts, application.ErrOutboundRateLimited)
		require.Equal(t, 1*time.Second, d,
			"rate-limit backoff stays at 1s regardless of attempt count; got %s at attempts=%d", d, attempts)
	}
}

// TestRetryDelayFor_RateLimitedSentinelWrapped: errors.Is must walk
// the error chain — the worker wraps the sentinel with context
// (notification id, channel) before returning it, and RetryDelayFor
// must still route correctly through fmt.Errorf chains.
func TestRetryDelayFor_RateLimitedSentinelWrapped(t *testing.T) {
	wrapped := fmt.Errorf("rate limited on channel sms for 01HX: %w", application.ErrOutboundRateLimited)

	d := application.RetryDelayFor(1, wrapped)
	require.Equal(t, 1*time.Second, d,
		"wrapped sentinel must still route to the rate-limit backoff via errors.Is")
}

// TestRetryDelayFor_TransientSentinel: ErrProviderTransient maps to
// the exponential backoff schedule (30s × 2^(n-1)). Pinned at
// attempts 1..3 to lock the curve.
func TestRetryDelayFor_TransientSentinel(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{1, 30 * time.Second},
		{2, 60 * time.Second},
		{3, 120 * time.Second},
	}
	for _, tc := range cases {
		d := application.RetryDelayFor(tc.attempts, application.ErrProviderTransient)
		require.Equal(t, tc.want, d,
			"transient backoff at attempts=%d should be %s; got %s", tc.attempts, tc.want, d)
	}
}

// TestRetryDelayFor_UnknownErrorFallsThroughToExponential: any error
// that is neither sentinel — including raw infrastructure errors
// surfaced from the claim or DB calls — must default to the
// exponential schedule so asynq still retries them with sane timing
// instead of dropping the task or hammering the dependency.
func TestRetryDelayFor_UnknownErrorFallsThroughToExponential(t *testing.T) {
	infraErr := errors.New("postgres: connection refused")

	d := application.RetryDelayFor(1, infraErr)
	require.Equal(t, 30*time.Second, d,
		"non-sentinel errors fall through to the exponential default; got %s", d)
}
