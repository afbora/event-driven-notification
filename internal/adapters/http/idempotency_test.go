package http_test

import (
	"context"
	"errors"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
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
	entries map[string]idempotencyEntry

	getErr error
	setErr error

	getCalls []string
	setCalls []setCall
}

type idempotencyEntry struct {
	body        []byte
	contentType string
}

type setCall struct {
	key         string
	body        []byte
	contentType string
	ttl         time.Duration
}

func newFakeIdempotencyStore() *fakeIdempotencyStore {
	return &fakeIdempotencyStore{entries: map[string]idempotencyEntry{}}
}

func (f *fakeIdempotencyStore) Get(_ context.Context, key string) ([]byte, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls = append(f.getCalls, key)
	if f.getErr != nil {
		return nil, "", false, f.getErr
	}
	e, ok := f.entries[key]
	if !ok {
		return nil, "", false, nil
	}
	return e.body, e.contentType, true, nil
}

func (f *fakeIdempotencyStore) Set(_ context.Context, key string, body []byte, contentType string, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls = append(f.setCalls, setCall{key: key, body: body, contentType: contentType, ttl: ttl})
	if f.setErr != nil {
		return f.setErr
	}
	f.entries[key] = idempotencyEntry{body: body, contentType: contentType}
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
	require.Equal(t, []byte(`{"id":"abc"}`), store.setCalls[0].body)
	require.Equal(t, "application/json", store.setCalls[0].contentType)
	require.Equal(t, 24*time.Hour, store.setCalls[0].ttl)
}

// TestIdempotency_CacheHit_ReplaysAs200_HandlerSkipped: a repeat
// request with a known key returns the cached body and content-type
// with status 200 (CLAUDE.md §3.9 — the replay collapses 201/202 to
// 200 so the client distinguishes "you saw this before"), and the
// downstream handler never runs.
func TestIdempotency_CacheHit_ReplaysAs200_HandlerSkipped(t *testing.T) {
	store := newFakeIdempotencyStore()
	store.entries["dup-key"] = idempotencyEntry{
		body:        []byte(`{"id":"cached"}`),
		contentType: "application/json",
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
	require.Equal(t, payload, store.setCalls[0].body)
	require.Equal(t, "application/json; charset=utf-8", store.setCalls[0].contentType)
}
