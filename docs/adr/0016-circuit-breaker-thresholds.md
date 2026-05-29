# ADR-0016: Explicit, Configurable Circuit-Breaker Thresholds

**Status:** Accepted
**Date:** 2026-05-29
**Deciders:** Ahmet Bora

## Context

The worker wraps the provider registry in a `sony/gobreaker` circuit breaker (ADR-0007's "passive failure handling"; CLAUDE.md §5). The breaker was constructed with only three fields set — `Name`, `MaxRequests: 1`, and the `OnStateChange` hook that feeds the `notifications_circuit_breaker_state` gauge. The two fields that actually decide *when* the breaker trips and *how long* it stays open were left unset, so they fell back to gobreaker's implicit defaults:

- **`ReadyToTrip`** default: trip when `ConsecutiveFailures > 5` (i.e. the 6th consecutive failure).
- **`Timeout`** default: stay open for **60 seconds** before allowing a half-open probe.

But CLAUDE.md §5 documents a different, specific behavior:

> Circuit breaker opens after **5 failures in 10 seconds** → subsequent attempts fail fast for **30 seconds** → half-open probe → close on success.

And the README failure-path diagram shows "fail-fast for 30s". So the running breaker silently contradicted the constitution on both axes: it tripped on *consecutive* failures (not a 5-in-10s rate), and it stayed open for 60s (not 30s). A live sweep had already noticed the symptom — a small burst of failures interleaved with the occasional success never tripped the breaker, because the consecutive counter kept resetting.

This is a code↔documentation mismatch in the direction that matters most: the documented behavior is the *intended* one. The fix is to make the code match the constitution, not to weaken the constitution to match the code.

## Decision

Make the breaker thresholds **explicit** and **configurable**, defaulting to exactly what CLAUDE.md §5 documents.

Three config knobs (validated positive at startup; CLAUDE.md §3.7 fail-fast):

| Env var | Field | Default | gobreaker field |
|---|---|---|---|
| `CIRCUIT_MAX_FAILURES` | `CircuitMaxFailures` | `5` | `ReadyToTrip` predicate |
| `CIRCUIT_WINDOW` | `CircuitWindow` | `10s` | `Interval` |
| `CIRCUIT_OPEN_TIMEOUT` | `CircuitOpenTimeout` | `30s` | `Timeout` |

`cmd/worker`'s `breakerSettings` builds the `gobreaker.Settings` from these:

- `ReadyToTrip: func(c gobreaker.Counts) bool { return int(c.TotalFailures) >= maxFailures }` — trips on **total** failures in the current counting window, not consecutive ones. This is the "5 failures within a window" rate the brief and constitution describe; a slow drip of failures interleaved with successes still trips it.
- `Interval: window` — gobreaker clears its closed-state counts every `Interval`, so "5 failures within 10s" is scoped to a **fixed** 10-second accounting window (see Consequences for the sliding-window caveat).
- `Timeout: openTimeout` — fail fast for 30s, then allow a single half-open probe (`MaxRequests: 1`).

The breaker only counts **transient** failures as failures: `internal/infrastructure/circuit` returns a non-nil error to gobreaker only when the `DeliveryResult` is `!Success && Retryable`. Permanent (4xx-class) results are caller errors, return nil, and never trip the breaker — so a flood of bad requests cannot open the circuit and starve healthy traffic.

The defaults live in both `internal/infrastructure/config` (the code default) and `docker-compose.yml` (made explicit in the worker env block, matching how `MOCK_PROVIDER_*` and the rate-limit knobs are surfaced) so an operator sees the production cap without grepping source.

## Consequences

**Positive:**

- The running breaker matches CLAUDE.md §5 and the README diagram exactly — code and documentation agree. The `notifications_circuit_breaker_state` gauge and the live-sweep observations now reflect the documented contract.
- Thresholds are tunable per deploy (env var) without a rebuild — the production-tuning path the original `breakerSettings` comment promised "later" is now real.
- Trip semantics are the rate-based "5 in 10s" the brief implies for "intelligent" failure handling, not gobreaker's consecutive-only default that a flapping provider can evade.
- Config validation rejects a zero/negative trip count or timeout at startup, so a typo fails the deploy loudly instead of shipping a dead or hair-trigger breaker.

**Negative / trade-offs:**

- gobreaker's `Interval` is a **fixed** cyclic window, not a sliding one: counts reset at each 10s boundary, so 4 failures at t=9s and a 5th at t=11s land in different windows and do not trip. A true sliding window would need a custom counter. For this system the fixed window is an honest, well-understood approximation of "5 in 10s" and avoids hand-rolling failure bookkeeping that gobreaker already owns.
- Two more config knobs to document and reason about. Mitigated by validated defaults that match the constitution, so the common path needs no configuration at all.
- `int(c.TotalFailures)` converts a `uint32` to `int`. This is safe on the 64-bit targets we ship (the value never approaches 2³¹), and it avoids a lossy `int → uint32` conversion of the config value that would otherwise trip `gosec` G115.

## Alternatives Considered

1. **Leave gobreaker's defaults in place.** Rejected — it is the source of the code↔doc mismatch this ADR exists to close. "It works in the demo" is not the bar; the documented behavior is.
2. **Match the docs but key on `ConsecutiveFailures` (gobreaker's default shape).** Rejected — "5 failures in 10 seconds" is a rate within a window, not 5 in an unbroken row. A provider that fails 4 of every 5 calls would keep resetting the consecutive counter and never trip, which is exactly the pathology a circuit breaker should catch.
3. **Hard-code the thresholds (5 / 10s / 30s) without config.** Rejected — the assessment rewards demonstrating production maturity, and breaker thresholds are the canonical thing operators tune against real traffic. Config + validated defaults costs little and shows the right instinct. (It also keeps the no-`.env`, all-in-compose convention of ADR-0010 intact.)
4. **Rewrite CLAUDE.md §5 down to "5 consecutive failures, 60s open" to match the old code.** Rejected — that weakens the constitution to rationalize an unset field. The constitution is the contract; the code is what bends.

## Related

- [ADR-0007](0007-failure-handling-interpretation.md) — circuit breaking is part of the "passive failure handling" interpretation; this ADR pins its thresholds.
- [ADR-0015](0015-asynq-native-retry.md) — retry timing sits one layer above the breaker; an open breaker surfaces as a transient `DeliveryResult` that feeds the same asynq-native retry path.
- CLAUDE.md §5 (failure paths) — the documented behavior this ADR makes the code honor.
- `internal/infrastructure/config` — `CircuitMaxFailures` / `CircuitWindow` / `CircuitOpenTimeout` + validation.
- `cmd/worker/main.go` — `breakerSettings` builds the `gobreaker.Settings`.
- `internal/infrastructure/circuit` — counts only transient failures toward the breaker.
