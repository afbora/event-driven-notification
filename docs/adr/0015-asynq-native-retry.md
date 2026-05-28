# ADR-0015: Asynq-Native Retry; Reconciler Narrowed To Safety-Net Only

**Status:** Accepted
**Supersedes (in part):** [ADR-0011](0011-reconciler-no-outbox.md) — the retry-authority implication
**Date:** 2026-05-26
**Deciders:** Ahmet Bora

## Context

[ADR-0011](0011-reconciler-no-outbox.md) chose a reconciliation safety net over the outbox pattern for the dual-write race, and noted that the same reconciler would also catch *"Overdue retrying — a notification in `status='retrying'` whose `next_retry_at` has elapsed but never got picked up (Redis flush, asynq schedule loss). Re-enqueue."*

The implementation drifted from the wording. The worker's `markRetrying` (and the rate-limit-defer path's `rescheduleForRateLimit`) returned `nil` to the asynq processor. Asynq reads a nil return as "task delivered successfully" and **does not retry**. The reconciler's `FindOverdueRetrying` sweep — which ADR-0011 framed as a safety net — became the **only** path that re-enqueued retrying rows. CLAUDE.md §5's documented behavior ("asynq retries with exponential backoff") and the actual code path agreed about the intent but not about who was driving.

A peer code review on the post-fix repo (case-review pass, M2 + M3) caught both symptoms:

- **M2 — Misleading comments.** `markRetrying`'s doc said *"asynq honors NextRetryAt; the worker re-runs Execute on the next delivery"*. False under the implementation as shipped.
- **M3 — Reconciler-quantized retry timing + drain ceiling.** With the reconciler running every 1 minute and a 1-minute `overdueRetryingThreshold`, the first retry of a transient failure landed ~1.5–2.5 minutes after the failure instead of the designed 30 seconds. The reconciler batch is capped at 100 rows per tick, so a 100k-row backlog from a provider outage would drain at ~6k/hour, taking ~16 hours. For a brief whose business context is *"millions of notifications daily"*, this is a structural ceiling.

Both symptoms have one cause: the wrong actor owns retry timing. Three design shapes were on the table.

1. **Keep reconciler as primary, tighten its cadence.** Drop the reconciler tick from 1 min to 10 s, loop the batch until drained, drop `overdueRetryingThreshold` to a few seconds. Mechanically possible; the reconciler runs hotter constantly. Two writers (reconciler + occasional asynq dead-letter rescue) still split the policy.
2. **Asynq-native retry.** Return a non-nil error from the use case on transient + rate-limit paths. Asynq's `RetryDelayFunc` picks the next-attempt delay per error class. Reconciler keeps the *truly* edge-case sweep (Redis flush, scheduler crash) at a much looser threshold. Single authority for retry timing, matching how Sidekiq / Resque / asynq itself were designed.
3. **Dual write to a retry topic.** Application writes a retry intent record; a separate process re-enqueues. Most ceremony; defers the same problem to a new dual-write race.

## Decision

We choose shape **(2)** — asynq-native retry. The reconciler keeps a sweep for retrying rows but it is now a **safety net for asynq itself**, not the primary mechanism.

Implementation:

- `internal/application/errors.go` defines two typed sentinels:
  - `ErrProviderTransient` — provider returned a retryable 5xx-class result.
  - `ErrOutboundRateLimited` — the per-channel rate limiter rejected the send.
- `markRetrying` returns `fmt.Errorf("transient failure on %s: %w", n.ID, ErrProviderTransient)` after writing the row's status + log + duration side effects. `rescheduleForRateLimit` returns the rate-limit sentinel the same way.
- `application.RetryDelayFor(attempts, err) time.Duration` is the single policy API:
  - `errors.Is(err, ErrOutboundRateLimited)` → `rateLimitBackoff` (1 s).
  - everything else (transient sentinel + any unwrapped infrastructure error from the claim path) → `backoffFor(attempts)` (30 s, 60 s, 120 s, ...).
- `cmd/worker/main.go` wires a one-line `retryDelay` shim that delegates to `application.RetryDelayFor` and sets it on `asynq.Config.RetryDelayFunc`. Policy stays in the application package; the worker only configures asynq.
- `overdueRetryingThreshold` widens from 1 minute to **10 minutes**. The reconciler now only catches rows where asynq has truly lost the schedule (Redis flush, scheduler crash). Under healthy operation a retry fires within seconds — well inside the new threshold.

## Consequences

**Positive:**

- Retry latency matches the documented exponential schedule: first transient retry at ~30 s (was ~1.5–2.5 min); rate-limit retry at ~1 s (was ~1 min). The brief's *"intelligent retry"* requirement is now actually intelligent.
- Drain ceiling lifts from ~6k retries/hour (reconciler batch × cadence) to asynq-native (Redis-bound, thousands/sec). A 100k-row backlog from a provider outage drains in minutes, not hours.
- Single retry authority matches every comparable queue framework's design — Sidekiq, Resque, Celery, and asynq itself are all *"handler returns error, framework re-schedules"*. New contributors recognize the shape immediately.
- The reconciler's role contracts to what its name implies — a reconciliation safety net, not a primary scheduler. ADR-0011's framing matches reality again.
- CLAUDE.md §5's documented failure-path behavior ("asynq retries with exponential backoff + jitter") matches the implementation. Comments and code agree.

**Negative:**

- Two retry actors now exist (asynq native + reconciler safety-net sweep). Double-fire risk is real but bounded: the reconciler's `overdueRetryingThreshold` of 10 minutes is far past any realistic asynq retry latency, and the SQL filter is `next_retry_at < $1` so a row whose asynq retry is still pending stays invisible to the reconciler. Pinned by `TestFindOverdueRetrying_HappyPath`'s timing assertions and the unit-test coverage of `RetryDelayFor`.
- The task is created with an explicit `MaxRetry(5)` (`internal/adapters/asynq/tasks.go`, `maxRetryAttempts = 5`), aligned with the application cap of `defaultMaxAttempts = 5`. In practice asynq's own retry counter never reaches that ceiling on transient failures: `applyResult` returns nil (markFailed) once `n.Attempts >= max`, so the row is marked failed before asynq would exhaust its count. Because both numbers agree at 5, the asynq UI and the application's `attempts` column tell the same story — operators read the row's `attempts` column or `notification_logs` for ground truth either way.
- The error-return contract is now load-bearing: a future refactor that accidentally returns `nil` from `markRetrying` would silently disable retry. The unit tests (`TestProcessNotification_TransientFailure`, `_RateLimited`) assert `ErrorIs(...)` against the matching sentinel so the regression would surface in CI.

## Alternatives Considered

1. **Tighten reconciler cadence (shape 1)** — rejected. Mechanically possible (drop tick to 10 s, drain batch in a loop, drop threshold to 5 s) but keeps the reconciler as the primary retry mechanism, which is not what its name implies. Two actors with overlapping responsibilities is harder to reason about than two actors with disjoint roles (asynq = primary, reconciler = safety net). And the constant tick load is harder to capacity-plan than asynq's pull-based schedule.
2. **Dual write to a retry topic (shape 3)** — rejected. Defers the dual-write problem one level up (notification table + retry topic + asynq), introduces a new failure mode (retry-topic poller falls behind), and asynq's own retry queue already provides the same guarantee without the ceremony.
3. **Keep `markRetrying` returning nil; use asynq's `asynq.SkipRetry` only for terminal failures.** Rejected as a no-op — asynq already treats nil and `SkipRetry` identically for the "no further retry" case. The discriminator is non-nil vs nil/SkipRetry, and we want non-nil on the retry paths.
4. **Use asynq's built-in default `RetryDelayFunc` (its own exponential backoff).** Rejected. Asynq's default is `min(2^n, 60s) + random jitter` capped at 60 s — short max delay. Our `backoffFor` goes to 30 s × 2^(attempts-1) = 480 s at attempt 5, which is the schedule the brief expects (longer waits between later retries to absorb sustained provider outages). Our policy is the right one to ship; the framework's hook lets us plug it in cleanly.

## Related

- [ADR-0009](0009-atomic-status-claim.md) — atomic claim is unchanged; the retry shift sits one layer above (after the claim succeeds and the provider call returns).
- [ADR-0011](0011-reconciler-no-outbox.md) — this ADR supersedes the *retry-authority* implication only. ADR-0011's dual-write rationale and `FindOrphanedPending` sweep are unchanged; this ADR narrows the reconciler's role from primary retry mechanism to safety-net-only for the retrying state.
- [ADR-0013](0013-reconciler-stuck-queued-sweep.md) — the fourth sweep (stuck-queued) is unaffected; its semantics (re-enqueue without status change) match how the reconciler still treats the rare overdue-retrying case.
- CLAUDE.md §5 (failure paths) — text matches the implementation again after this shift.
- `internal/application/errors.go` — sentinels + `RetryDelayFor` policy
- `internal/application/process_notification.go` — `markRetrying`, `rescheduleForRateLimit`
- `internal/application/reconcile_stuck_notifications.go` — `overdueRetryingThreshold = 10 * time.Minute`
- `cmd/worker/main.go` — `retryDelay` shim on `asynq.Config.RetryDelayFunc`
