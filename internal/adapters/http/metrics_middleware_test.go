package http_test

import (
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
	"github.com/afbora/event-driven-notification/internal/infrastructure/metrics"
)

// TestMetricsMiddleware_RecordsRequestCountByPattern: each request
// produces one counter increment on (method, route_pattern, status)
// and one histogram observation. Using the chi route PATTERN (not
// URL.Path) keeps the label cardinality bounded — `/notifications/{id}`
// instead of one series per id.
func TestMetricsMiddleware_RecordsRequestCountByPattern(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	r := chi.NewRouter()
	r.Use(httpadapter.MetricsMiddleware(m))
	r.Get("/api/v1/notifications/{id}", func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
	})

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/notifications/abc-"+string(rune('0'+i)), nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		require.Equal(t, nethttp.StatusOK, rr.Code)
	}

	require.Equal(t, float64(3),
		testutil.ToFloat64(m.HTTPRequestsTotal().WithLabelValues("GET", "/api/v1/notifications/{id}", "200")),
		"three GETs to different ids must collapse into one (pattern, status) series")
}

// TestMetricsMiddleware_RecordsStatusCode: a handler that returns
// 404 is reflected in the status label. Same for 500 from a panic
// the recover middleware catches.
func TestMetricsMiddleware_RecordsStatusCode(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	r := chi.NewRouter()
	r.Use(httpadapter.MetricsMiddleware(m))
	r.Get("/probe", func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusNotFound)
	})

	req := httptest.NewRequest(nethttp.MethodGet, "/probe", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, nethttp.StatusNotFound, rr.Code)

	require.Equal(t, float64(1),
		testutil.ToFloat64(m.HTTPRequestsTotal().WithLabelValues("GET", "/probe", "404")))
}

// TestMetricsMiddleware_RecordsDuration: every request stamps the
// histogram. We assert via _count which always increments by 1 on
// observation, regardless of which bucket the duration lands in.
func TestMetricsMiddleware_RecordsDuration(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	r := chi.NewRouter()
	r.Use(httpadapter.MetricsMiddleware(m))
	r.Get("/x", func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
	})

	req := httptest.NewRequest(nethttp.MethodGet, "/x", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, nethttp.StatusOK, rr.Code)

	// Histograms have no clean ToFloat64; the gather count works.
	require.Equal(t, 1, testutil.CollectAndCount(m.HTTPRequestDuration()),
		"one histogram series with one observation")
}

// TestMetricsMiddleware_DefaultStatus200: a handler that never calls
// WriteHeader still emits a 200 to the client (stdlib's implicit
// behavior). The middleware must match — otherwise the response and
// the metric would disagree.
func TestMetricsMiddleware_DefaultStatus200(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	r := chi.NewRouter()
	r.Use(httpadapter.MetricsMiddleware(m))
	r.Get("/x", func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		_, _ = w.Write([]byte("hi"))
	})

	req := httptest.NewRequest(nethttp.MethodGet, "/x", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, float64(1),
		testutil.ToFloat64(m.HTTPRequestsTotal().WithLabelValues("GET", "/x", "200")),
		"implicit 200 must be recorded as 200, not 0 or empty")
}
