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

// TestCancel_QueuedAllowed: a freshly-created notification is in the
// `queued` state; PATCH /cancel transitions it to `cancelled`. The
// provider's WithLatency stretches each Send call so the worker is
// guaranteed to still be mid-flight (or not yet started) when the
// cancel arrives, giving the assertion a deterministic window.
func TestCancel_QueuedAllowed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	// 3-second latency keeps the provider call in flight long enough
	// for the cancel PATCH to race with it. Mock retains success
	// semantics so a missed cancel would land delivered (and the
	// test would catch it on the final assertion).
	h.Provider.Configure(provider.WithLatency(3 * time.Second))

	id := createNotification(ctx, t, h.BaseURL, "+15555550030")

	resp := patchCancel(ctx, t, h.BaseURL, id)
	require.Equal(t, http.StatusOK, resp.status, "cancel body=%s", resp.body)

	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(resp.body, &out))
	require.Equal(t, id, out.ID)
	require.Equal(t, "cancelled", out.Status,
		"cancel of a queued notification must return the cancelled body")

	// And it stays cancelled — the worker that was mid-Send must not
	// flip the status back to delivered.
	time.Sleep(4 * time.Second) // > provider latency
	require.Equal(t, "cancelled", fetchStatus(ctx, t, h.BaseURL, id),
		"worker post-Send must not overwrite a cancelled notification")
}

// TestCancel_RetryingAllowed: when the provider fails transiently
// the notification lands in `retrying`; cancel must accept that
// state too because the brief explicitly lists retrying as
// cancellable (CLAUDE.md §10 / openapi.yaml description).
func TestCancel_RetryingAllowed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	h.Provider.Configure(
		provider.WithSuccessRate(0),
		provider.WithFailureMode(provider.FailureTransient),
	)

	id := createNotification(ctx, t, h.BaseURL, "+15555550031")

	require.Eventually(t, func() bool {
		return fetchStatus(ctx, t, h.BaseURL, id) == "retrying"
	}, 30*time.Second, 200*time.Millisecond, "notification never reached retrying")

	resp := patchCancel(ctx, t, h.BaseURL, id)
	require.Equal(t, http.StatusOK, resp.status, "cancel body=%s", resp.body)
	require.Equal(t, "cancelled", fetchStatus(ctx, t, h.BaseURL, id))
}

// TestCancel_DeliveredRejected: terminal states must reject cancel
// with a 409 + RFC 7807 problem (CLAUDE.md §3.5). The domain's
// transition table is the source of truth — see domain/status.go.
func TestCancel_DeliveredRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	// Default mock: success on first try, so we land delivered fast.
	id := createNotification(ctx, t, h.BaseURL, "+15555550032")
	require.Eventually(t, func() bool {
		return fetchStatus(ctx, t, h.BaseURL, id) == "delivered"
	}, 30*time.Second, 200*time.Millisecond, "notification never reached delivered")

	resp := patchCancel(ctx, t, h.BaseURL, id)
	require.Equal(t, http.StatusConflict, resp.status, "cancel body=%s", resp.body)
	require.Equal(t, "application/problem+json", resp.contentType,
		"non-cancellable state must surface as RFC 7807 problem")

	var p struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	require.NoError(t, json.Unmarshal(resp.body, &p))
	require.Equal(t, "/probs/invalid-transition", p.Type)
	require.NotEmpty(t, p.Detail, "transition rejection must surface a human-readable detail")

	// And the status didn't drift.
	require.Equal(t, "delivered", fetchStatus(ctx, t, h.BaseURL, id))
}

// TestCancel_FailedRejected: same contract as delivered — once a
// notification reaches the failed terminal state, the cancel endpoint
// returns 409.
func TestCancel_FailedRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	// Permanent failure on the first attempt → notification goes
	// straight to `failed` (no retries since the result is not
	// retryable).
	h.Provider.Configure(
		provider.WithSuccessRate(0),
		provider.WithFailureMode(provider.FailurePermanent),
	)

	id := createNotification(ctx, t, h.BaseURL, "+15555550033")
	require.Eventually(t, func() bool {
		return fetchStatus(ctx, t, h.BaseURL, id) == "failed"
	}, 30*time.Second, 200*time.Millisecond, "notification never reached failed")

	resp := patchCancel(ctx, t, h.BaseURL, id)
	require.Equal(t, http.StatusConflict, resp.status, "cancel body=%s", resp.body)

	var p struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(resp.body, &p))
	require.Equal(t, "/probs/invalid-transition", p.Type)
	require.Equal(t, "failed", fetchStatus(ctx, t, h.BaseURL, id))
}

// TestCancel_UnknownID_404: an id with no matching row surfaces as
// 404 — the request fell off the rails before the cancel transition
// ever ran.
func TestCancel_UnknownID_404(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	const missing = "019e0000-0000-0000-0000-deadbeef0000"
	resp := patchCancel(ctx, t, h.BaseURL, missing)
	require.Equal(t, http.StatusNotFound, resp.status, "cancel body=%s", resp.body)

	var p struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(resp.body, &p))
	require.Equal(t, "/probs/not-found", p.Type)
}

// patchCancelResponse bundles the captured pieces of a PATCH /cancel
// response so assertions can quote the body in failure messages.
type patchCancelResponse struct {
	status      int
	contentType string
	body        []byte
}

// patchCancel issues PATCH /api/v1/notifications/{id}/cancel and
// returns the captured response. Errors bubble through require so a
// misconfigured test stops early.
func patchCancel(ctx context.Context, t *testing.T, baseURL, id string) patchCancelResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		baseURL+"/api/v1/notifications/"+id+"/cancel", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return patchCancelResponse{
		status:      resp.StatusCode,
		contentType: resp.Header.Get("Content-Type"),
		body:        body,
	}
}
