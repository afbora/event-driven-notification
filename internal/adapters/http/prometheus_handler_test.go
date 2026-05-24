package http_test

import (
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
)

// buildMetricsTestServer wires only the Prometheus gatherer.
func buildMetricsTestServer(t *testing.T, gatherer prometheus.Gatherer) chi.Router {
	t.Helper()
	server := httpadapter.NewServer(httpadapter.ServerOptions{
		PrometheusGatherer: gatherer,
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

// TestPrometheusMetrics_RendersRegisteredMetrics: a counter registered
// in the gatherer surfaces in the exposition output. The response is
// text/plain with status 200 — Prometheus scrape format.
func TestPrometheusMetrics_RendersRegisteredMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "notifications_test_total",
		Help: "Test counter for the /metrics handler unit test.",
	})
	reg.MustRegister(c)
	c.Add(7)

	r := buildMetricsTestServer(t, reg)

	req := httptest.NewRequest(nethttp.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	require.Contains(t, rr.Header().Get("Content-Type"), "text/plain")

	body, err := io.ReadAll(rr.Body)
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "# HELP notifications_test_total",
		"output must carry the HELP comment line")
	require.Contains(t, text, "# TYPE notifications_test_total counter",
		"output must declare the metric type")
	require.Contains(t, text, "notifications_test_total 7")
}

// TestPrometheusMetrics_EmptyRegistry_200Empty: an empty registry is
// a valid configuration — the handler returns 200 with no metric
// lines. Prometheus tolerates this.
func TestPrometheusMetrics_EmptyRegistry_200Empty(t *testing.T) {
	reg := prometheus.NewRegistry()
	r := buildMetricsTestServer(t, reg)

	req := httptest.NewRequest(nethttp.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code)
	require.Empty(t, rr.Body.String(),
		"empty registry produces an empty body — no help/type lines")
}

// TestPrometheusMetrics_NilGatherer_NotImplemented: when the server
// has not been wired with a gatherer the embedded stub semantics kick
// in and the response is 501.
func TestPrometheusMetrics_NilGatherer_NotImplemented(t *testing.T) {
	r := buildMetricsTestServer(t, nil)

	req := httptest.NewRequest(nethttp.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNotImplemented, rr.Code)
}
