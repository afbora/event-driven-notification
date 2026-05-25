package http

import (
	"log/slog"
	"math"
	"net"
	nethttp "net/http"
	"strconv"
	"strings"
	"time"

	"github.com/afbora/event-driven-notification/internal/ports"
)

// RateLimitMetricsRecorder is the slim port the inbound rate-limit
// middleware uses to publish the per-endpoint hit counter. Defined here
// (not in internal/ports) because the middleware owns the contract it
// consumes — production wires *infrastructure/metrics.Metrics, tests
// pass a stub or nil. Mirrors the same pattern websocket.Hub uses for
// its client-count gauge.
type RateLimitMetricsRecorder interface {
	InboundRateLimitHit(endpoint string)
}

// InboundRateLimitMiddleware throttles inbound HTTP traffic at a fixed
// budget of 60 requests/minute per client IP (CLAUDE.md §2.6 / §10).
// The middleware delegates the counting itself to ports.RateLimiter so
// production wires the Redis-backed implementation while tests inject
// a deterministic fake.
//
// Bucket key shape: "ip:<addr>". The "ip:" prefix is the inbound
// namespace separator — outbound (per channel) uses "channel:<name>"
// against the same RateLimiter and must not collide (CLAUDE.md §2.6).
//
// On rejection the middleware emits RFC 7807 problem details with a
// Retry-After header carrying the limiter's hint, and (when metrics is
// non-nil) increments inbound_rate_limit_hits_total tagged by URL
// path so dashboards can attribute pressure per endpoint.
//
// Failure policy: when the limiter returns an error (Redis unreachable
// etc.) the middleware logs and lets the request through (fail-open).
// Availability is more valuable than strict enforcement at the inbound
// edge — a brief abuse window beats taking the whole API offline when
// Redis hiccups.
func InboundRateLimitMiddleware(limiter ports.RateLimiter, metrics RateLimitMetricsRecorder) func(nethttp.Handler) nethttp.Handler {
	return func(next nethttp.Handler) nethttp.Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			ip := clientIP(r)
			bucket := "ip:" + ip

			allowed, retryAfter, err := limiter.Allow(r.Context(), bucket)
			if err != nil {
				slog.WarnContext(r.Context(), "inbound rate limiter unavailable, allowing request",
					"bucket", bucket,
					"error", err.Error(),
				)
				next.ServeHTTP(w, r)
				return
			}
			if !allowed {
				respondWithRateLimited(w, r, retryAfter, metrics)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// respondWithRateLimited writes the canonical 429 response: an RFC
// 7807 problem document with a Retry-After header carrying the
// limiter's hint. Setting Retry-After BEFORE WriteProblem is required
// because WriteProblem owns WriteHeader — once it fires, subsequent
// header writes are silently dropped by net/http.
func respondWithRateLimited(w nethttp.ResponseWriter, r *nethttp.Request, retryAfter time.Duration, metrics RateLimitMetricsRecorder) {
	w.Header().Set("Retry-After", strconv.FormatInt(retryAfterSeconds(retryAfter), 10))
	WriteProblem(w, r, Problem{
		Type:   "/probs/rate-limited",
		Title:  "Too Many Requests",
		Status: nethttp.StatusTooManyRequests,
		Detail: "Inbound request rate limit exceeded for this client. Wait the number of seconds in Retry-After before retrying.",
	})
	if metrics != nil {
		metrics.InboundRateLimitHit(r.URL.Path)
	}
}

// retryAfterSeconds converts the limiter's duration hint into a
// positive integer second count. RFC 9110 §10.2.3 requires the value
// to be a non-zero positive integer; a sub-second hint still tells the
// client to wait — round up rather than truncating to zero.
func retryAfterSeconds(d time.Duration) int64 {
	secs := int64(math.Ceil(d.Seconds()))
	if secs < 1 {
		return 1
	}
	return secs
}

// clientIP returns the best guess at the originating client's ip from
// the request. Order of precedence:
//
//  1. X-Forwarded-For — leftmost entry. Behind a reverse proxy this is
//     the address closest to the actual client; the proxy appends its
//     own ip to the right.
//  2. X-Real-IP — the other common reverse-proxy header.
//  3. r.RemoteAddr with the port stripped — the direct connection's
//     remote address when no proxy is in front.
//
// The function is intentionally simple: production deployments behind
// untrusted hops need a configured trust list, but for this
// assessment's compose stack the simple precedence is correct.
func clientIP(r *nethttp.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.IndexByte(xff, ','); idx >= 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
