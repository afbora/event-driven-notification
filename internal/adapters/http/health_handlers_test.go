package http_test

import (
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
)

// buildHealthTestServer wires only the health endpoints. Tests pass
// the readiness check funcs they want — empty slice means "no
// downstream dependencies to verify".
func buildHealthTestServer(t *testing.T, checks []httpadapter.ReadinessCheck) chi.Router {
	t.Helper()
	server := httpadapter.NewServer(httpadapter.ServerOptions{
		ReadinessChecks: checks,
	})
	r := chi.NewRouter()
	api.HandlerFromMux(api.NewStrictHandlerWithOptions(
		server, nil,
		api.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  httpadapter.RespondWithError,
			ResponseErrorHandlerFunc: httpadapter.RespondWithError,
		},
	), r)
	return r
}

// TestHealthzLive_AlwaysOK: liveness ignores downstream state — it
// only signals that the process itself is alive. Kubernetes uses
// this to decide whether to restart the pod.
func TestHealthzLive_AlwaysOK(t *testing.T) {
	r := buildHealthTestServer(t, nil)

	req := httptest.NewRequest(nethttp.MethodGet, "/healthz/live", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	var out api.HealthResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Equal(t, api.Ok, out.Status)
}

// TestHealthzReady_AllChecksPass_200: readiness is 200 only when
// every injected dependency check returns nil.
func TestHealthzReady_AllChecksPass_200(t *testing.T) {
	called := 0
	// Same closure referenced twice — the intent is "two distinct
	// registrations that both pass." Declaring it once instead of
	// inlining a duplicate body satisfies SonarCloud S4144 without
	// changing behavior: the called counter still hits 2 after the
	// handler invokes every check.
	passCheck := func(_ context.Context) error { called++; return nil }
	checks := []httpadapter.ReadinessCheck{passCheck, passCheck}
	r := buildHealthTestServer(t, checks)

	req := httptest.NewRequest(nethttp.MethodGet, "/healthz/ready", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	require.Equal(t, 2, called, "every check must be invoked")

	var out api.HealthResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Equal(t, api.Ok, out.Status)
}

// TestHealthzReady_AnyCheckFails_503: a single failing check flips
// the whole response to 503 — Kubernetes stops routing traffic. The
// body is RFC 7807 to match the spec.
func TestHealthzReady_AnyCheckFails_503(t *testing.T) {
	checks := []httpadapter.ReadinessCheck{
		func(_ context.Context) error { return nil },
		func(_ context.Context) error { return errors.New("redis: connection refused") },
	}
	r := buildHealthTestServer(t, checks)

	req := httptest.NewRequest(nethttp.MethodGet, "/healthz/ready", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusServiceUnavailable, rr.Code, "body=%s", rr.Body.String())
	require.Equal(t, "application/problem+json", rr.Header().Get("Content-Type"))

	var p struct {
		Type   string `json:"type"`
		Status int    `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &p))
	require.Equal(t, nethttp.StatusServiceUnavailable, p.Status)
	require.Equal(t, "/probs/dependency-unavailable", p.Type)
}

// TestHealthzReady_NoChecks_200: empty checks slice is a valid
// configuration — the API is ready when the process is up. Used in
// integration tests and during local development where there is no
// Postgres or Redis to probe.
func TestHealthzReady_NoChecks_200(t *testing.T) {
	r := buildHealthTestServer(t, nil)

	req := httptest.NewRequest(nethttp.MethodGet, "/healthz/ready", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
}
