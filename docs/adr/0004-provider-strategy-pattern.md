# ADR-0004: Provider Strategy Pattern For Channels

**Status:** Accepted
**Date:** 2026-05-23
**Deciders:** Ahmet Bora

## Context

The brief requires three channels — **SMS**, **Email**, **Push** — and the assessment context explicitly mentions future channel additions ("WhatsApp, in-app notifications, etc.") as a likely extension. A naive implementation would put a `switch channel` block in the worker:

```go
switch n.Channel {
case ChannelSMS:   /* call SMS provider */
case ChannelEmail: /* call Email provider */
case ChannelPush:  /* call Push provider */
}
```

This pattern violates the Open-Closed Principle: adding a channel means editing the worker, and there is no compile-time guarantee that every channel is handled. It also makes the worker a god-object that knows the details of every provider.

The brief's external-provider section says we should use `webhook.site` to simulate the third-party. We need at least two implementations from day one — a real `WebhookProvider` and a `MockProvider` for tests.

## Decision

We use the **Strategy Pattern**, expressed as a port:

```go
// internal/ports/provider.go
type Provider interface {
    Send(ctx context.Context, channel Channel, recipient, content string) DeliveryResult
}
```

`DeliveryResult` carries success / failure status, provider message ID, and a **retryable** flag (transient vs permanent) so the worker can decide whether to schedule a retry.

A **`ProviderRegistry`** (also in `internal/ports/`) maps `Channel → Provider`. The worker:

```go
provider := registry.For(n.Channel)
result   := provider.Send(ctx, n.Channel, n.Recipient, n.Content)
```

Implementations live in `internal/adapters/provider/`:

- `WebhookProvider` — calls `webhook.site` (or any HTTP endpoint) with the request format documented in the brief.
- `MockProvider` — configurable success rate and latency; used in unit and integration tests.
- Future: `TwilioProvider`, `SendGridProvider`, `FCMProvider`. None of these require changes outside the registry wiring.

The Provider call is wrapped in a circuit breaker decorator (`sony/gobreaker`) so a flapping provider does not exhaust worker concurrency. Settings will be documented in a later ADR if they prove non-trivial.

## Consequences

**Positive:**

- Adding a channel is one new file plus one line in `cmd/api/main.go` (registry wiring). No worker edits. No switch statements.
- The worker depends only on `ports.Provider`, so unit tests use `MockProvider` directly with no HTTP fixture.
- The circuit breaker decorator wraps **the port**, not each implementation — so every current and future provider gets it for free.
- Provider-specific retry classification lives next to the provider code (e.g., Twilio's 4xx → permanent, 5xx → retryable), not scattered through the worker.

**Negative:**

- One extra interface to maintain. `ports.Provider` must change carefully because every adapter and every test mock changes with it.
- The registry is a small piece of global-ish state. We pass it explicitly through the use-case constructor; nothing is registered via `init()`.

## Alternatives Considered

1. **Switch statement** — rejected per CLAUDE.md anti-patterns. Closed for extension, requires editing the worker for every new channel.
2. **Map of function pointers (`map[Channel]func(...) DeliveryResult`)** — works but loses the ability to attach per-provider state (HTTP client, credentials) and to add cross-cutting decorators cleanly.
3. **One binary per channel** — rejected. Operational overhead (one Dockerfile per channel, one compose service per channel) for no benefit at this scale.
4. **External provider gateway service** — rejected for this assessment. Possible in production where many services share a notification SDK.

## Related

- CLAUDE.md §3.4 (SOLID, applied concretely — "O"), Anti-Patterns ("switch statements on channel type")
- `.claude/skills/core/add-provider/SKILL.md`
- ADR-0001 (Hexagonal Architecture) — the Provider port is one of the canonical ports
- `internal/ports/provider.go`
- `internal/adapters/provider/`
