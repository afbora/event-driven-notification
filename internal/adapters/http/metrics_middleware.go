package http

import (
	nethttp "net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/afbora/event-driven-notification/internal/infrastructure/metrics"
)

// MetricsMiddleware records one http_requests_total increment and
// one http_request_duration_seconds observation per request
// (CLAUDE.md §12.1). The label set uses the chi route PATTERN (not
// URL.Path) so high-cardinality path params like /notifications/{id}
// collapse into a single series.
//
// Unmatched requests fall into a fixed "unknown" pattern so a script
// spraying random paths cannot blow up the cardinality.
func MetricsMiddleware(m *metrics.Metrics) func(nethttp.Handler) nethttp.Handler {
	return func(next nethttp.Handler) nethttp.Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			start := time.Now()
			tracker := &statusTracker{ResponseWriter: w, status: nethttp.StatusOK}

			next.ServeHTTP(tracker, r)

			// chi populates the route pattern AFTER routing — so we
			// read it post-ServeHTTP.
			pattern := "unknown"
			if rc := chi.RouteContext(r.Context()); rc != nil {
				if p := rc.RoutePattern(); p != "" {
					pattern = p
				}
			}

			m.HTTPRequest(r.Method, pattern, strconv.Itoa(tracker.status))
			m.ObserveHTTPRequest(r.Method, pattern, time.Since(start))
		})
	}
}

// statusTracker is a minimal ResponseWriter wrapper that captures
// the status code so the metrics middleware can label requests
// correctly. Defaults to 200 when WriteHeader is never called —
// matching stdlib's implicit behavior.
type statusTracker struct {
	nethttp.ResponseWriter
	status      int
	wroteHeader bool
}

// WriteHeader records the status the first time it is called and
// forwards to the underlying writer. Duplicate calls match stdlib
// behavior — only the first wins.
func (s *statusTracker) WriteHeader(status int) {
	if s.wroteHeader {
		return
	}
	s.status = status
	s.wroteHeader = true
	s.ResponseWriter.WriteHeader(status)
}

// Write mirrors stdlib's "implicit 200 on first Write" behavior.
func (s *statusTracker) Write(p []byte) (int, error) {
	if !s.wroteHeader {
		s.wroteHeader = true
	}
	return s.ResponseWriter.Write(p)
}
