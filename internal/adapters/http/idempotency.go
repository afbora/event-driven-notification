package http

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
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

// maxIdempotencyBodyBytes caps how much of the request body the
// middleware will buffer for fingerprinting. A real notification
// payload is well under 1 KiB; the 1 MiB ceiling exists purely as a
// defense against a maliciously oversized POST that would otherwise
// pin memory while we hash it.
//
// Requests exceeding the cap are rejected with RFC 7807 413 Payload
// Too Large before the handler runs — this is consistent with
// CLAUDE.md §3.5 (errors at the edge are typed problem responses).
const maxIdempotencyBodyBytes = 1 << 20

// errIdempotencyBodyTooLarge sentinels the oversized-body path so
// readAndBufferRequestBody's caller can branch on it without string
// comparisons (CLAUDE.md §3.5 forbids string compare on errors).
var errIdempotencyBodyTooLarge = errors.New("request body exceeds idempotency limit")

// IdempotencyMiddleware caches the response of write requests keyed by
// the client-supplied Idempotency-Key header so a duplicate request
// returns the original response without re-running the handler
// (CLAUDE.md §3.9).
//
// Semantics:
//
//   - No header → pass through; the store is never touched.
//   - Header + cache hit with matching body fingerprint → return cached
//     body and Content-Type with status 200 (intentionally collapses
//     201/202 to 200 so the client can distinguish "you saw this
//     before" from "we just created this").
//   - Header + cache hit with *different* body → RFC 7807 409 Conflict.
//     Reusing a key with a divergent payload is a client bug — two
//     intents collided on one key — and surfacing it loudly beats
//     silently replaying the first response.
//   - Header + cache hit on a legacy entry (no recorded body
//     fingerprint, e.g. written by an older deployment) → replay as
//     before. The upgrade path does not break in-flight cache entries.
//   - Header + cache miss → run the handler with a capturing writer.
//     2xx responses are cached with the request body fingerprint and
//     a 24h ttl; non-2xx are not cached because the result may be
//     transient (5xx) or the client may want to retry with corrections
//     (4xx).
//   - Store Get error → fail-open (run the handler). Availability
//     beats strict enforcement when Redis hiccups.
//   - Store Set error → log and continue; the client still gets the
//     handler's response. The cache will be re-populated on the next
//     attempt.
func IdempotencyMiddleware(store ports.IdempotencyStore) func(nethttp.Handler) nethttp.Handler {
	return func(next nethttp.Handler) nethttp.Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			serveIdempotent(w, r, next, store)
		})
	}
}

// serveIdempotent owns the per-request branching for the idempotency
// middleware. Extracted from IdempotencyMiddleware so the cognitive
// complexity of the public constructor (which is mostly closure
// scaffolding) stays under Sonar's S3776 threshold — the constructor
// returns a returned-from-a-returned func, which compounds every
// branch's nesting penalty when measured at the top.
func serveIdempotent(w nethttp.ResponseWriter, r *nethttp.Request, next nethttp.Handler, store ports.IdempotencyStore) {
	key := r.Header.Get(idempotencyHeader)
	if key == "" {
		next.ServeHTTP(w, r)
		return
	}

	bodyBytes, err := readAndBufferRequestBody(r)
	if err != nil {
		respondToBodyReadError(w, r, err)
		return
	}
	requestHash := hashRequestBody(bodyBytes)

	ctx := r.Context()
	entry, found, err := store.Get(ctx, key)
	if err != nil {
		slog.WarnContext(ctx, "idempotency store unavailable, allowing request",
			"key", key,
			"error", err.Error(),
		)
		runAndCache(ctx, w, r, next, store, key, requestHash)
		return
	}
	if found {
		dispatchCacheHit(ctx, w, r, key, entry, requestHash)
		return
	}

	runAndCache(ctx, w, r, next, store, key, requestHash)
}

// dispatchCacheHit branches on whether the stored entry's request
// fingerprint matches the current request's. A mismatched fingerprint
// is the "same key, different intent" client bug; everything else
// (matching fingerprint or a legacy entry without a stored hash)
// falls through to the canonical replay path.
func dispatchCacheHit(ctx context.Context, w nethttp.ResponseWriter, r *nethttp.Request, key string, entry ports.IdempotencyEntry, requestHash []byte) {
	if requestHashMismatched(entry.RequestHash, requestHash) {
		respondWithKeyMismatch(w, r)
		return
	}
	replayCachedEntry(ctx, w, key, entry)
}

// requestHashMismatched returns true when the stored fingerprint is
// non-empty AND does not match the current request's. Empty stored
// fingerprints (legacy entries written before the body-hash field
// landed) intentionally return false so the upgrade path keeps
// replaying them.
func requestHashMismatched(stored, current []byte) bool {
	return len(stored) > 0 && !bytes.Equal(stored, current)
}

// readAndBufferRequestBody drains r.Body up to the configured ceiling,
// re-installs a fresh reader so downstream handlers still see the full
// payload, and returns the buffered bytes. A nil body is treated as an
// empty payload (matches stdlib semantics for GET-like requests that
// somehow carry an Idempotency-Key header).
//
// On EOF / read error the buffered bytes so far are discarded and
// errIdempotencyBodyTooLarge / the underlying error is returned.
func readAndBufferRequestBody(r *nethttp.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer func() { _ = r.Body.Close() }()

	// Read one byte past the limit so we can distinguish "exactly at
	// the cap" (allowed) from "over the cap" (reject).
	limited := io.LimitReader(r.Body, maxIdempotencyBodyBytes+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if int64(len(buf)) > maxIdempotencyBodyBytes {
		return nil, errIdempotencyBodyTooLarge
	}
	r.Body = io.NopCloser(bytes.NewReader(buf))
	return buf, nil
}

// hashRequestBody returns the SHA-256 fingerprint of the supplied body
// bytes. SHA-256 is overkill for collision avoidance at the volumes the
// idempotency cache sees, but it is the standard "non-weak hash" pick
// and avoids any flags around MD5/SHA1.
func hashRequestBody(body []byte) []byte {
	sum := sha256.Sum256(body)
	return sum[:]
}

// respondToBodyReadError translates a request-body failure into an
// RFC 7807 problem response. Oversized bodies are 413; everything else
// is a generic 400 (the underlying read failure is logged but never
// echoed to the client).
func respondToBodyReadError(w nethttp.ResponseWriter, r *nethttp.Request, err error) {
	if errors.Is(err, errIdempotencyBodyTooLarge) {
		WriteProblem(w, r, Problem{
			Type:   "/probs/payload-too-large",
			Title:  "Payload Too Large",
			Status: nethttp.StatusRequestEntityTooLarge,
			Detail: fmt.Sprintf("Request body exceeds the %d-byte idempotency cache limit.", maxIdempotencyBodyBytes),
		})
		return
	}
	slog.WarnContext(r.Context(), "idempotency middleware could not buffer request body",
		"error", err.Error(),
	)
	WriteProblem(w, r, Problem{
		Type:   "/probs/bad-request",
		Title:  "Bad Request",
		Status: nethttp.StatusBadRequest,
		Detail: "Request body could not be read.",
	})
}

// respondWithKeyMismatch emits the 409 Conflict response a key/body
// collision deserves. Wording is deliberately client-actionable — it
// names the cause and suggests the fix without echoing either body.
func respondWithKeyMismatch(w nethttp.ResponseWriter, r *nethttp.Request) {
	WriteProblem(w, r, Problem{
		Type:   "/probs/idempotency-key-mismatch",
		Title:  "Idempotency Key Conflict",
		Status: nethttp.StatusConflict,
		Detail: "An Idempotency-Key was reused with a different request body. Use a fresh key for the new payload.",
	})
}

// replayCachedEntry writes the previously captured response with the
// canonical replay status (200). Body comes from our own cache, not
// the current client, so it is safe to write verbatim.
func replayCachedEntry(ctx context.Context, w nethttp.ResponseWriter, key string, entry ports.IdempotencyEntry) {
	if entry.ContentType != "" {
		w.Header().Set("Content-Type", entry.ContentType)
	}
	w.WriteHeader(nethttp.StatusOK)
	// Body is our own captured handler output round-tripped through
	// Redis, not client input. gosec's taint analysis cannot see the
	// provenance, so the suppression is documented here.
	if _, werr := w.Write(entry.Body); werr != nil { //nolint:gosec // body is our captured handler output, not user-tainted
		slog.WarnContext(ctx, "idempotency replay write failed",
			"key", key,
			"error", werr.Error(),
		)
	}
}

// runAndCache executes the downstream handler with a capturing writer
// and persists the response (with the request body fingerprint) on a
// 2xx outcome. Non-2xx responses are not cached because the result may
// be transient (5xx) or the client may want to retry with corrections
// (4xx).
func runAndCache(ctx context.Context, w nethttp.ResponseWriter, r *nethttp.Request, next nethttp.Handler, store ports.IdempotencyStore, key string, requestHash []byte) {
	cw := &capturingWriter{ResponseWriter: w, status: nethttp.StatusOK}
	next.ServeHTTP(cw, r)

	if cw.status < 200 || cw.status >= 300 {
		return
	}
	entry := ports.IdempotencyEntry{
		Body:        cw.body.Bytes(),
		ContentType: w.Header().Get("Content-Type"),
		RequestHash: requestHash,
	}
	if err := store.Set(ctx, key, entry, idempotencyTTL); err != nil {
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

// Hijack forwards to the underlying ResponseWriter's Hijack method
// when it implements http.Hijacker. The idempotency middleware
// short-circuits on missing Idempotency-Key header, but for safety
// any request that does flow through capturingWriter must still be
// upgradable to a WebSocket (or chunked transfer) — the embedded
// ResponseWriter interface does not promote Hijack to the wrapper.
func (c *capturingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := c.ResponseWriter.(nethttp.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return h.Hijack()
}

// Flush forwards to the underlying ResponseWriter's Flush method when
// it implements http.Flusher. Required for streaming response bodies
// (server-sent events, chunked transfer).
func (c *capturingWriter) Flush() {
	if f, ok := c.ResponseWriter.(nethttp.Flusher); ok {
		f.Flush()
	}
}
