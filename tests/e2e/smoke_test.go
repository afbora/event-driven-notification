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
)

// TestHarness_BringsUpFullStack: the most basic e2e claim — the
// Harness wires every dependency (postgres + redis + asynq + http +
// worker) and the resulting /healthz/live endpoint returns 200 with
// the canonical health payload. If this test fails, every other e2e
// test will too — fix this first.
func TestHarness_BringsUpFullStack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	h := NewHarness(ctx, t)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.BaseURL+"/healthz/live", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "GET /healthz/live")
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var out struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	require.Equal(t, "ok", out.Status)
}

// TestHarness_ReadinessProbesDependencies: with both postgres and redis
// running, /healthz/ready also returns 200 — proves the readiness
// checks reach real downstream services.
func TestHarness_ReadinessProbesDependencies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	h := NewHarness(ctx, t)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.BaseURL+"/healthz/ready", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"both pg and redis are up; ready must report 200")
}
