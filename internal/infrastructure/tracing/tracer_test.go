package tracing_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/afbora/event-driven-notification/internal/infrastructure/tracing"
)

// installRecorder swaps the global TracerProvider for one whose only
// SpanProcessor is a tracetest.SpanRecorder, captures every span
// produced during the test, and restores the previous provider on
// cleanup so other tests in the package stay isolated.
func installRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return recorder
}

// TestTracer_StartSpan_RecordsNameThroughGlobalProvider: the adapter
// resolves its tracer from the global OTel provider — the same one
// tracing.Setup installs in production. With a SpanRecorder swapped
// in, a span opened via the adapter must appear with its name in
// the recorder's snapshot, and the returned ctx must carry the live
// span (so downstream tracer.Start calls would link to it).
func TestTracer_StartSpan_RecordsNameThroughGlobalProvider(t *testing.T) {
	recorder := installRecorder(t)

	tr := tracing.NewTracer("test-scope")
	ctx, span := tr.StartSpan(context.Background(), "outer.work")
	require.NotNil(t, ctx, "StartSpan must return a non-nil ctx")
	require.NotNil(t, span, "StartSpan must return a non-nil Span")

	span.End()

	ended := recorder.Ended()
	require.Len(t, ended, 1, "exactly one span must have been recorded")
	require.Equal(t, "outer.work", ended[0].Name())
	require.Equal(t, "test-scope", ended[0].InstrumentationScope().Name,
		"the adapter must register the instrumentation scope it was constructed with")
}

// TestTracer_SetAttributes_ReachSpan: the typed port setters
// (SetStringAttr / SetBoolAttr) must translate to OTel
// attribute.KeyValue entries on the underlying span. Recorder
// inspection confirms key, value, and type for each attribute.
func TestTracer_SetAttributes_ReachSpan(t *testing.T) {
	recorder := installRecorder(t)

	_, span := tracing.NewTracer("test-scope").StartSpan(context.Background(), "attrs")
	span.SetStringAttr("notification.id", "01HX")
	span.SetBoolAttr("provider.success", true)
	span.End()

	ended := recorder.Ended()
	require.Len(t, ended, 1)

	attrs := map[string]any{}
	for _, kv := range ended[0].Attributes() {
		attrs[string(kv.Key)] = kv.Value.AsInterface()
	}

	require.Equal(t, "01HX", attrs["notification.id"],
		"SetStringAttr must surface as a string attribute on the span")
	require.Equal(t, true, attrs["provider.success"],
		"SetBoolAttr must surface as a bool attribute on the span")
}

// TestTracer_End_IsIdempotent: a second End() call on a finished
// span must not produce a second recording. Guards against future
// refactors that might accidentally re-end a span via defer when the
// caller already ended it explicitly.
func TestTracer_End_IsIdempotent(t *testing.T) {
	recorder := installRecorder(t)

	_, span := tracing.NewTracer("test-scope").StartSpan(context.Background(), "idem")
	span.End()
	span.End()

	require.Len(t, recorder.Ended(), 1,
		"a second End() must remain a no-op — OTel deduplicates internally")
}
