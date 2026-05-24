package correlation_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/infrastructure/correlation"
)

// TestFromContext_MissingReturnsEmpty: the helper is total — callers
// must be able to log "" without a presence check when the middleware
// has not run (or the context was not threaded through).
func TestFromContext_MissingReturnsEmpty(t *testing.T) {
	require.Equal(t, "", correlation.FromContext(context.Background()))
}

// TestWithContext_RoundTrip: WithContext stashes the id; FromContext
// returns it. This is the load-bearing contract every log line and
// queue payload relies on.
func TestWithContext_RoundTrip(t *testing.T) {
	ctx := correlation.WithContext(context.Background(), "01HXYZCORR0001")
	require.Equal(t, "01HXYZCORR0001", correlation.FromContext(ctx))
}

// TestWithContext_NestedOverride: a later WithContext call shadows
// the earlier value within its scope, matching standard
// context.WithValue semantics.
func TestWithContext_NestedOverride(t *testing.T) {
	outer := correlation.WithContext(context.Background(), "outer")
	inner := correlation.WithContext(outer, "inner")
	require.Equal(t, "inner", correlation.FromContext(inner))
	require.Equal(t, "outer", correlation.FromContext(outer))
}
