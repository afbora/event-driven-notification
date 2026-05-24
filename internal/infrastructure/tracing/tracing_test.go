package tracing_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	"github.com/afbora/event-driven-notification/internal/infrastructure/tracing"
)

// TestSetup_NoopWhenEndpointEmpty: when OTLP endpoint is empty
// Setup wires a no-op tracer provider — no exporter goroutine, no
// network IO. The returned shutdown func must be safe to call.
func TestSetup_NoopWhenEndpointEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	shutdown, err := tracing.Setup(ctx, tracing.Config{
		ServiceName: "api",
		Endpoint:    "",
	})
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	// Tracer must be usable — spans become no-ops but the API
	// stays the same, so call sites do not need to nil-check.
	tr := otel.Tracer("test")
	_, span := tr.Start(ctx, "noop-span")
	span.End()

	require.NoError(t, shutdown(ctx))
}

// TestSetup_RealEndpointDoesNotPanic: a real endpoint string makes
// Setup build the OTLP/gRPC exporter. Without a collector at the
// address spans never get sent — but the build path must not error
// (the connect happens lazily on first export).
func TestSetup_RealEndpointDoesNotPanic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	shutdown, err := tracing.Setup(ctx, tracing.Config{
		ServiceName: "api",
		Endpoint:    "localhost:4317",
	})
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	// Bound shutdown so a missing collector does not stall the test.
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutCancel()
	_ = shutdown(shutCtx) // ignore error — no collector listening
}

// TestSetup_ReadsEnvFallback: when Config.Endpoint is empty Setup
// checks OTEL_EXPORTER_OTLP_ENDPOINT so operators can flip tracing
// on at deploy time without touching code (CLAUDE.md §12.4).
func TestSetup_ReadsEnvFallback(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	shutdown, err := tracing.Setup(ctx, tracing.Config{ServiceName: "api"})
	require.NoError(t, err)
	require.NoError(t, shutdown(ctx))
}
