package tracing

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/afbora/event-driven-notification/internal/ports"
)

// Tracer is the OTel-backed implementation of ports.Tracer. It
// resolves the named tracer from the global OTel provider Setup
// installed at startup — when no exporter is wired, the provider is
// a no-op so spans are dropped on the floor.
//
// Wiring: cmd/api and cmd/worker construct one via NewTracer with the
// package import path of the calling code as the tracer name (the
// OTel convention), then inject it into use cases through their Deps
// struct. The application layer sees only ports.Tracer — it never
// imports go.opentelemetry.io/otel.
type Tracer struct {
	inner trace.Tracer
}

// NewTracer wires an OTel-backed tracer for the named instrumentation
// scope (typically the calling package's import path).
func NewTracer(name string) *Tracer {
	return &Tracer{inner: otel.Tracer(name)}
}

// StartSpan opens a new span on the underlying OTel tracer and
// returns a context that carries it plus a port-shaped Span wrapper.
func (t *Tracer) StartSpan(ctx context.Context, name string) (context.Context, ports.Span) {
	ctx, span := t.inner.Start(ctx, name)
	return ctx, &otelSpan{inner: span}
}

// otelSpan adapts a trace.Span to ports.Span. The attribute setters
// translate the port's typed methods onto OTel's attribute.KeyValue
// shape so the application layer never has to know about either.
type otelSpan struct {
	inner trace.Span
}

func (s *otelSpan) SetStringAttr(key, value string) {
	s.inner.SetAttributes(attribute.String(key, value))
}

func (s *otelSpan) SetBoolAttr(key string, value bool) {
	s.inner.SetAttributes(attribute.Bool(key, value))
}

func (s *otelSpan) End() {
	s.inner.End()
}
