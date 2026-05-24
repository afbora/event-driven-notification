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

// TestNotificationLifecycle_HappyPath_Delivered: the canonical
// end-to-end claim. A POST through the public API enqueues a task,
// the worker dequeues and calls the mock provider, which by default
// succeeds, and a subsequent GET observes the notification in the
// terminal `delivered` state. This is the load-bearing proof that
// every layer is connected: chi → strict-server → use case → repo →
// asynq queue → worker → provider → repo update → pub/sub → trace
// log.
func TestNotificationLifecycle_HappyPath_Delivered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	// --- POST /api/v1/notifications ------------------------------------
	createBody, err := json.Marshal(map[string]any{
		"channel":   "sms",
		"recipient": "+15555550001",
		"content":   "Hello from the e2e suite",
		"priority":  "normal",
	})
	require.NoError(t, err)

	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		h.BaseURL+"/api/v1/notifications", bytes.NewReader(createBody))
	require.NoError(t, err)
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Correlation-ID", "e2e-lifecycle-happy")

	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err, "POST /api/v1/notifications")
	createBytes := readBody(t, createResp)
	_ = createResp.Body.Close()

	require.Equal(t, http.StatusAccepted, createResp.StatusCode,
		"create body=%s", createBytes)
	require.Equal(t, "e2e-lifecycle-happy", createResp.Header.Get("X-Correlation-ID"),
		"the inbound correlation id must echo back unchanged")

	var created struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		CorrelationID string `json:"correlation_id"`
	}
	require.NoError(t, json.Unmarshal(createBytes, &created), "create body=%s", createBytes)

	require.NotEmpty(t, created.ID, "response must carry the new id")
	require.Equal(t, "queued", created.Status,
		"CreateNotification advances pending → queued before returning so the worker's atomic claim accepts the task")

	// --- Wait for the worker to mark it delivered ----------------------
	// 30s budget gives the asynq scheduler / process loop enough slack
	// to dequeue, atomically claim, call the mock provider, and write
	// the terminal status — the mock has zero artificial latency so a
	// healthy stack lands in well under a second.
	var final string
	require.Eventually(t, func() bool {
		final = fetchStatus(ctx, t, h.BaseURL, created.ID)
		return final == "delivered"
	}, 30*time.Second, 200*time.Millisecond,
		"notification did not reach delivered; last seen status=%q", final)

	// --- Provider must have been invoked exactly once on the happy path
	require.Len(t, h.Provider.Calls(), 1,
		"mock provider should record exactly one call for a successful single-shot delivery")
	call := h.Provider.Calls()[0]
	require.Equal(t, "+15555550001", call.Recipient)
	require.Equal(t, "Hello from the e2e suite", call.Content)
}

// fetchStatus issues a GET against the public read endpoint and
// returns the notification's current status. Errors translate to
// require.Eventually retry semantics by returning an empty string,
// so a transient blip during processing does not flake the test.
func fetchStatus(ctx context.Context, t *testing.T, baseURL, id string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/api/v1/notifications/"+id, nil)
	if err != nil {
		t.Logf("build GET: %v", err)
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("do GET: %v", err)
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Logf("GET %s/api/v1/notifications/%s → %d body=%s", baseURL, id, resp.StatusCode, body)
		return ""
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Logf("decode GET body=%s err=%v", body, err)
		return ""
	}
	return out.Status
}

// readBody reads the full response body so test assertions can quote
// it in failure messages. The caller is responsible for closing the
// response.
func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return b
}
