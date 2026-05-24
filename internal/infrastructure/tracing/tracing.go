// Package tracing wires the OpenTelemetry SDK with a no-op default
// and an OTLP/gRPC exporter when an endpoint is configured
// (CLAUDE.md §12.4). The pattern: every binary calls Setup once at
// startup, defers the returned shutdown, and uses otel.Tracer to
// create spans throughout.
//
// Switching from no-op to real tracing is a deploy-time concern: set
// OTEL_EXPORTER_OTLP_ENDPOINT (or pass Config.Endpoint explicitly)
// and point a collector at it. No code changes required.
package tracing

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// ShutdownFunc flushes pending spans and tears the provider down.
// Bind it in the caller via defer; callers that have already begun
// shutdown can pass a short context to bound the flush.
type ShutdownFunc func(ctx context.Context) error

// Config carries the knobs Setup honors. Empty Endpoint plus empty
// OTEL_EXPORTER_OTLP_ENDPOINT env yields a no-op provider — the
// default for local development.
type Config struct {
	// ServiceName tags every span and shows up as `service.name` in
	// the collector. Each binary passes its own ("api", "worker",
	// "reconciler") so span streams are filterable.
	ServiceName string

	// Endpoint is the OTLP gRPC target (host:port). Empty falls
	// back to the OTEL_EXPORTER_OTLP_ENDPOINT env var; if that is
	// also empty Setup installs a no-op provider.
	Endpoint string
}

// Setup installs a global tracer provider configured per cfg and
// returns a shutdown func. The function is idempotent — calling
// shutdown twice is safe.
//
// No-op semantics: when no endpoint is supplied, otel.Tracer still
// returns a usable tracer, but its spans do nothing. Call sites do
// not need to nil-check or conditionally avoid Span calls.
func Setup(ctx context.Context, cfg Config) (ShutdownFunc, error) {
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}

	// Always set the global propagator so HTTP and queue handlers
	// can extract/inject trace context regardless of whether
	// exporting is live.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if endpoint == "" {
		// No-op provider. The global default already is a no-op so
		// we just hand back a no-op shutdown for symmetry.
		return func(context.Context) error { return nil }, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(), // dev compose; TLS is an ops swap-in
	)
	if err != nil {
		return nil, fmt.Errorf("build otlp exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	return func(c context.Context) error {
		// Best-effort shutdown — drop errors from a closed
		// collector so deferred calls do not surface noise.
		_ = tp.Shutdown(c)
		_ = exp.Shutdown(c)
		return nil
	}, nil
}
