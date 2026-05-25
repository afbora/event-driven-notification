package ports_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/ports"
)

// TestNoopTracer_StartSpanReturnsInputCtx: the no-op tracer must
// hand back the exact ctx it was given so call sites that thread
// ctx through downstream calls do not lose any deadlines, cancel
// chains, or values stamped on it. The Span returned alongside is a
// no-op too — every method runs without panic.
func TestNoopTracer_StartSpanReturnsInputCtx(t *testing.T) {
	type ctxKey struct{}
	parent := context.WithValue(context.Background(), ctxKey{}, "carry-me")

	var tracer ports.Tracer = ports.NoopTracer{}
	ctx, span := tracer.StartSpan(parent, "any-name")

	require.Same(t, parent, ctx,
		"NoopTracer.StartSpan must return the input ctx unchanged so caller chains stay intact")
	require.Equal(t, "carry-me", ctx.Value(ctxKey{}),
		"values on the parent ctx must survive the no-op pass-through")
	require.NotNil(t, span, "even the no-op tracer must return a non-nil Span")
}

// TestNoopTracer_SpanMethodsAreSafe: every Span method (SetStringAttr,
// SetBoolAttr, End) on the no-op span must run silently — call sites
// must be free to invoke them unconditionally without nil-guarding.
// Includes a second End() call to confirm the implementation is
// idempotent (defer-friendly).
func TestNoopTracer_SpanMethodsAreSafe(t *testing.T) {
	_, span := ports.NoopTracer{}.StartSpan(context.Background(), "noop")

	require.NotPanics(t, func() { span.SetStringAttr("key", "value") })
	require.NotPanics(t, func() { span.SetBoolAttr("flag", true) })
	require.NotPanics(t, func() { span.End() })
	require.NotPanics(t, func() { span.End() },
		"a second End() must remain a no-op so defer-style call sites stay safe")
}

// TestNoopTracer_SatisfiesPortInterface is a compile-time assertion
// that NoopTracer implements ports.Tracer. Caught at build time
// today, but pinning it as a test guards against a future interface
// change accidentally dropping the no-op from the implementer set.
func TestNoopTracer_SatisfiesPortInterface(t *testing.T) {
	var _ ports.Tracer = ports.NoopTracer{}
	_ = t // keep the body non-empty so coverage counts this file
}
