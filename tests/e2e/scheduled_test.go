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

// TestScheduled_NotProcessedBeforeScheduledAt: a notification with a
// future `scheduled_at` is queued through asynq's scheduled set, not
// the immediate pending list. The worker does not see it (and the
// provider is not called) until the scheduled time arrives. This is
// the load-bearing claim that the scheduling feature actually defers
// work end-to-end.
func TestScheduled_NotProcessedBeforeScheduledAt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	// 3-second deferral keeps the test fast while leaving the asynq
	// scheduler enough room to demonstrate the hold-then-release
	// behavior. asynq's default forwarder poll cadence is well under
	// a second, so the actual release lands shortly after the
	// deadline.
	scheduledAt := time.Now().Add(3 * time.Second).UTC()

	id := postScheduled(ctx, t, h.BaseURL, "+15555550040", scheduledAt)

	// Immediately after POST the worker must NOT have picked it up.
	// Sleep ~1s to give an over-eager worker time to surface a bug.
	time.Sleep(1 * time.Second)
	require.Equal(t, "queued", fetchStatus(ctx, t, h.BaseURL, id),
		"scheduled notification must stay queued until scheduled_at")
	require.Empty(t, h.Provider.Calls(),
		"provider must not be invoked before the scheduled time")

	// Eventually — after scheduled_at + the asynq forwarder interval
	// — the task is released, the worker claims it, and the
	// notification reaches delivered.
	require.Eventually(t, func() bool {
		return fetchStatus(ctx, t, h.BaseURL, id) == "delivered"
	}, 30*time.Second, 200*time.Millisecond,
		"notification did not reach delivered after scheduled_at")

	require.Len(t, h.Provider.Calls(), 1,
		"provider must be invoked exactly once after the scheduled release")
}

// TestScheduled_AlreadyPastScheduledAt_RunsImmediately: when the
// caller sets `scheduled_at` in the past (e.g., due to clock drift
// or a deliberate "process ASAP" intent), asynq's scheduler releases
// the task on its next poll — the test confirms the path does not
// silently hang.
func TestScheduled_AlreadyPastScheduledAt_RunsImmediately(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	scheduledAt := time.Now().Add(-1 * time.Hour).UTC()
	id := postScheduled(ctx, t, h.BaseURL, "+15555550041", scheduledAt)

	require.Eventually(t, func() bool {
		return fetchStatus(ctx, t, h.BaseURL, id) == "delivered"
	}, 30*time.Second, 200*time.Millisecond,
		"a past scheduled_at must not block delivery; worker should run on next poll")
}

// postScheduled POSTs a notification carrying the supplied
// scheduled_at and returns the new id. Kept here rather than in
// notification_lifecycle_test.go because the lifecycle helper
// deliberately omits scheduled_at to exercise the immediate path.
func postScheduled(ctx context.Context, t *testing.T, baseURL, recipient string, scheduledAt time.Time) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"channel":      "sms",
		"recipient":    recipient,
		"content":      "scheduled-test",
		"scheduled_at": scheduledAt.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/api/v1/notifications", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	respBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode, "create body=%s", respBytes)

	var out struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(respBytes, &out))
	return out.ID
}
