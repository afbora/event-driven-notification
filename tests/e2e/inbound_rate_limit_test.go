//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestInboundRateLimit_Returns429AfterCap: CLAUDE.md §2.6 / §10 limit
// the API to 60 requests/minute per client IP. This test lowers the
// cap to 3 so the assertion runs in well under a second, then drives
// 5 requests from the same in-process client (all 127.0.0.1) and
// expects the 4th to flip to 429 with the documented Retry-After
// header.
func TestInboundRateLimit_Returns429AfterCap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t, WithInboundRateLimit(3))

	url := h.BaseURL + "/healthz/live"

	// Issue requests until one returns 429 — or until we exceed an
	// upper bound that proves the limiter is broken.
	var firstThrottled int
	const upperBound = 10
	for i := 1; i <= upperBound; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			firstThrottled = i
			// Retry-After must be a positive integer (seconds) so
			// well-behaved clients can back off correctly.
			retry := resp.Header.Get("Retry-After")
			require.NotEmpty(t, retry, "429 must carry a Retry-After header")
			seconds, perr := strconv.Atoi(retry)
			require.NoError(t, perr, "Retry-After must be an integer; got %q", retry)
			require.Greater(t, seconds, 0, "Retry-After must be a positive value")
			break
		}
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"under-limit request %d unexpectedly returned %d", i, resp.StatusCode)
	}

	require.NotZero(t, firstThrottled, "no request was throttled within %d attempts", upperBound)
	require.Equal(t, 4, firstThrottled,
		"with cap=3, the 4th request must be the first one throttled; got %d", firstThrottled)
}

// TestInboundRateLimit_UnderCapAllPass: a baseline check — when the
// number of requests fits inside the cap, every single one succeeds.
// Guards against a regression where the limiter accidentally
// throttles legitimate traffic.
func TestInboundRateLimit_UnderCapAllPass(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t, WithInboundRateLimit(20))

	url := h.BaseURL + "/healthz/live"
	for i := 1; i <= 10; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"request %d under cap=20 must pass; got %d", i, resp.StatusCode)
	}
}
