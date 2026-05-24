package http

import (
	"fmt"
	"log/slog"
	"math"
	"net"
	nethttp "net/http"
	"strings"

	"github.com/afbora/event-driven-notification/internal/ports"
)

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
// Failure policy: when the limiter returns an error (Redis unreachable
// etc.) the middleware logs and lets the request through (fail-open).
// Availability is more valuable than strict enforcement at the inbound
// edge — a brief abuse window beats taking the whole API offline when
// Redis hiccups.
func InboundRateLimitMiddleware(limiter ports.RateLimiter) func(nethttp.Handler) nethttp.Handler {
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
				// Round up so a sub-second hint still tells the client to wait.
				retrySecs := int64(math.Ceil(retryAfter.Seconds()))
				if retrySecs < 1 {
					retrySecs = 1
				}
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retrySecs))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(nethttp.StatusTooManyRequests)
				// Body is a minimal JSON envelope; task 5 swaps this for the
				// RFC 7807 problem details translator. The bucket is not
				// echoed — it would tell the caller the limiter's internal
				// namespacing for no useful purpose.
				_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}`))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
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
