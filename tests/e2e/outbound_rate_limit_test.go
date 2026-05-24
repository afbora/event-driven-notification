//go:build e2e

package e2e_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestOutboundRateLimit_BurstThrottledToCap: with the worker's
// per-channel cap set to 2 messages per 60-second window, posting a
// burst of 5 SMS notifications results in exactly 2 provider calls.
// The other 3 land in `retrying` because the worker's
// rescheduleForRateLimit path runs (CLAUDE.md §2.6 / §3.x). This is
// the load-bearing claim that outbound throttling protects the
// downstream provider from being slammed.
func TestOutboundRateLimit_BurstThrottledToCap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t, WithOutboundRateLimit(2, time.Minute))

	const burst = 5

	// Fire the burst concurrently so the worker sees them packed
	// tightly enough for the limiter to engage. Serial requests would
	// also work given the 60s window, but the parallel shape mirrors
	// a real flash-sale scenario.
	ids := make([]string, burst)
	var wg sync.WaitGroup
	wg.Add(burst)
	for i := 0; i < burst; i++ {
		go func(i int) {
			defer wg.Done()
			ids[i] = createNotification(ctx, t, h.BaseURL, "+1555555010"+string(rune('0'+i)))
		}(i)
	}
	wg.Wait()

	// Give the worker time to dequeue all 5 tasks. The exact moment
	// "all dequeued" is observable through provider calls + status
	// transitions — we eventual-poll until the total of (delivered +
	// retrying) reaches burst, then check the split.
	require.Eventually(t, func() bool {
		delivered, retrying := countStatuses(ctx, t, h.BaseURL, ids)
		return delivered+retrying == burst
	}, 30*time.Second, 200*time.Millisecond,
		"worker did not finish processing the burst within the budget")

	delivered, retrying := countStatuses(ctx, t, h.BaseURL, ids)
	require.Equal(t, 2, delivered,
		"cap=2: exactly the first 2 calls must reach the provider; got delivered=%d retrying=%d",
		delivered, retrying)
	require.Equal(t, 3, retrying,
		"cap=2: the other 3 must be rescheduled as retrying; got delivered=%d retrying=%d",
		delivered, retrying)
	require.Len(t, h.Provider.Calls(), 2,
		"the outbound limiter must prevent more than cap provider calls in the window")
}

// countStatuses inspects each notification individually via the
// public GET endpoint and returns the delivered/retrying split. Read
// errors are silently treated as "not yet known" so the eventual
// poller can keep going.
func countStatuses(ctx context.Context, t *testing.T, baseURL string, ids []string) (delivered, retrying int) {
	t.Helper()
	for _, id := range ids {
		switch fetchStatus(ctx, t, baseURL, id) {
		case "delivered":
			delivered++
		case "retrying":
			retrying++
		}
	}
	return delivered, retrying
}
