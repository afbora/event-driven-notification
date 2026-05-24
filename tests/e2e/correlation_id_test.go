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

// TestCorrelationID_PropagatesAcrossEveryStage: the inbound
// X-Correlation-ID flows verbatim through API → queue → worker →
// trace logs. This is the load-bearing claim that operators can
// pivot on a single id to investigate any cross-component issue
// (CLAUDE.md §2.3 / §12.3).
func TestCorrelationID_PropagatesAcrossEveryStage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	const corr = "01HXYZE2ECORRPROPAGATION01"

	body, err := json.Marshal(map[string]any{
		"channel":   "sms",
		"recipient": "+15555550070",
		"content":   "correlation-propagation",
	})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		h.BaseURL+"/api/v1/notifications", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Correlation-ID", corr)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	respBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode, "create body=%s", respBytes)

	// --- Stage 1: response carries the inbound id back out -------------
	require.Equal(t, corr, resp.Header.Get("X-Correlation-ID"),
		"response header must echo the inbound correlation id")

	var created struct {
		ID            string `json:"id"`
		CorrelationID string `json:"correlation_id"`
	}
	require.NoError(t, json.Unmarshal(respBytes, &created))
	require.Equal(t, corr, created.CorrelationID,
		"response body's correlation_id must match the inbound header")

	// --- Stage 2: wait for the worker to land the notification --------
	require.Eventually(t, func() bool {
		return fetchStatus(ctx, t, h.BaseURL, created.ID) == "delivered"
	}, 30*time.Second, 200*time.Millisecond,
		"notification never reached delivered")

	// --- Stage 3: trace endpoint — every log entry inherits the id ----
	traceURL := h.BaseURL + "/api/v1/notifications/" + created.ID + "/trace"
	traceReq, err := http.NewRequestWithContext(ctx, http.MethodGet, traceURL, nil)
	require.NoError(t, err)
	traceResp, err := http.DefaultClient.Do(traceReq)
	require.NoError(t, err)
	traceBytes, _ := io.ReadAll(traceResp.Body)
	_ = traceResp.Body.Close()
	require.Equal(t, http.StatusOK, traceResp.StatusCode, "trace body=%s", traceBytes)

	var trace struct {
		NotificationID string `json:"notification_id"`
		Entries        []struct {
			Event         string `json:"event"`
			CorrelationID string `json:"correlation_id"`
		} `json:"entries"`
	}
	require.NoError(t, json.Unmarshal(traceBytes, &trace))
	require.Equal(t, created.ID, trace.NotificationID)
	require.NotEmpty(t, trace.Entries, "trace must surface at least the initial events")

	// Every transition the worker wrote (created, queued, processing,
	// delivered) inherits the inbound correlation id verbatim.
	for _, entry := range trace.Entries {
		require.Equalf(t, corr, entry.CorrelationID,
			"trace event %q did not inherit the inbound correlation id; got %q",
			entry.Event, entry.CorrelationID)
	}

	// Sanity check: the trace covers the canonical end-to-end path.
	events := make(map[string]bool, len(trace.Entries))
	for _, e := range trace.Entries {
		events[e.Event] = true
	}
	for _, must := range []string{"created", "queued", "processing", "delivered"} {
		require.Truef(t, events[must],
			"trace missing %q event; got events=%v", must, events)
	}
}

// TestCorrelationID_AbsentHeader_GeneratedServerSide: when the
// caller omits X-Correlation-ID, the API generates one and uses it
// consistently. The generated id still flows through every layer —
// the propagation contract is not "client must supply", just "every
// transition shares the same id".
func TestCorrelationID_AbsentHeader_GeneratedServerSide(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	body, err := json.Marshal(map[string]any{
		"channel":   "sms",
		"recipient": "+15555550071",
		"content":   "generated-corr",
	})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		h.BaseURL+"/api/v1/notifications", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	respBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	var created struct {
		ID            string `json:"id"`
		CorrelationID string `json:"correlation_id"`
	}
	require.NoError(t, json.Unmarshal(respBytes, &created))
	require.NotEmpty(t, created.CorrelationID,
		"server must generate a correlation id when the header is absent")

	// The response X-Correlation-ID header must match the body's
	// correlation_id — they are the same id, just exposed twice.
	require.Equal(t, created.CorrelationID, resp.Header.Get("X-Correlation-ID"))

	// And the generated id propagates the same way as a client-supplied
	// one: wait for delivery, then verify trace entries inherit it.
	require.Eventually(t, func() bool {
		return fetchStatus(ctx, t, h.BaseURL, created.ID) == "delivered"
	}, 30*time.Second, 200*time.Millisecond)

	events := traceEvents(ctx, t, h.Pool, created.ID)
	require.NotEmpty(t, events)

	// Read correlation_ids directly from the DB to confirm consistency.
	rows, err := h.Pool.Query(ctx,
		"SELECT DISTINCT correlation_id FROM notification_logs WHERE notification_id = $1",
		created.ID)
	require.NoError(t, err)
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.Equal(t, []string{created.CorrelationID}, ids,
		"every log row must share the single generated correlation id")
}
