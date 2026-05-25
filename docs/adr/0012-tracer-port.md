# ADR-0012: Wrap OpenTelemetry Behind ports.Tracer To Keep the Application Layer Stdlib-Pure

**Status:** Accepted
**Date:** 2026-05-26
**Deciders:** Ahmet Bora

## Context

CLAUDE.md §3.3 is one of the project's load-bearing invariants:

> `internal/domain/` and `internal/application/` import **only** the Go standard library and other packages within those two directories. … This is the **Dependency Inversion Principle** made physical. The reviewer should be able to delete `internal/adapters/postgres/` and the domain still compiles.

`internal/application/process_notification.go` violated that invariant: to wrap the provider call in an OpenTelemetry span, it imported `go.opentelemetry.io/otel`, `attribute`, and `trace` directly. The original hexagonal-boundary E2E check missed it because the grep listed only the stack we feared (`database/sql`, `net/http`, `asynq`, `redis`, `pgx`) — OpenTelemetry slipped past.

A peer code review (case-review pass on `E2E_REPORT.md` §N) surfaced this as **L1** — *low severity* but a real architecture-purity break that contradicts an explicit project invariant. Three forces shaped the fix:

1. **The application layer must stay stdlib-pure** for the same reasons §3.3 spells out: the layer encodes our domain logic; coupling it to any third-party SDK makes the layer harder to test in isolation and harder to port if the SDK changes shape.
2. **Spans must keep working in production** with the existing global `TracerProvider` setup (`internal/infrastructure/tracing.Setup`) and the existing no-op default (when no OTLP exporter is configured).
3. **The change should be small** and not invent abstractions the worker does not actually need. The only span the worker opens today is `provider.send`, with four attributes; the surface we expose should match that, not OTel's full API.

We considered four shapes:

1. **Move the span work to an adapter** — push the OTel call site out of the application layer into `internal/adapters/asynq` or `internal/adapters/provider`. Rejected: the span is inherently about *what the use case does* (open a span around the provider call, with use-case-specific attributes); moving it loses that intent. The use case would have to expose internal lifecycle hooks the adapter could attach to.
2. **A `ports.Tracer` port wrapping OTel** — define a tiny interface in `internal/ports`, implement it as an adapter that wraps `go.opentelemetry.io/otel`, inject it into the use case via the existing `ProcessNotificationDeps` struct. Hexagonal-clean; application code never sees the OTel types.
3. **Soften the §3.3 rule** to "domain stdlib-only; application may import OTel because OTel is vendor-neutral". Rejected: the rule is the rule. The point of having an invariant is that it does not move under pressure; if the cost of fix is low, fix it.
4. **Use `// nolint` style suppression on the imports** with a justification comment. Rejected: that documents a violation, it does not remove it.

## Decision

We introduce a **`ports.Tracer`** port with a deliberately minimal surface (`StartSpan` + typed attribute setters + `End`), implement it as a thin adapter in `internal/infrastructure/tracing/tracer.go` that wraps `go.opentelemetry.io/otel`, and inject it into `ProcessNotificationDeps`. The application layer drops every OTel import.

```go
// internal/ports/tracing.go
type Tracer interface {
    StartSpan(ctx context.Context, name string) (context.Context, Span)
}
type Span interface {
    SetStringAttr(key, value string)
    SetBoolAttr(key string, value bool)
    End()
}

// Zero-cost default so call sites never need a nil-guard.
type NoopTracer struct{}
```

Constructor behavior: `NewProcessNotification` substitutes `NoopTracer{}` when `deps.Tracer` is nil, so the existing e2e harness (which does not wire a tracer) continues to work unchanged and call sites can rely on `uc.tracer` being non-nil.

Adapter (`internal/infrastructure/tracing/tracer.go`) resolves the named tracer from the global `TracerProvider` that `tracing.Setup` installs at startup. When no exporter is wired (the default), the global provider is itself a no-op, so spans cost nothing — production behavior is identical to the pre-refactor state.

`cmd/worker/main.go` constructs the adapter via `tracing.NewTracer("github.com/afbora/event-driven-notification/internal/application")` (the OTel instrumentation-scope convention) and threads it into `ProcessNotificationDeps`. `cmd/api/main.go` does not change because the API binary never constructs `ProcessNotification`.

## Consequences

**Positive:**

- `internal/domain/` and `internal/application/` are now genuinely third-party-free (re-verified with a regex grep, not the historical short list). The §3.3 invariant matches reality.
- The application layer is more isolated for testing — the `recordingTracer` fake in `process_notification_test.go` captures span lifecycle (name, attrs, End) without standing up an OTel SDK at all.
- The Tracer surface is small enough to grow in lockstep with actual call-site needs (a new attribute type → one new method on `Span`); over-engineering pressure is minimal.
- Production spans cost the same as before: when no exporter is configured the OTel global provider is no-op and the adapter is a one-method-call shim around it.

**Negative:**

- Two new types (`ports.Tracer`, `ports.Span`) and a 50-line adapter add line count to the repo for a refactor that delivers no behavior change. We accept this as the cost of keeping the architecture-purity invariant honest.
- Future OTel features (events, links, status codes) require widening the port if we want them in the application layer. The doc on the port type calls this out explicitly so the next person knows what shape the next addition should take.
- The constructor's nil-tracer fallback is a small piece of magic — tests pass `nil`, production passes a real tracer, both work. The fallback is documented at the constructor; the `NoopTracer` type's doc explains why it exists.

## Alternatives Considered

1. **Move the span into an adapter** — rejected. The span is use-case-scoped (it wraps the provider call with use-case-specific attributes); pushing it out of the use case loses authorial intent and would require new lifecycle hooks. See "shapes considered" #1 above.
2. **Soften CLAUDE.md §3.3 to allow OpenTelemetry imports in application** — rejected. The rule exists because cross-layer coupling has a real cost; carving exceptions ("OTel is fine, it is vendor-neutral") opens a door we would have to close later. The fix is cheap; pay the fix.
3. **`//nolint` comments on the imports** — rejected. That documents a violation in place, it does not remove it. The next refactor pass would still have to do this work.
4. **Generic `ports.Observer` covering metrics + traces + logs** — rejected as scope creep. Metrics already flow through `application.MetricsRecorder`; logs go through `slog` with the project's `contextHandler`. A single observer interface would be larger and serve less well.

## Related

- CLAUDE.md §3.3 (Domain Purity), §12.4 (Tracing)
- ADR-0001 (Hexagonal Architecture)
- `internal/ports/tracing.go` — port + `NoopTracer`
- `internal/infrastructure/tracing/tracer.go` — OTel-backed adapter
- `internal/infrastructure/tracing/tracing.go` — global `TracerProvider` setup (unchanged)
- `cmd/worker/main.go` — wiring
- `E2E_REPORT.md` §N — re-tightened hexagonal-boundary check that now uses a comprehensive regex instead of a short fixed list
