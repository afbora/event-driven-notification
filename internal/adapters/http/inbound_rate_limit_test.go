package http_test

import (
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
)

// fakeLimiter is a deterministic ports.RateLimiter for the middleware
// tests — every Allow call is recorded so assertions can inspect the
// bucket key the middleware computed, and the return value is fully
// controllable.
type fakeLimiter struct {
	mu sync.Mutex

	allowed    bool
	retryAfter time.Duration
	err        error

	buckets []string
}

func (f *fakeLimiter) Allow(_ context.Context, bucket string) (bool, time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buckets = append(f.buckets, bucket)
	return f.allowed, f.retryAfter, f.err
}

func (f *fakeLimiter) lastBucket() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.buckets) == 0 {
		return ""
	}
	return f.buckets[len(f.buckets)-1]
}

// TestInboundRateLimit_Allowed_PassesThrough: under-limit requests
// reach the wrapped handler and the response is whatever the handler
// emits. The limiter saw exactly one Allow call.
func TestInboundRateLimit_Allowed_PassesThrough(t *testing.T) {
	limiter := &fakeLimiter{allowed: true}
	mw := httpadapter.InboundRateLimitMiddleware(limiter, nil)

	called := false
	handler := mw(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		called = true
		w.WriteHeader(nethttp.StatusOK)
	}))

	req := httptest.NewRequest(nethttp.MethodGet, "/x", nil)
	req.RemoteAddr = "1.2.3.4:55555"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.True(t, called, "handler must be reached when allowed")
	require.Equal(t, nethttp.StatusOK, rr.Code)
	require.Equal(t, "ip:1.2.3.4", limiter.lastBucket())
}

// TestInboundRateLimit_Denied_Returns429: over-limit requests are
// stopped at the middleware. The response carries the canonical 429
// status, a Retry-After header reflecting the limiter's hint, and the
// downstream handler is not invoked.
func TestInboundRateLimit_Denied_Returns429(t *testing.T) {
	limiter := &fakeLimiter{allowed: false, retryAfter: 12 * time.Second}
	mw := httpadapter.InboundRateLimitMiddleware(limiter, nil)

	called := false
	handler := mw(nethttp.HandlerFunc(func(_ nethttp.ResponseWriter, _ *nethttp.Request) {
		called = true
	}))

	req := httptest.NewRequest(nethttp.MethodGet, "/x", nil)
	req.RemoteAddr = "9.9.9.9:1"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.False(t, called, "handler must not run when limit exceeded")
	require.Equal(t, nethttp.StatusTooManyRequests, rr.Code)
	require.Equal(t, "12", rr.Header().Get("Retry-After"),
		"retry-after must be the limiter's hint, rounded up to seconds")
}

// TestInboundRateLimit_Denied_BodyIsRFC7807ProblemDetails: CLAUDE.md
// §3.5 / §10 mandate RFC 7807 problem responses for every error the
// API emits, including 429. The current minimal `{"error": "..."}`
// envelope is the gap captured in E2E_REPORT.md §H — fix it.
//
// Required shape:
//
//	Content-Type: application/problem+json
//	body:        { "type":..., "title":..., "status": 429, "detail":... }
//
// The Retry-After header from the limiter must still be present —
// problem details and retry guidance are independent contracts.
func TestInboundRateLimit_Denied_BodyIsRFC7807ProblemDetails(t *testing.T) {
	limiter := &fakeLimiter{allowed: false, retryAfter: 30 * time.Second}
	mw := httpadapter.InboundRateLimitMiddleware(limiter, nil)

	handler := mw(nethttp.HandlerFunc(func(_ nethttp.ResponseWriter, _ *nethttp.Request) {
		t.Fatal("handler must not be invoked on a 429")
	}))

	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/notifications", nil)
	req.RemoteAddr = "9.9.9.9:1"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusTooManyRequests, rr.Code)
	require.Equal(t, "application/problem+json", rr.Header().Get("Content-Type"),
		"RFC 7807 §3 mandates application/problem+json")
	require.Equal(t, "30", rr.Header().Get("Retry-After"),
		"Retry-After must survive the migration to RFC 7807")

	var prob httpadapter.Problem
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &prob),
		"body must be parseable RFC 7807 Problem; got %s", rr.Body.String())
	require.Equal(t, nethttp.StatusTooManyRequests, prob.Status,
		"Problem.status must mirror the HTTP status")
	require.NotEmpty(t, prob.Title, "Problem.title is required by RFC 7807")
	require.NotEmpty(t, prob.Type, "Problem.type should advertise the cause")
	require.Equal(t, "/api/v1/notifications", prob.Instance,
		"Problem.instance defaults to the request URL path")
}

// TestInboundRateLimit_Denied_IncrementsHitMetric: every 429 must
// increment inbound_rate_limit_hits_total tagged with the URL path
// so the alerting stack (CLAUDE.md §3.8 / docs/RUNBOOK.md) can
// attribute pressure per endpoint. A nil recorder is also legal (used
// by the e2e harness) — that path is exercised by the other Denied
// tests above and must not panic.
func TestInboundRateLimit_Denied_IncrementsHitMetric(t *testing.T) {
	limiter := &fakeLimiter{allowed: false, retryAfter: 1 * time.Second}
	recorder := &recordingHitMetrics{}
	mw := httpadapter.InboundRateLimitMiddleware(limiter, recorder)

	handler := mw(nethttp.HandlerFunc(func(_ nethttp.ResponseWriter, _ *nethttp.Request) {
		t.Fatal("handler must not be invoked on a 429")
	}))

	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/notifications", nil)
	req.RemoteAddr = "9.9.9.9:1"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusTooManyRequests, rr.Code)
	require.Equal(t, []string{"/api/v1/notifications"}, recorder.endpoints,
		"the rejected request must be counted once, tagged with the URL path")
}

// recordingHitMetrics is the in-memory recorder used by the metric
// assertion. Keeps the test fixture local to this file rather than
// reaching into infrastructure/metrics — the middleware contract is
// just the InboundRateLimitHit method.
type recordingHitMetrics struct {
	mu        sync.Mutex
	endpoints []string
}

func (r *recordingHitMetrics) InboundRateLimitHit(endpoint string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.endpoints = append(r.endpoints, endpoint)
}

// TestInboundRateLimit_LimiterError_FailsOpen: when the backing store
// errors out (Redis down, network blip), the middleware does not deny —
// it logs the error and lets the request through. Availability beats
// strict enforcement for the inbound limiter.
func TestInboundRateLimit_LimiterError_FailsOpen(t *testing.T) {
	limiter := &fakeLimiter{err: errors.New("redis: connection refused")}
	mw := httpadapter.InboundRateLimitMiddleware(limiter, nil)

	called := false
	handler := mw(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		called = true
		w.WriteHeader(nethttp.StatusOK)
	}))

	req := httptest.NewRequest(nethttp.MethodGet, "/x", nil)
	req.RemoteAddr = "5.5.5.5:1"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.True(t, called, "fail-open: handler must still be reached on limiter error")
	require.Equal(t, nethttp.StatusOK, rr.Code)
}

// TestInboundRateLimit_ClientIP_PrefersXForwardedFor: when the request
// arrives via a reverse proxy, X-Forwarded-For carries the original
// client. The middleware uses the leftmost entry for the rate-limit
// bucket — that is the closest ip to the client.
func TestInboundRateLimit_ClientIP_PrefersXForwardedFor(t *testing.T) {
	limiter := &fakeLimiter{allowed: true}
	mw := httpadapter.InboundRateLimitMiddleware(limiter, nil)

	handler := mw(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
	}))

	req := httptest.NewRequest(nethttp.MethodGet, "/x", nil)
	req.RemoteAddr = "10.0.0.1:443" // proxy
	req.Header.Set("X-Forwarded-For", "203.0.113.42, 10.0.0.1")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, "ip:203.0.113.42", limiter.lastBucket(),
		"leftmost X-Forwarded-For entry is the original client")
}

// TestInboundRateLimit_ClientIP_FallsBackToRealIP: X-Real-IP is the
// other common reverse-proxy header. When X-Forwarded-For is absent the
// middleware honors X-Real-IP before falling back to RemoteAddr.
func TestInboundRateLimit_ClientIP_FallsBackToRealIP(t *testing.T) {
	limiter := &fakeLimiter{allowed: true}
	mw := httpadapter.InboundRateLimitMiddleware(limiter, nil)

	handler := mw(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
	}))

	req := httptest.NewRequest(nethttp.MethodGet, "/x", nil)
	req.RemoteAddr = "10.0.0.1:443"
	req.Header.Set("X-Real-IP", "198.51.100.7")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, "ip:198.51.100.7", limiter.lastBucket())
}

// TestInboundRateLimit_ClientIP_StripsPortFromRemoteAddr: no proxy
// header set — the middleware reads RemoteAddr and strips the port
// component so the bucket is a stable per-host key.
func TestInboundRateLimit_ClientIP_StripsPortFromRemoteAddr(t *testing.T) {
	limiter := &fakeLimiter{allowed: true}
	mw := httpadapter.InboundRateLimitMiddleware(limiter, nil)

	handler := mw(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
	}))

	req := httptest.NewRequest(nethttp.MethodGet, "/x", nil)
	req.RemoteAddr = "192.0.2.55:51234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, "ip:192.0.2.55", limiter.lastBucket())
}
