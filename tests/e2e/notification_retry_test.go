//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	hibikenasynq "github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/adapters/provider"
)

// TestNotificationRetry_TransientFailure_MarksRetrying: when the
// provider returns a transient failure the worker must NOT terminate
// the notification — it transitions to `retrying` and records a
// `retrying` log entry. The reconciler picks the row up later and
// re-enqueues it; this test focuses on the first transition because
// the production retry cadence (30s base) is too slow for e2e timing.
func TestNotificationRetry_TransientFailure_MarksRetrying(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	// Always-fail-transient for this scenario.
	h.Provider.Configure(
		provider.WithSuccessRate(0),
		provider.WithFailureMode(provider.FailureTransient),
	)

	id := createNotification(ctx, t, h.BaseURL, "+15555550010")

	require.Eventually(t, func() bool {
		return fetchStatus(ctx, t, h.BaseURL, id) == "retrying"
	}, 30*time.Second, 200*time.Millisecond, "notification never reached retrying")

	require.Len(t, h.Provider.Calls(), 1, "provider must have been invoked exactly once before retry")

	// Confirm the audit-log row exists so the trace endpoint surfaces
	// the transition end-to-end.
	events := traceEvents(ctx, t, h.Pool, id)
	require.Contains(t, events, "retrying", "trace must include a retrying entry; got %v", events)
}

// TestNotificationRetry_ExhaustedAttempts_MarksFailed: after
// defaultMaxAttempts transient failures the worker stops retrying and
// marks the notification `failed`. The test simulates the reconciler
// by re-enqueueing manually, exercising the worker's claim → fail
// path on each cycle.
func TestNotificationRetry_ExhaustedAttempts_MarksFailed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	h.Provider.Configure(
		provider.WithSuccessRate(0),
		provider.WithFailureMode(provider.FailureTransient),
	)

	id := createNotification(ctx, t, h.BaseURL, "+15555550011")

	// Wait for the first failure to land.
	require.Eventually(t, func() bool {
		return fetchStatus(ctx, t, h.BaseURL, id) == "retrying"
	}, 30*time.Second, 200*time.Millisecond, "first failure never landed")

	// Manually re-enqueue (reconciler stand-in) until the notification
	// is no longer retrying. defaultMaxAttempts=5 in ProcessNotification,
	// so we expect at most 4 more re-enqueues to push attempts to 5
	// and trip the terminal branch.
	rawClient := hibikenasynq.NewClient(hibikenasynq.RedisClientOpt{Addr: h.RedisAddr})
	defer func() { _ = rawClient.Close() }()

	const maxLoops = 10 // safety cap so a regression cannot hang the suite
	for i := 0; i < maxLoops; i++ {
		if fetchStatus(ctx, t, h.BaseURL, id) == "failed" {
			break
		}
		before := len(h.Provider.Calls())

		// Build a process-notification task by hand with NO TaskID so
		// asynq does not reject the re-enqueue as a duplicate.
		payload, err := json.Marshal(map[string]string{"notification_id": id})
		require.NoError(t, err)
		task := hibikenasynq.NewTask("notification:process", payload)
		_, err = rawClient.EnqueueContext(ctx, task,
			hibikenasynq.Queue("normal"),
			hibikenasynq.MaxRetry(0),
		)
		require.NoError(t, err, "manual re-enqueue %d", i)

		// Wait until the worker observably processed this re-enqueue —
		// signaled by a fresh provider call.
		require.Eventually(t, func() bool {
			return len(h.Provider.Calls()) > before
		}, 10*time.Second, 100*time.Millisecond,
			"re-enqueue %d: worker did not pick up the task; provider_calls=%d", i, len(h.Provider.Calls()))
	}

	require.Equal(t, "failed", fetchStatus(ctx, t, h.BaseURL, id),
		"after enough transient failures the notification must terminate as failed")

	events := traceEvents(ctx, t, h.Pool, id)
	require.Contains(t, events, "failed", "trace must include the terminal failed entry; got %v", events)
}

// createNotification is a small helper used by retry/lifecycle tests
// — POSTs a fresh sms notification and returns its server-assigned id.
func createNotification(ctx context.Context, t *testing.T, baseURL, recipient string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"channel":   "sms",
		"recipient": recipient,
		"content":   "retry-test",
	})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/notifications", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	var out struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out.ID
}

// traceEvents reads the notification_logs table directly and returns
// the ordered list of event names. Using the DB instead of the HTTP
// trace endpoint avoids an extra request on a hot loop.
func traceEvents(ctx context.Context, t *testing.T, pool *pgxpool.Pool, id string) []string {
	t.Helper()
	rows, err := pool.Query(ctx,
		"SELECT event FROM notification_logs WHERE notification_id = $1 ORDER BY created_at",
		id)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ev string
		require.NoError(t, rows.Scan(&ev))
		out = append(out, ev)
	}
	return out
}
