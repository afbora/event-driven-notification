// Package correlation owns the context key for the end-to-end
// request correlation id (CLAUDE.md §2.3). Every layer that needs to
// read or write the id imports this package — the http middleware
// stashes it on inbound requests, the logger pulls it on every
// emitted record, the worker reads it from queue payloads before
// invoking the use case. Keeping the key in infrastructure rather
// than the http adapter avoids the adapter → infrastructure cycle.
package correlation

import "context"

// key is the unexported context-key type. A dedicated struct type
// prevents collisions with any other package using string keys.
type key struct{}

// FromContext returns the correlation id stashed in ctx by
// WithContext or the http correlation middleware, or "" when no id
// has been set. Total — callers may log the result unconditionally.
func FromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(key{}).(string)
	return id
}

// WithContext returns a context derived from parent that carries id.
// Used by the http correlation middleware on inbound requests and by
// the worker when re-seeding the id from a queue payload.
func WithContext(parent context.Context, id string) context.Context {
	return context.WithValue(parent, key{}, id)
}
