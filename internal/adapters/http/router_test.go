package http_test

import (
	nethttp "net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
)

// TestNewRouter_RegisteredRouteResponds: the most basic contract — a
// route registered on the returned router serves its handler.
func TestNewRouter_RegisteredRouteResponds(t *testing.T) {
	r := httpadapter.NewRouter(httpadapter.Config{})
	r.Get("/ping", func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
		_, _ = w.Write([]byte("pong"))
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := nethttp.Get(srv.URL + "/ping")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, nethttp.StatusOK, resp.StatusCode)
}

// TestNewRouter_RecoversFromPanic: the Recoverer middleware is part of
// the fixed chain. A handler that panics must not crash the server —
// the client sees a 500 instead.
func TestNewRouter_RecoversFromPanic(t *testing.T) {
	r := httpadapter.NewRouter(httpadapter.Config{})
	r.Get("/boom", func(_ nethttp.ResponseWriter, _ *nethttp.Request) {
		panic("kaboom")
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := nethttp.Get(srv.URL + "/boom")
	require.NoError(t, err, "request must not error out — the server stays up")
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, nethttp.StatusInternalServerError, resp.StatusCode)
}

// TestNewRouter_AppliesConfigMiddlewaresInOrder: middlewares supplied via
// Config.Middlewares are applied AFTER Recoverer, in the slice order the
// caller provided. This is the load-bearing contract — the chain order
// is fixed (recover → correlation ID → request log → metrics → inbound
// rate limit → idempotency → handler) and the caller proves it by
// providing the slice in that order.
func TestNewRouter_AppliesConfigMiddlewaresInOrder(t *testing.T) {
	rec := &callRecorder{}

	recorder := func(name string) func(nethttp.Handler) nethttp.Handler {
		return func(next nethttp.Handler) nethttp.Handler {
			return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, req *nethttp.Request) {
				rec.append(name)
				next.ServeHTTP(w, req)
			})
		}
	}

	cfg := httpadapter.Config{
		Middlewares: []func(nethttp.Handler) nethttp.Handler{
			recorder("a"),
			recorder("b"),
			recorder("c"),
		},
	}
	r := httpadapter.NewRouter(cfg)
	r.Get("/x", func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		rec.append("handler")
		w.WriteHeader(nethttp.StatusOK)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := nethttp.Get(srv.URL + "/x")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, nethttp.StatusOK, resp.StatusCode)

	require.Equal(t, []string{"a", "b", "c", "handler"}, rec.snapshot(),
		"middlewares must run in the order supplied, then the handler")
}

// TestNewRouter_NilMiddlewareSlotsAreSkipped: a nil entry in the
// Middlewares slice is silently ignored. Lets cmd/api compose the chain
// from optional dependencies without panicking when one is absent (e.g.
// rate limiter disabled in a unit test).
func TestNewRouter_NilMiddlewareSlotsAreSkipped(t *testing.T) {
	cfg := httpadapter.Config{
		Middlewares: []func(nethttp.Handler) nethttp.Handler{nil, nil, nil},
	}

	require.NotPanics(t, func() {
		r := httpadapter.NewRouter(cfg)
		r.Get("/y", func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			w.WriteHeader(nethttp.StatusOK)
		})

		srv := httptest.NewServer(r)
		defer srv.Close()

		resp, err := nethttp.Get(srv.URL + "/y")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, nethttp.StatusOK, resp.StatusCode)
	})
}

// callRecorder is a tiny thread-safe string slice used to record the
// middleware call order. httptest may serve requests concurrently and
// the recorder is shared across middleware layers, so a mutex is the
// simplest correct primitive — atomic.Value.CompareAndSwap cannot model
// "first store from nil interface" without an extra branch.
type callRecorder struct {
	mu sync.Mutex
	v  []string
}

func (r *callRecorder) append(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.v = append(r.v, s)
}

func (r *callRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.v))
	copy(out, r.v)
	return out
}
