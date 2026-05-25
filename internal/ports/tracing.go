package ports

import "context"

// Tracer is the port the application layer uses to start spans without
// importing an observability SDK directly (CLAUDE.md §3.3 — application
// stays stdlib + ports only). The production adapter lives in
// internal/infrastructure/tracing and wraps OpenTelemetry; tests pass
// a recording fake or the package-level NoopTracer.
//
// The surface is deliberately tiny — just enough for the worker's
// "provider.send" span and the attributes it carries. New attribute
// types (int64, float64, etc.) get a method on Span; new lifecycle
// events get a method on Tracer. Keep it small; OTel's full API is
// not the goal.
type Tracer interface {
	// StartSpan opens a new span named name and returns a ctx that
	// carries it so downstream calls can extend the trace. End must
	// be called on the returned Span — adapters that allocate
	// resources (the OTel adapter does) rely on it for flushing.
	StartSpan(ctx context.Context, name string) (context.Context, Span)
}

// Span is the per-call handle returned by Tracer.StartSpan. Attribute
// setters are typed (no any) so the adapter side never has to switch
// on dynamic types — and so call sites stay self-documenting.
type Span interface {
	// SetStringAttr stamps a string key/value on the span. Safe to call
	// after StartSpan and before End; calls after End are no-ops.
	SetStringAttr(key, value string)

	// SetBoolAttr stamps a boolean key/value on the span. Same lifecycle
	// guarantees as SetStringAttr.
	SetBoolAttr(key string, value bool)

	// End closes the span. Must be called exactly once per StartSpan
	// (defer is the common pattern). Adapter implementations flush
	// timing data here.
	End()
}

// NoopTracer is the zero-cost Tracer used when no real tracing backend
// is wired (tests, dev compose without OTLP endpoint). Every method is
// a no-op so call sites can rely on Tracer being non-nil without a
// guard at every use.
type NoopTracer struct{}

// StartSpan returns the input ctx unchanged and a no-op Span.
func (NoopTracer) StartSpan(ctx context.Context, _ string) (context.Context, Span) {
	return ctx, noopSpan{}
}

// noopSpan satisfies Span without recording anything. Unexported
// because callers should always reach it via NoopTracer.StartSpan.
type noopSpan struct{}

// SetStringAttr is intentionally empty — noopSpan exists so call
// sites can stamp attributes unconditionally when no tracing backend
// is wired; there is nothing to record.
func (noopSpan) SetStringAttr(string, string) {
	// no-op: see type doc.
}

// SetBoolAttr is intentionally empty — see SetStringAttr.
func (noopSpan) SetBoolAttr(string, bool) {
	// no-op: see type doc.
}

// End is intentionally empty — noopSpan owns no resources to flush.
// Safe to call (defer-friendly) and idempotent under repeated calls.
func (noopSpan) End() {
	// no-op: see type doc.
}
