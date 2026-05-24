//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestNotificationBatch_AllEnqueuedAndProcessed: a single batch POST
// persists every notification under a shared batch id, the worker
// processes each one, and a follow-up GET on the batch endpoint
// returns every member in the terminal delivered state.
func TestNotificationBatch_AllEnqueuedAndProcessed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	const batchSize = 5

	// --- POST /api/v1/notifications/batch ------------------------------
	items := make([]map[string]any, batchSize)
	for i := 0; i < batchSize; i++ {
		items[i] = map[string]any{
			"channel":   "sms",
			"recipient": fmt.Sprintf("+155555500%02d", i),
			"content":   fmt.Sprintf("batch msg %d", i),
		}
	}
	body, err := json.Marshal(map[string]any{"notifications": items})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		h.BaseURL+"/api/v1/notifications/batch", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Correlation-ID", "e2e-batch-happy")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	respBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode, "create body=%s", respBytes)

	var created struct {
		ID            string `json:"id"`
		Size          int    `json:"size"`
		CorrelationID string `json:"correlation_id"`
	}
	require.NoError(t, json.Unmarshal(respBytes, &created))
	require.NotEmpty(t, created.ID)
	require.Equal(t, batchSize, created.Size, "batch size echoes the count of notifications submitted")
	require.Equal(t, "e2e-batch-happy", created.CorrelationID,
		"every notification in the batch shares the inbound correlation id (CLAUDE.md §2.3)")

	// --- Wait for the worker to deliver every member ------------------
	require.Eventually(t, func() bool {
		return countDelivered(ctx, t, h.BaseURL, created.ID) == batchSize
	}, 30*time.Second, 200*time.Millisecond,
		"not all batch members reached delivered within the budget")

	require.Len(t, h.Provider.Calls(), batchSize,
		"provider must be invoked once per batch member on the happy path")

	// --- GET /api/v1/notifications/batch/{id} -------------------------
	// Final sanity check via the public read endpoint: every member is
	// inlined (GET response includes Notifications, unlike POST 202),
	// every status is delivered, every correlation id matches.
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		h.BaseURL+"/api/v1/notifications/batch/"+created.ID, nil)
	require.NoError(t, err)
	getResp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	getBytes, _ := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	require.Equal(t, http.StatusOK, getResp.StatusCode, "get body=%s", getBytes)

	var got struct {
		ID            string `json:"id"`
		Size          int    `json:"size"`
		CorrelationID string `json:"correlation_id"`
		Notifications []struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			Recipient     string `json:"recipient"`
			CorrelationID string `json:"correlation_id"`
		} `json:"notifications"`
	}
	require.NoError(t, json.Unmarshal(getBytes, &got))
	require.Equal(t, created.ID, got.ID)
	require.Equal(t, batchSize, got.Size)
	require.Len(t, got.Notifications, batchSize,
		"GET inlines every member (unlike POST 202 which omits them)")
	for i, n := range got.Notifications {
		require.Equal(t, "delivered", n.Status,
			"member %d (recipient=%s) must be delivered", i, n.Recipient)
		require.Equal(t, "e2e-batch-happy", n.CorrelationID,
			"member %d must inherit the batch's correlation id", i)
	}
}

// countDelivered returns how many of the batch's member notifications
// are in the delivered state. Pulled from the DB rather than the trace
// endpoint to keep the polling loop cheap.
func countDelivered(ctx context.Context, t *testing.T, baseURL, batchID string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/api/v1/notifications/batch/"+batchID, nil)
	if err != nil {
		return 0
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var out struct {
		Notifications []struct {
			Status string `json:"status"`
		} `json:"notifications"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0
	}
	n := 0
	for _, x := range out.Notifications {
		if x.Status == "delivered" {
			n++
		}
	}
	return n
}
