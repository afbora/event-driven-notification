package http

import (
	"context"
	nethttp "net/http"

	"github.com/afbora/event-driven-notification/internal/infrastructure/correlation"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// correlationIDHeader is the canonical header name carrying a request's
// end-to-end correlation id (CLAUDE.md §2.3 / §10). The server reads it
// from the inbound request, falls back to generation when absent or
// blank, and echoes the resolved value in the response so the client
// can correlate API calls with its own traces.
const correlationIDHeader = "X-Correlation-ID"

// CorrelationIDMiddleware reads X-Correlation-ID from the inbound
// request, generates a new id via gen when the header is missing or
// blank, stashes the resolved id in the request context, and echoes it
// in the response header. Downstream handlers retrieve it via
// CorrelationIDFromContext.
//
// The middleware deliberately takes the full ports.IDGenerator rather
// than a narrower factory function — the same generator is wired into
// the application use cases (CreateNotification, ProcessNotification,
// ...) so the http adapter shares it instead of constructing a
// duplicate.
func CorrelationIDMiddleware(gen ports.IDGenerator) func(nethttp.Handler) nethttp.Handler {
	return func(next nethttp.Handler) nethttp.Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			id := r.Header.Get(correlationIDHeader)
			if id == "" {
				id = gen.NewCorrelationID()
			}

			w.Header().Set(correlationIDHeader, id)
			next.ServeHTTP(w, r.WithContext(correlation.WithContext(r.Context(), id)))
		})
	}
}

// CorrelationIDFromContext is a thin re-export of
// correlation.FromContext kept for backward compatibility with
// existing call sites in this adapter. New code should reach for the
// infrastructure package directly.
func CorrelationIDFromContext(ctx context.Context) string {
	return correlation.FromContext(ctx)
}

// ContextWithCorrelationID is the matching re-export of
// correlation.WithContext.
func ContextWithCorrelationID(parent context.Context, id string) context.Context {
	return correlation.WithContext(parent, id)
}
