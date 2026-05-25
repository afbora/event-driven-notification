# ADR-0013: Reconciler Stuck-Queued Sweep With scheduled_at Guard

**Status:** Accepted
**Date:** 2026-05-26
**Deciders:** Ahmet Bora

## Context

[ADR-0011](0011-reconciler-no-outbox.md) chose a reconciler-based safety net over the outbox pattern to handle the dual-write race when the API persists a notification and then enqueues it. It documented one half of the race — the `pending` orphan, where `enqueue` fails after `Create` succeeds — and gave the reconciler a `FindOrphanedPending` sweep to recover those rows.

A peer code review (case-review pass, **M1**) surfaced the other half. The actual `CreateNotification` flow is:

```
1. INSERT INTO notifications (..., status='pending')   -- PostgreSQL
2. asynq.Enqueue(notificationID, ...)                  -- Redis
3. UPDATE notifications SET status='queued' WHERE id=$1
```

If a worker dequeues the asynq task between steps 2 and 3 — the race window is sub-millisecond, but it exists — the atomic claim (`UPDATE … WHERE status IN ('queued', 'retrying')`, [ADR-0009](0009-atomic-status-claim.md)) is a no-op on the still-`pending` row. The worker returns `nil` to asynq, asynq counts the task as delivered and discards it, and step 3 then writes `status='queued'`. The row is now in `queued` with **no task on the queue**. None of the three pre-existing sweeps (`pending`, `processing`, `retrying`) cover it, so the row sits stranded until a human notices.

The fix shape is similar to the orphaned-pending sweep — find rows in `queued` past a threshold, re-enqueue them — but two design questions need explicit decisions:

1. **Recovery semantics.** Re-enqueuing alone vs. transitioning status. The row is in the right state already; only the task is missing.
2. **What about scheduled notifications.** Notifications with `scheduled_at` in the future live in `queued` from creation until their scheduled time fires (asynq holds the task as a delayed entry). After the 5-minute threshold a naive sweep would see them as "stuck" and re-enqueue, producing a duplicate delivery the moment the original delayed task fires. This is a real correctness risk, not a theoretical one.

## Decision

We add a **fourth reconciler sweep** named `FindStuckQueued` with the following shape:

```sql
SELECT *
FROM   notifications
WHERE  status      = 'queued'
  AND  updated_at  < $1                              -- 5-minute threshold
  AND  (scheduled_at IS NULL OR scheduled_at < $1)   -- load-bearing guard
ORDER BY updated_at
LIMIT  $2
FOR UPDATE SKIP LOCKED;
```

The handler **re-enqueues only**:
- Status stays `queued` (it is already correct — only the task is missing).
- No `notification_logs` row is written: there is no new event, just a recovered delivery. Writing a log row would imply a state transition the user could mistake for "the system did something new"; the trace endpoint already shows the original `created → queued`.
- Counter `StuckQueuedReenqueued` surfaces the sweep's work to the reconciler log line and (once Phase 6 lands) a Prometheus counter.

The threshold is **5 minutes**, mirroring `orphanedPendingThreshold` and `stuckProcessingThreshold`. The actual race window is sub-millisecond; five minutes guarantees the worker has either finished claim (status no longer matches) or crashed (no recovery is coming from the worker).

The **`scheduled_at` predicate is load-bearing**, not cosmetic. Without it:
- A notification with `scheduled_at = now + 30 minutes` is created → enters `queued` → updated_at = created_at
- 5 minutes later, `updated_at < now - 5min`, sweep flags it as stuck
- Reconciler re-enqueues → asynq now has a fresh non-delayed task for that ID
- 25 minutes later, the original delayed task also fires
- **Duplicate delivery.**

The predicate `scheduled_at IS NULL OR scheduled_at < older_than` means "only touch rows that either have no scheduled time at all (immediate delivery that lost its task) or whose scheduled time itself is more than the threshold in the past (the delayed task should have fired but did not — itself a real recovery target, e.g. asynq scheduler crash or Redis flush). This is pinned by the integration test `TestFindStuckQueued_ExcludesFutureScheduled`.

The sweep uses `SELECT ... FOR UPDATE SKIP LOCKED` like the other three so multiple reconciler instances can run safely in parallel.

## Consequences

**Positive:**

- The dual-write race is now fully closed. ADR-0011 covered one half; this ADR covers the other.
- The architecture stays consistent: four sweeps, one shape (`Find* → iterate → handle → counter`), one threshold style, one concurrency model. The reconciler's mental model does not split.
- The `scheduled_at` guard makes scheduled notifications first-class in the safety net's correctness story, not a footnote.
- The recovery is conservative — re-enqueue only, no status change, no log churn. A future audit of `notification_logs` for a recovered row reads as if nothing went wrong (because nothing observable to the user did go wrong; the recovery is invisible).

**Negative:**

- Maximum latency for a `queued` orphan is the reconciler interval (1 minute) plus the threshold (5 minutes) = ~6 minutes. Same bound as the `pending` orphan in ADR-0011 — we accept this for the same reasons.
- The `scheduled_at` predicate adds one column to the query plan; the existing `idx_notifications_status_scheduled_at` index covers it, so the cost is negligible. We re-verified with `EXPLAIN` after migration.
- A user inspecting the trace endpoint during a recovery sees nothing new — only the original `created → queued` events. This is intentional (the recovery is not a transition) but documenting that intent matters; the API trace doc calls it out.
- Adding a fourth sweep pushed `Execute`'s cognitive complexity over Sonar's S3776 limit (16 > 15). A follow-up refactor extracted each sweep into its own private method (`sweepX`) so Execute is now a thin dispatcher. Six new tests cover every Find/handle error path across all four sweeps. The refactor is in the same branch; the cognitive-complexity issue is closed.

## Alternatives Considered

1. **Re-enqueue and transition `queued → queued` to refresh `updated_at`.** Considered to make the sweep idempotent — running the reconciler twice in quick succession would otherwise re-enqueue the same row twice. Rejected: status update brings UpdateStatus's `expected_source` guard into play, which is plumbing without value here. Instead, the second-run risk is bounded by the 5-minute threshold (`updated_at` from the original enqueue stays in place; the row will not re-appear in the sweep until 5 minutes after `Create` — which is after the recovery has either succeeded or itself failed).
2. **Combine the `pending` and `queued` sweeps into one** (`status IN ('pending', 'queued')`). Rejected: the two handlers differ — `pending` rows need a `MarkQueued` transition and a `queued` log entry; `queued` rows need only the enqueue. Combining would require branching inside the handler, which is the opposite of "one sweep, one shape."
3. **Drop the `scheduled_at` guard and rely on asynq's `unique-task` semantics to dedupe.** Rejected: asynq's unique-task TTL is designed for short retry windows, not multi-hour scheduled deliveries. A 30-minute scheduled notification would outlive any reasonable unique-task TTL we could set.
4. **Skip the sweep entirely; surface stuck rows via an alert and require manual recovery.** Rejected: the whole point of the reconciler is that recovery is automatic. A `StuckQueuedFound` metric paired with an alert is still useful (Phase 6 will add it) but as observability on top of automatic recovery, not as a replacement.
5. **Use asynq's own retry / re-delivery to handle the race.** Rejected: asynq sees the task as successfully delivered (the worker's claim returned `nil`). There is nothing for asynq to retry; from its point of view the work is done. Only the row's status reveals the truth.

## Related

- CLAUDE.md §3.11 (Reconciliation Safety Net) — now documents both halves of the dual-write race
- [ADR-0009](0009-atomic-status-claim.md) — the atomic claim that *causes* this race window (the claim's filter is `queued|retrying`, which is correct; the race is in the API-side ordering, not the claim)
- [ADR-0011](0011-reconciler-no-outbox.md) — covered the first half of the dual-write race
- `internal/application/reconcile_stuck_notifications.go` — `sweepStuckQueued`, `handleStuckQueued`
- `internal/adapters/postgres/sqlc/queries/notifications.sql` — `FindStuckQueued` query with the `scheduled_at` guard
- `internal/adapters/postgres/notification_repository_test.go` — `TestFindStuckQueued_HappyPath`, `TestFindStuckQueued_ExcludesFutureScheduled`, `TestFindStuckQueued_DBError_WrapsError`
- `E2E_REPORT.md` §J — live behavior entry for the new sweep
