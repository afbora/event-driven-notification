//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/adapters/provider"
)

// TestTrace_HappyPath_AllTransitionsChronological: a happy-path
// notification surfaces the full canonical sequence (created →
// queued → processing → delivered) via /trace, in strict
// chronological order. The trace endpoint is what support reaches
// for to answer "what happened to this notification?" — this test
// pins the contract that the answer is complete and ordered.
func TestTrace_HappyPath_AllTransitionsChronological(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	id := createNotification(ctx, t, h.BaseURL, "+15555550080")
	require.Eventually(t, func() bool {
		return fetchStatus(ctx, t, h.BaseURL, id) == "delivered"
	}, 30*time.Second, 200*time.Millisecond)

	trace := fetchTrace(ctx, t, h.BaseURL, id)
	require.Equal(t, id, trace.NotificationID)

	events := make([]string, len(trace.Entries))
	times := make([]time.Time, len(trace.Entries))
	for i, e := range trace.Entries {
		events[i] = e.Event
		times[i] = e.CreatedAt
	}

	require.Equal(t, []string{"created", "queued", "processing", "delivered"}, events,
		"happy path trace must be exactly the canonical four transitions, in order")

	// Manual ascending check — sort.SliceIsSorted requires a strict
	// less, and ties between adjacent entries (deterministic clock
	// inside a single use-case method) would confuse it.
	for i := 1; i < len(times); i++ {
		require.Falsef(t, times[i].Before(times[i-1]),
			"trace entry %d (%s @ %s) is older than entry %d (%s @ %s)",
			i, events[i], times[i], i-1, events[i-1], times[i-1])
	}
}

// TestTrace_TransientFailure_IncludesRetrying: after a transient
// provider failure the trace surfaces created → queued → processing
// → retrying, again in chronological order. Same ordering contract,
// different terminal step.
func TestTrace_TransientFailure_IncludesRetrying(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	h.Provider.Configure(
		provider.WithSuccessRate(0),
		provider.WithFailureMode(provider.FailureTransient),
	)

	id := createNotification(ctx, t, h.BaseURL, "+15555550081")
	require.Eventually(t, func() bool {
		return fetchStatus(ctx, t, h.BaseURL, id) == "retrying"
	}, 30*time.Second, 200*time.Millisecond)

	trace := fetchTrace(ctx, t, h.BaseURL, id)
	events := make([]string, len(trace.Entries))
	for i, e := range trace.Entries {
		events[i] = e.Event
	}
	require.Equal(t, []string{"created", "queued", "processing", "retrying"}, events)
}

// TestTrace_UnknownID_404: querying the trace endpoint for an id
// that does not exist surfaces the same 404 problem as GET notification,
// so support tooling can use one error-handling code path.
func TestTrace_UnknownID_404(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		h.BaseURL+"/api/v1/notifications/019e0000-0000-0000-0000-000000000000/trace", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "trace body=%s", body)
}

// traceResponse mirrors the wire shape just enough for the
// assertions above.
type traceResponse struct {
	NotificationID string `json:"notification_id"`
	Entries        []struct {
		Event     string    `json:"event"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"entries"`
}

func fetchTrace(ctx context.Context, t *testing.T, baseURL, id string) traceResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/api/v1/notifications/"+id+"/trace", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "trace body=%s", body)
	var out traceResponse
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}
