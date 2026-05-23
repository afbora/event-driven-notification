# Skill: add-adr

## Purpose

Record an Architectural Decision Record (ADR) when a non-trivial design choice is made, so the reasoning survives long after the decision was made.

## When To Use

- A new library is being adopted.
- A pattern is being introduced (or rejected) — caching strategy, error handling style, etc.
- A non-obvious trade-off has been chosen.
- A decision is being reversed (the new ADR supersedes the old).

When **not** to use: trivial choices (file naming, variable naming, formatting). ADRs are for things future engineers will ask "why did we do it this way?"

## Steps

### 1. Pick the next number

ADRs live in `docs/adr/` and are numbered:

```
docs/adr/
  0001-hexagonal-architecture.md
  0002-postgres-with-sqlc.md
  0003-redis-asynq-queue.md
  ...
```

The next ADR is the highest number + 1. Use 4-digit zero-padded numbering.

### 2. Create the file

```sh
touch docs/adr/0009-circuit-breaker-thresholds.md
```

### 3. Use the template

```markdown
# ADR-0009: Circuit Breaker Thresholds For Provider Calls

**Status:** Accepted
**Date:** 2026-05-23
**Deciders:** Bora

## Context

Provider calls (webhook.site in development; Twilio/SendGrid/FCM in production) can fail in patterns that hurt the system if we keep retrying blindly: a regional outage causes every call to time out, exhausting worker concurrency and growing the queue without making progress.

We have introduced a circuit breaker (via `sony/gobreaker`). The question is: what thresholds?

## Decision

We use the following settings for the per-provider circuit breaker:

- **Trip threshold:** 5 consecutive failures within 10 seconds.
- **Open duration:** 30 seconds before attempting half-open.
- **Half-open probe:** 1 request; if it succeeds, close; if it fails, re-open for another 30 seconds.

These settings apply uniformly across SMS, Email, and Push providers initially. Per-channel tuning is deferred until we have real production data.

## Consequences

**Positive:**

- A flapping provider can no longer monopolise worker capacity.
- Open state surfaces clearly in metrics (`notifications_circuit_breaker_state` gauge) and Grafana.
- Operators get an alert (`CircuitBreakerOpen`) within 5 minutes.

**Negative:**

- Notifications routed to an open breaker fail immediately with `ErrCircuitOpen` and will be retried by asynq later. Users may see slightly higher tail latency during recovery.
- The 30-second open duration is a guess; we may need to tune per-provider once we have data.

## Alternatives Considered

1. **Adaptive thresholds** (e.g., open if error rate > 50% over 1 minute) — more sophisticated, but harder to reason about and overkill for the initial implementation.
2. **No circuit breaker, rely on retry backoff alone** — rejected because retry backoff does not bound worker concurrency consumption.
3. **Per-channel hand-tuned settings** — deferred until we have data; uniform settings now.

## Related

- ADR-0003 (Redis + asynq for queue)
- `docs/RUNBOOK.md#circuitbreakeropen`
- `internal/infrastructure/circuit/breaker.go`
```

### 4. Required sections

Every ADR must have:

- **Title:** `ADR-NNNN: <Short Topic>`
- **Status:** `Proposed`, `Accepted`, `Deprecated`, or `Superseded by ADR-XXXX`
- **Date:** ISO 8601
- **Deciders:** Names
- **Context:** What problem? What forces? What constraints?
- **Decision:** What we chose. Concrete, specific.
- **Consequences:** Positive and negative outcomes of this decision.
- **Alternatives Considered:** What else was on the table and why it was rejected.

Optional but useful:

- **Related:** Links to other ADRs, code paths, runbook sections, external references.

### 5. Reference the ADR from the code or document that implements the decision

In the relevant Go file or config:

```go
// Circuit breaker settings per ADR-0009.
var defaultSettings = gobreaker.Settings{
    Name:        "provider",
    MaxRequests: 1,
    Interval:    10 * time.Second,
    Timeout:     30 * time.Second,
    // ...
}
```

In the runbook entry:

```
**Background:** See ADR-0009 for circuit breaker threshold reasoning.
```

### 6. Commit

```
docs(adr): add ADR-0009 on circuit breaker thresholds
```

If the ADR drives a code change in the same PR, the ADR commit comes **first** so the reasoning is documented before the implementation.

### 7. Superseding an old ADR

When a decision is reversed:

1. Change the old ADR's `Status:` to `Superseded by ADR-NNNN`.
2. Add a one-line note at the top of the old ADR linking to the new one.
3. The new ADR's `Context:` references the old ADR and explains why the change.

## Verification

- [ ] File numbered correctly (no gaps, no duplicates).
- [ ] All required sections present.
- [ ] Linked from the code or document that implements it.
- [ ] If superseding, the old ADR is updated.
- [ ] Committed before the implementing code change.

## Common Mistakes

- Vague decisions ("we use a circuit breaker"). Be specific: thresholds, settings, why.
- Skipping "Alternatives Considered." This section is where the reasoning lives.
- Treating the ADR as paperwork to fill in after the fact. ADRs are decision documents, not history.
- Editing an accepted ADR to change the decision. Add a new ADR that supersedes; do not rewrite history.
- Numbering collisions when two branches add ADRs in parallel. Coordinate with the human if working on a parallel branch.
- ADR for everything. The signal-to-noise ratio matters; reserve ADRs for things future engineers will ask about.
