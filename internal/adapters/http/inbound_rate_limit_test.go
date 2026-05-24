package http_test

import (
	"context"
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
	mw := httpadapter.InboundRateLimitMiddleware(limiter)

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
	mw := httpadapter.InboundRateLimitMiddleware(limiter)

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

// TestInboundRateLimit_LimiterError_FailsOpen: when the backing store
// errors out (Redis down, network blip), the middleware does not deny —
// it logs the error and lets the request through. Availability beats
// strict enforcement for the inbound limiter.
func TestInboundRateLimit_LimiterError_FailsOpen(t *testing.T) {
	limiter := &fakeLimiter{err: errors.New("redis: connection refused")}
	mw := httpadapter.InboundRateLimitMiddleware(limiter)

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
	mw := httpadapter.InboundRateLimitMiddleware(limiter)

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
	mw := httpadapter.InboundRateLimitMiddleware(limiter)

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
	mw := httpadapter.InboundRateLimitMiddleware(limiter)

	handler := mw(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
	}))

	req := httptest.NewRequest(nethttp.MethodGet, "/x", nil)
	req.RemoteAddr = "192.0.2.55:51234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, "ip:192.0.2.55", limiter.lastBucket())
}
