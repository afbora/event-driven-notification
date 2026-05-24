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

// buildJSONMetricsTestServer wires only the JSON metrics provider.
func buildJSONMetricsTestServer(t *testing.T, provider httpadapter.JSONMetricsProvider) chi.Router {
	t.Helper()
	server := httpadapter.NewServer(httpadapter.ServerOptions{
		JSONMetrics: provider,
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

// TestJSONMetrics_HappyPath: the provider's snapshot is serialized
// verbatim — every numeric counter and the success rate land in the
// response. The endpoint is the non-Prometheus consumer's view of
// the same data /metrics exposes.
func TestJSONMetrics_HappyPath(t *testing.T) {
	provider := func(_ context.Context) (httpadapter.JSONMetricsSnapshot, error) {
		successRate := 0.987
		return httpadapter.JSONMetricsSnapshot{
			CreatedPerMinute:   120,
			DeliveredPerMinute: 118,
			FailedPerMinute:    2,
			QueueDepth:         42,
			SuccessRate:        &successRate,
		}, nil
	}
	r := buildJSONMetricsTestServer(t, provider)

	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/metrics", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	var out api.MetricsSummary
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))

	require.NotNil(t, out.CreatedPerMinute)
	require.Equal(t, 120, *out.CreatedPerMinute)
	require.NotNil(t, out.DeliveredPerMinute)
	require.Equal(t, 118, *out.DeliveredPerMinute)
	require.NotNil(t, out.FailedPerMinute)
	require.Equal(t, 2, *out.FailedPerMinute)
	require.NotNil(t, out.QueueDepth)
	require.Equal(t, 42, *out.QueueDepth)
	require.NotNil(t, out.SuccessRate)
	require.InDelta(t, 0.987, *out.SuccessRate, 1e-9)
}

// TestJSONMetrics_NilSuccessRate_Omitted: the snapshot's SuccessRate
// is optional — when the provider has nothing to report (e.g. zero
// throughput in the window) the response omits the field entirely
// rather than emitting an ambiguous 0.
func TestJSONMetrics_NilSuccessRate_Omitted(t *testing.T) {
	provider := func(_ context.Context) (httpadapter.JSONMetricsSnapshot, error) {
		return httpadapter.JSONMetricsSnapshot{}, nil
	}
	r := buildJSONMetricsTestServer(t, provider)

	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/metrics", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code)
	require.NotContains(t, rr.Body.String(), "success_rate",
		"absent success rate must be omitted, not emitted as null or 0")
}

// TestJSONMetrics_ProviderError_500: a provider failure (e.g. asynq
// inspector unreachable) becomes a generic 500. The raw error stays
// in the log, not the body.
func TestJSONMetrics_ProviderError_500(t *testing.T) {
	provider := func(_ context.Context) (httpadapter.JSONMetricsSnapshot, error) {
		return httpadapter.JSONMetricsSnapshot{}, errors.New("asynq: inspector closed")
	}
	r := buildJSONMetricsTestServer(t, provider)

	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/metrics", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusInternalServerError, rr.Code, "body=%s", rr.Body.String())
	require.NotContains(t, rr.Body.String(), "inspector closed",
		"raw error text must not leak to the client")
}

// TestJSONMetrics_NilProvider_NotImplemented: 501 when the provider
// slot is empty.
func TestJSONMetrics_NilProvider_NotImplemented(t *testing.T) {
	r := buildJSONMetricsTestServer(t, nil)
	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/metrics", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNotImplemented, rr.Code)
}
