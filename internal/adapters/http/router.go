// Package http is the HTTP adapter — the chi-based router that fronts
// the application use cases. It owns three responsibilities:
//
//  1. Compose the fixed middleware chain (recover → correlation ID →
//     request log → metrics → inbound rate limit → idempotency →
//     handler) in the order mandated by CLAUDE.md §3 / §10.
//  2. Mount the HTTP handlers that translate REST calls into use case
//     invocations and translate domain errors into RFC 7807 problem
//     responses.
//  3. Mount auxiliary endpoints — health, /metrics, Swagger UI, the
//     WebSocket upgrade endpoint.
//
// The package name collides with stdlib net/http; callers import this
// package aliased as httpadapter and the stdlib as nethttp when both
// are needed in the same file. Inside this package the stdlib is
// aliased; chi handlers themselves are stdlib-compatible.
package http

import (
	nethttp "net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Config wires the optional middlewares the chain composes after the
// always-on Recoverer. The Middlewares slice is applied in order, so
// the caller (cmd/api) is responsible for preserving the canonical chain:
//
//	recover (always first, wired by NewRouter)
//	  → correlation ID
//	  → request log
//	  → metrics
//	  → inbound rate limit
//	  → idempotency
//	  → handler
//
// nil entries are silently skipped — tests and reduced-stack composes
// pass a partial chain without panicking. Tasks 2-4 in phase 4 add the
// concrete middleware constructors that fill the slots.
type Config struct {
	// Middlewares is the ordered list applied after Recoverer. Each
	// entry has the standard func(http.Handler) http.Handler shape so
	// it can come from chi/middleware, this package, or a third-party.
	Middlewares []func(nethttp.Handler) nethttp.Handler
}

// NewRouter returns a chi.Mux with Recoverer wired first and the
// configured middlewares appended in order. Callers register routes on
// the returned mux via Get/Post/Mount/Route — chi's behavior is
// unchanged, this constructor only fixes the chain order.
//
// Recoverer is always present so a panic in any handler degrades to a
// 500 instead of taking down the API process — the worker fleet and
// reconciler continue running.
func NewRouter(cfg Config) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	for _, mw := range cfg.Middlewares {
		if mw == nil {
			continue
		}
		r.Use(mw)
	}
	return r
}
