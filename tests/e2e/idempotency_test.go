//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestIdempotency_DuplicateKeyReturnsCachedResponse: two identical
// POSTs that share an Idempotency-Key — the second one must NOT
// create a fresh notification. Instead, the middleware replays the
// cached body of the first response. The replay collapses the
// original 202 Accepted to 200 OK so the client can distinguish
// "we just created this" from "you saw this before"
// (CLAUDE.md §3.9, §10).
func TestIdempotency_DuplicateKeyReturnsCachedResponse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	const key = "01HXYZIDEMPOTENCYKEY00001"
	body, err := json.Marshal(map[string]any{
		"channel":   "sms",
		"recipient": "+15555550020",
		"content":   "idempotency-test",
	})
	require.NoError(t, err)

	// --- First POST: 202 + fresh resource -----------------------------
	first := doIdempotentPost(ctx, t, h.BaseURL, key, body)
	require.Equal(t, http.StatusAccepted, first.status, "first call body=%s", first.body)

	var firstOut struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(first.body, &firstOut))
	require.NotEmpty(t, firstOut.ID)
	require.Equal(t, "queued", firstOut.Status)

	// --- Second POST: 200 + identical body, no new notification ------
	second := doIdempotentPost(ctx, t, h.BaseURL, key, body)
	require.Equal(t, http.StatusOK, second.status,
		"replay must collapse 202 → 200 per CLAUDE.md §3.9; got status=%d body=%s",
		second.status, second.body)
	require.JSONEq(t, string(first.body), string(second.body),
		"replay body must be byte-equivalent to the original response")

	// --- Verify only one notification exists --------------------------
	// Counting via the public list endpoint keeps the assertion at the
	// HTTP surface — no DB peek required for the load-bearing claim.
	listURL := h.BaseURL + "/api/v1/notifications?status=queued"
	listReq, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	require.NoError(t, err)
	listResp, err := http.DefaultClient.Do(listReq)
	require.NoError(t, err)
	listBytes, _ := io.ReadAll(listResp.Body)
	_ = listResp.Body.Close()
	require.Equal(t, http.StatusOK, listResp.StatusCode, "list body=%s", listBytes)

	var listOut struct {
		Items []struct {
			ID        string `json:"id"`
			Recipient string `json:"recipient"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(listBytes, &listOut))

	// Filter to our recipient to insulate against unrelated test
	// fixtures the harness may add later.
	matches := 0
	for _, n := range listOut.Items {
		if n.Recipient == "+15555550020" {
			matches++
			require.Equal(t, firstOut.ID, n.ID,
				"the one persisted notification must be the one returned to the first caller")
		}
	}
	require.Equal(t, 1, matches,
		"idempotent replay must not create a second notification; got %d", matches)

	// --- Worker still processes exactly once --------------------------
	require.Eventually(t, func() bool {
		return fetchStatus(ctx, t, h.BaseURL, firstOut.ID) == "delivered"
	}, 30*time.Second, 200*time.Millisecond,
		"the single notification did not reach delivered")

	require.Len(t, h.Provider.Calls(), 1,
		"the duplicate POST must not produce a second provider call")
}

// TestIdempotency_DifferentKeyDoesNotReplay: same body, different
// Idempotency-Key → two independent notifications. The key is the
// cache lookup primary; without a match the middleware delegates to
// the handler normally.
func TestIdempotency_DifferentKeyDoesNotReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	body, err := json.Marshal(map[string]any{
		"channel":   "sms",
		"recipient": "+15555550021",
		"content":   "two-keys",
	})
	require.NoError(t, err)

	first := doIdempotentPost(ctx, t, h.BaseURL, "key-one", body)
	require.Equal(t, http.StatusAccepted, first.status)
	second := doIdempotentPost(ctx, t, h.BaseURL, "key-two", body)
	require.Equal(t, http.StatusAccepted, second.status,
		"a fresh key produces a fresh 202, not a replay")

	var firstOut, secondOut struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(first.body, &firstOut))
	require.NoError(t, json.Unmarshal(second.body, &secondOut))
	require.NotEqual(t, firstOut.ID, secondOut.ID,
		"different keys must produce different notification ids")

	require.Eventually(t, func() bool {
		return len(h.Provider.Calls()) == 2
	}, 30*time.Second, 200*time.Millisecond,
		"two notifications must produce two provider calls; got %d", len(h.Provider.Calls()))
}

// TestIdempotency_SameKey_DifferentBody_Returns409Conflict: the
// Idempotency-Key contract is "same key, same intent." Reusing a key
// with a different request body is a client bug — the API must refuse
// the second POST with RFC 7807 409 Conflict instead of silently
// replaying the first response (which would hide the divergent intent
// from any audit). Bug observed in E2E_REPORT.md §C.
func TestIdempotency_SameKey_DifferentBody_Returns409Conflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	const key = "01HXYZIDEMPOTENCYKEY00009"
	bodyA, err := json.Marshal(map[string]any{
		"channel":   "sms",
		"recipient": "+15555550022",
		"content":   "intent-A",
	})
	require.NoError(t, err)
	bodyB, err := json.Marshal(map[string]any{
		"channel":   "email",
		"recipient": "intent-b@example.com",
		"content":   "intent-B",
	})
	require.NoError(t, err)

	first := doIdempotentPost(ctx, t, h.BaseURL, key, bodyA)
	require.Equal(t, http.StatusAccepted, first.status,
		"first call (intent A) must succeed: body=%s", first.body)

	second := doIdempotentPost(ctx, t, h.BaseURL, key, bodyB)
	require.Equal(t, http.StatusConflict, second.status,
		"same key + different body must be 409 Conflict; got status=%d body=%s",
		second.status, second.body)

	// RFC 7807 problem details body — application/problem+json with the
	// required title / status fields. Type is the project-specific URI
	// `/probs/idempotency-key-mismatch`.
	var prob struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	require.NoError(t, json.Unmarshal(second.body, &prob),
		"second response must be RFC 7807 JSON: %s", second.body)
	require.Equal(t, http.StatusConflict, prob.Status)
	require.NotEmpty(t, prob.Title)
	require.Contains(t, prob.Type, "idempotency",
		"problem type should advertise the conflict cause; got %q", prob.Type)

	// Only the first notification is persisted — the conflicting POST
	// must never reach the use case.
	listURL := h.BaseURL + "/api/v1/notifications?status=queued"
	listReq, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	require.NoError(t, err)
	listResp, err := http.DefaultClient.Do(listReq)
	require.NoError(t, err)
	listBytes, _ := io.ReadAll(listResp.Body)
	_ = listResp.Body.Close()

	var listOut struct {
		Items []struct {
			Recipient string `json:"recipient"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(listBytes, &listOut))

	matchesA, matchesB := 0, 0
	for _, n := range listOut.Items {
		switch n.Recipient {
		case "+15555550022":
			matchesA++
		case "intent-b@example.com":
			matchesB++
		}
	}
	require.Equal(t, 1, matchesA, "intent A should have been persisted exactly once; got %d", matchesA)
	require.Equal(t, 0, matchesB, "intent B must NOT have been persisted (409 rejected it); got %d", matchesB)
}

// idempotentResponse bundles the captured pieces of an HTTP response
// the idempotency tests need to assert against. Body is read eagerly
// so the caller can quote it in failure messages.
type idempotentResponse struct {
	status int
	body   []byte
}

// doIdempotentPost is a tiny helper that POSTs the supplied body with
// the supplied Idempotency-Key header and returns the captured
// response. Failures bubble through require so a misconfigured test
// stops early.
func doIdempotentPost(ctx context.Context, t *testing.T, baseURL, key string, body []byte) idempotentResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/api/v1/notifications", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	respBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return idempotentResponse{status: resp.StatusCode, body: respBytes}
}
