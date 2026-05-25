package http

import (
	"bytes"
	"context"
	"log/slog"
	nethttp "net/http"
	"time"

	"github.com/afbora/event-driven-notification/internal/ports"
)

// idempotencyHeader is the canonical header name the API uses for
// client-supplied idempotency keys (CLAUDE.md §3.9 / §10). The header
// is optional — when absent, the middleware does nothing.
const idempotencyHeader = "Idempotency-Key"

// idempotencyTTL is the default lifetime of a cached response. The
// 24-hour window comes from CLAUDE.md §3.9 — it is long enough to
// absorb client retry storms but short enough that stale state does
// not pile up in Redis forever.
const idempotencyTTL = 24 * time.Hour

// IdempotencyMiddleware caches the response of write requests keyed by
// the client-supplied Idempotency-Key header so a duplicate request
// returns the original response without re-running the handler
// (CLAUDE.md §3.9).
//
// Semantics:
//
//   - No header → pass through; the store is never touched.
//   - Header + cache hit → return cached body and Content-Type with
//     status 200 (intentionally collapses 201/202 to 200 so the client
//     can distinguish "you saw this before" from "we just created
//     this").
//   - Header + cache miss → run the handler with a capturing writer.
//     2xx responses are cached with a 24h ttl; non-2xx are not cached
//     because the result may be transient (5xx) or the client may want
//     to retry with corrections (4xx).
//   - Store Get error → fail-open (run the handler). Availability
//     beats strict enforcement when Redis hiccups.
//   - Store Set error → log and continue; the client still gets the
//     handler's response. The cache will be re-populated on the next
//     attempt.
func IdempotencyMiddleware(store ports.IdempotencyStore) func(nethttp.Handler) nethttp.Handler {
	return func(next nethttp.Handler) nethttp.Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			key := r.Header.Get(idempotencyHeader)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			ctx := r.Context()
			if serveFromIdempotencyCache(ctx, w, store, key) {
				return
			}

			cw := &capturingWriter{ResponseWriter: w, status: nethttp.StatusOK}
			next.ServeHTTP(cw, r)

			cacheIdempotencyResponse(ctx, store, key, cw, w)
		})
	}
}

// serveFromIdempotencyCache writes a cached response when the key was
// seen within the TTL and returns true. On store error it logs and
// returns false (fail-open: availability beats strict enforcement when
// Redis hiccups). A cache miss also returns false so the caller runs
// the handler.
func serveFromIdempotencyCache(ctx context.Context, w nethttp.ResponseWriter, store ports.IdempotencyStore, key string) bool {
	body, contentType, found, err := store.Get(ctx, key)
	if err != nil {
		slog.WarnContext(ctx, "idempotency store unavailable, allowing request",
			"key", key,
			"error", err.Error(),
		)
		return false
	}
	if !found {
		return false
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(nethttp.StatusOK)
	// body comes from a previously captured handler response on a
	// cache hit; it is our own data round-tripped through Redis, not
	// direct client input. gosec's taint analysis cannot see the
	// provenance, so the suppression is documented here.
	if _, werr := w.Write(body); werr != nil { //nolint:gosec // body is our captured handler output, not user-tainted
		slog.WarnContext(ctx, "idempotency replay write failed",
			"key", key,
			"error", werr.Error(),
		)
	}
	return true
}

// cacheIdempotencyResponse persists the captured response when the
// handler succeeded (2xx). Non-2xx responses are intentionally skipped:
// 5xx may be transient and 4xx might be retried with corrections.
func cacheIdempotencyResponse(ctx context.Context, store ports.IdempotencyStore, key string, cw *capturingWriter, w nethttp.ResponseWriter) {
	if cw.status < 200 || cw.status >= 300 {
		return
	}
	if err := store.Set(ctx, key, cw.body.Bytes(), w.Header().Get("Content-Type"), idempotencyTTL); err != nil {
		slog.WarnContext(ctx, "idempotency cache write failed",
			"key", key,
			"error", err.Error(),
		)
	}
}

// capturingWriter is a thin http.ResponseWriter wrapper that mirrors
// every write to a private buffer so the middleware can cache the
// final body. The wrapper also captures the status code — handlers
// that never call WriteHeader implicitly emit 200 (matches stdlib
// behavior), which we preserve as the default.
type capturingWriter struct {
	nethttp.ResponseWriter
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

// WriteHeader records the status and forwards the call. Repeated
// invocations are a stdlib bug pattern; we suppress the second
// forward but keep the first status (chi's middleware.Recoverer does
// the same).
func (c *capturingWriter) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}
	c.status = status
	c.wroteHeader = true
	c.ResponseWriter.WriteHeader(status)
}

// Write tees the payload into the buffer and the underlying writer.
// stdlib's ResponseWriter implicitly calls WriteHeader(200) on the
// first Write — we mirror that so capturing handlers that skip
// WriteHeader still produce a captured status.
func (c *capturingWriter) Write(p []byte) (int, error) {
	if !c.wroteHeader {
		c.wroteHeader = true
	}
	c.body.Write(p)
	return c.ResponseWriter.Write(p)
}
