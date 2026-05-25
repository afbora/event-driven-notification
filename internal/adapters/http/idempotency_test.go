package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// fakeIdempotencyStore is an in-memory ports.IdempotencyStore for the
// middleware tests — deterministic, lets the test choose what Get/Set
// return, and records every interaction so assertions can inspect the
// keys the middleware operated on.
type fakeIdempotencyStore struct {
	mu sync.Mutex

	// preloaded entries keyed by the raw client key (no prefix — the
	// middleware passes the raw key; the redis adapter is the layer
	// that applies the "idempotency:" namespace).
	entries map[string]ports.IdempotencyEntry

	getErr error
	setErr error

	getCalls []string
	setCalls []setCall
}

type setCall struct {
	key   string
	entry ports.IdempotencyEntry
	ttl   time.Duration
}

func newFakeIdempotencyStore() *fakeIdempotencyStore {
	return &fakeIdempotencyStore{entries: map[string]ports.IdempotencyEntry{}}
}

func (f *fakeIdempotencyStore) Get(_ context.Context, key string) (ports.IdempotencyEntry, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls = append(f.getCalls, key)
	if f.getErr != nil {
		return ports.IdempotencyEntry{}, false, f.getErr
	}
	e, ok := f.entries[key]
	if !ok {
		return ports.IdempotencyEntry{}, false, nil
	}
	return e, true, nil
}

func (f *fakeIdempotencyStore) Set(_ context.Context, key string, entry ports.IdempotencyEntry, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls = append(f.setCalls, setCall{key: key, entry: entry, ttl: ttl})
	if f.setErr != nil {
		return f.setErr
	}
	f.entries[key] = entry
	return nil
}

// TestIdempotency_NoHeader_PassesThrough: when the client omits
// Idempotency-Key the middleware does nothing — handler runs, store is
// untouched.
func TestIdempotency_NoHeader_PassesThrough(t *testing.T) {
	store := newFakeIdempotencyStore()
	mw := httpadapter.IdempotencyMiddleware(store)

	called := false
	handler := mw(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		called = true
		w.WriteHeader(nethttp.StatusCreated)
	}))

	req := httptest.NewRequest(nethttp.MethodPost, "/x", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.True(t, called)
	require.Equal(t, nethttp.StatusCreated, rr.Code)
	require.Empty(t, store.getCalls, "store must not be queried when no header is set")
	require.Empty(t, store.setCalls, "store must not be written when no header is set")
}

// TestIdempotency_CacheMiss_CapturesAndStores2xx: first time we see a
// key, the handler runs, the response is captured, and a successful
// (2xx) response is written to the store with a 24h ttl.
func TestIdempotency_CacheMiss_CapturesAndStores2xx(t *testing.T) {
	store := newFakeIdempotencyStore()
	mw := httpadapter.IdempotencyMiddleware(store)

	handler := mw(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(nethttp.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"abc"}`))
	}))

	req := httptest.NewRequest(nethttp.MethodPost, "/x", nil)
	req.Header.Set("Idempotency-Key", "key-1")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusCreated, rr.Code, "first call passes through unchanged")
	require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	require.Equal(t, `{"id":"abc"}`, rr.Body.String())

	require.Equal(t, []string{"key-1"}, store.getCalls)
	require.Len(t, store.setCalls, 1)
	require.Equal(t, "key-1", store.setCalls[0].key)
	require.Equal(t, []byte(`{"id":"abc"}`), store.setCalls[0].entry.Body)
	require.Equal(t, "application/json", store.setCalls[0].entry.ContentType)
	require.NotEmpty(t, store.setCalls[0].entry.RequestHash,
		"the captured entry must carry the request body hash so a future divergent payload is 409'd")
	require.Equal(t, 24*time.Hour, store.setCalls[0].ttl)
}

// TestIdempotency_CacheHit_ReplaysAs200_HandlerSkipped: a repeat
// request with a known key returns the cached body and content-type
// with status 200 (CLAUDE.md §3.9 — the replay collapses 201/202 to
// 200 so the client distinguishes "you saw this before"), and the
// downstream handler never runs.
func TestIdempotency_CacheHit_ReplaysAs200_HandlerSkipped(t *testing.T) {
	store := newFakeIdempotencyStore()
	// Preloaded entry has no RequestHash — the legacy fallback path
	// (older deployments without fingerprinting). The middleware
	// recognizes the empty hash and replays, never 409s.
	store.entries["dup-key"] = ports.IdempotencyEntry{
		Body:        []byte(`{"id":"cached"}`),
		ContentType: "application/json",
	}
	mw := httpadapter.IdempotencyMiddleware(store)

	called := false
	handler := mw(nethttp.HandlerFunc(func(_ nethttp.ResponseWriter, _ *nethttp.Request) {
		called = true
	}))

	req := httptest.NewRequest(nethttp.MethodPost, "/x", nil)
	req.Header.Set("Idempotency-Key", "dup-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.False(t, called, "cache hit must short-circuit the handler")
	require.Equal(t, nethttp.StatusOK, rr.Code, "replay status must be 200, never 201/202")
	require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	require.Equal(t, `{"id":"cached"}`, rr.Body.String())
}

// TestIdempotency_StoreGetError_FailsOpen: a store outage during the
// Get lookup must not block the request — the middleware falls through
// to the handler. The handler's response is still cached on success so
// the next attempt benefits.
func TestIdempotency_StoreGetError_FailsOpen(t *testing.T) {
	store := newFakeIdempotencyStore()
	store.getErr = errors.New("redis: connection refused")
	mw := httpadapter.IdempotencyMiddleware(store)

	called := false
	handler := mw(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		called = true
		w.WriteHeader(nethttp.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))

	req := httptest.NewRequest(nethttp.MethodPost, "/x", nil)
	req.Header.Set("Idempotency-Key", "key-err")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.True(t, called, "fail-open: handler must run when Get errors")
	require.Equal(t, nethttp.StatusOK, rr.Code)
}

// TestIdempotency_NonSuccessNotCached: 4xx and 5xx responses are NOT
// cached. A subsequent request with the same key gets a fresh attempt
// — caching a 500 would punish the client for an upstream transient.
func TestIdempotency_NonSuccessNotCached(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{"client error 400", nethttp.StatusBadRequest},
		{"server error 500", nethttp.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeIdempotencyStore()
			mw := httpadapter.IdempotencyMiddleware(store)

			handler := mw(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
				w.WriteHeader(tc.status)
			}))

			req := httptest.NewRequest(nethttp.MethodPost, "/x", nil)
			req.Header.Set("Idempotency-Key", "no-cache-key")
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			require.Equal(t, tc.status, rr.Code)
			require.Empty(t, store.setCalls, "non-2xx responses must not be cached")
		})
	}
}

// TestIdempotency_SameKey_DifferentBody_Returns409Conflict: CLAUDE.md
// §3.9 calls Idempotency-Key the contract for "same intent, same
// outcome." Reusing a key with a *different* request body is a client
// bug — two distinct intents collide on one key — and the API must
// surface it as RFC 7807 409 Conflict, not silently replay the first
// response (which would mask the bug AND hide the second intent's
// payload from any audit).
//
// The middleware runs the handler twice on the same fake store: first
// with body A (cache miss → handler runs, response cached), then with
// body B + the same key (cache hit BUT bodies disagree → 409). The
// handler must NOT run on the second call — the conflict is the
// terminal response, the inner handler never sees it.
func TestIdempotency_SameKey_DifferentBody_Returns409Conflict(t *testing.T) {
	store := newFakeIdempotencyStore()
	mw := httpadapter.IdempotencyMiddleware(store)

	handlerCalls := 0
	handler := mw(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		handlerCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(nethttp.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"first"}`))
	}))

	// --- First POST: body A, cache miss, handler runs, response stored.
	bodyA := []byte(`{"channel":"sms","content":"A"}`)
	req1 := httptest.NewRequest(nethttp.MethodPost, "/x", bytes.NewReader(bodyA))
	req1.Header.Set("Idempotency-Key", "dup-key")
	req1.Header.Set("Content-Type", "application/json")
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req1)
	require.Equal(t, nethttp.StatusAccepted, rr1.Code, "first call must succeed (cache miss)")
	require.Equal(t, 1, handlerCalls, "first call must run the handler exactly once")

	// --- Second POST: body B, same key, cache hit with mismatched body.
	bodyB := []byte(`{"channel":"sms","content":"B"}`)
	req2 := httptest.NewRequest(nethttp.MethodPost, "/x", bytes.NewReader(bodyB))
	req2.Header.Set("Idempotency-Key", "dup-key")
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)

	require.Equal(t, nethttp.StatusConflict, rr2.Code,
		"different body under the same idempotency key must be 409 Conflict; got status=%d body=%s",
		rr2.Code, rr2.Body.String())
	require.Equal(t, 1, handlerCalls,
		"handler must NOT run on a key/body conflict — the 409 is terminal")
	require.Equal(t, "application/problem+json", rr2.Header().Get("Content-Type"),
		"RFC 7807 mandates application/problem+json")

	var prob httpadapter.Problem
	require.NoError(t, json.Unmarshal(rr2.Body.Bytes(), &prob),
		"body must be parseable RFC 7807 Problem: %s", rr2.Body.String())
	require.Equal(t, nethttp.StatusConflict, prob.Status, "Problem.status must mirror the HTTP status")
	require.NotEmpty(t, prob.Title, "Problem.title is required by RFC 7807")
	require.NotEmpty(t, prob.Type, "Problem.type should be set (we use /probs/idempotency-key-mismatch)")
}

// TestIdempotency_SameKey_SameBody_StillReplays: the matching-body path
// keeps the existing replay semantics — 200 + cached body — even after
// the conflict-detection logic lands. Guards against an over-eager fix
// that 409s every cache hit.
func TestIdempotency_SameKey_SameBody_StillReplays(t *testing.T) {
	store := newFakeIdempotencyStore()
	mw := httpadapter.IdempotencyMiddleware(store)

	handlerCalls := 0
	handler := mw(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		handlerCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(nethttp.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"first"}`))
	}))

	body := []byte(`{"channel":"sms","content":"same"}`)

	req1 := httptest.NewRequest(nethttp.MethodPost, "/x", bytes.NewReader(body))
	req1.Header.Set("Idempotency-Key", "match-key")
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req1)
	require.Equal(t, nethttp.StatusAccepted, rr1.Code)
	require.Equal(t, 1, handlerCalls)

	req2 := httptest.NewRequest(nethttp.MethodPost, "/x", bytes.NewReader(body))
	req2.Header.Set("Idempotency-Key", "match-key")
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)

	require.Equal(t, nethttp.StatusOK, rr2.Code, "matching body must still replay as 200")
	require.Equal(t, 1, handlerCalls, "matching replay must not re-invoke the handler")
	require.Equal(t, `{"id":"first"}`, rr2.Body.String(), "replay body must match first response")
}

// TestIdempotency_LegacyEntryWithoutHash_StillReplays: an entry that
// pre-dates the body-fingerprint fix carries no RequestHash. The
// middleware must recognize the legacy shape (len(hash)==0) and fall
// back to the pre-fingerprint replay behavior — otherwise the upgrade
// would 409 every in-flight cache entry written before the fix.
func TestIdempotency_LegacyEntryWithoutHash_StillReplays(t *testing.T) {
	store := newFakeIdempotencyStore()
	store.entries["legacy-key"] = ports.IdempotencyEntry{
		Body:        []byte(`{"id":"legacy"}`),
		ContentType: "application/json",
		// RequestHash deliberately nil.
	}
	mw := httpadapter.IdempotencyMiddleware(store)

	called := false
	handler := mw(nethttp.HandlerFunc(func(_ nethttp.ResponseWriter, _ *nethttp.Request) {
		called = true
	}))

	req := httptest.NewRequest(nethttp.MethodPost, "/x",
		bytes.NewReader([]byte(`{"any":"body"}`)))
	req.Header.Set("Idempotency-Key", "legacy-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.False(t, called, "legacy replay must still short-circuit the handler")
	require.Equal(t, nethttp.StatusOK, rr.Code, "legacy entries replay as 200, not 409")
	require.Equal(t, `{"id":"legacy"}`, rr.Body.String())
}

// TestIdempotency_OversizedBody_Returns413: a body above the
// fingerprint cap (1 MiB) is rejected with RFC 7807 413 before the
// handler runs. Hashing megabytes of client-controlled input on every
// duplicate-detection check would be a DoS amplifier.
func TestIdempotency_OversizedBody_Returns413(t *testing.T) {
	store := newFakeIdempotencyStore()
	mw := httpadapter.IdempotencyMiddleware(store)

	called := false
	handler := mw(nethttp.HandlerFunc(func(_ nethttp.ResponseWriter, _ *nethttp.Request) {
		called = true
	}))

	// 1 MiB + 1 byte — exactly one byte past the cap.
	oversized := bytes.Repeat([]byte{'a'}, (1<<20)+1)
	req := httptest.NewRequest(nethttp.MethodPost, "/x", bytes.NewReader(oversized))
	req.Header.Set("Idempotency-Key", "big-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusRequestEntityTooLarge, rr.Code,
		"body above the cap must be 413 Payload Too Large; got %d", rr.Code)
	require.False(t, called, "oversized body must short-circuit before the handler runs")
	require.Equal(t, "application/problem+json", rr.Header().Get("Content-Type"))
	require.Empty(t, store.setCalls, "no cache write on the rejection path")
}

// TestIdempotency_CapturedBodyMatchesClientView: end-to-end check that
// the body cached on miss equals the body the client received — a
// regression guard against the response writer wrapper accidentally
// double-writing or truncating.
func TestIdempotency_CapturedBodyMatchesClientView(t *testing.T) {
	store := newFakeIdempotencyStore()
	mw := httpadapter.IdempotencyMiddleware(store)

	payload := []byte(`{"items":[1,2,3,4,5],"ok":true}`)
	handler := mw(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(nethttp.StatusAccepted)
		_, _ = w.Write(payload)
	}))

	req := httptest.NewRequest(nethttp.MethodPost, "/x", nil)
	req.Header.Set("Idempotency-Key", "match-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	clientBody, err := io.ReadAll(rr.Body)
	require.NoError(t, err)
	require.Equal(t, payload, clientBody)

	require.Len(t, store.setCalls, 1)
	require.Equal(t, payload, store.setCalls[0].entry.Body)
	require.Equal(t, "application/json; charset=utf-8", store.setCalls[0].entry.ContentType)
}
