# ADR-0009: Atomic Status Claim In The Worker

**Status:** Accepted
**Date:** 2026-05-23
**Deciders:** Ahmet Bora

## Context

Three independent forces can cause the same notification to be processed twice if the worker is naive:

1. **Worker horizontal scaling.** Multiple worker instances consume from the same asynq queue. asynq's primary mechanism for at-least-once delivery is the Redis "in-flight" set; under most conditions exactly one worker gets a given task. Under some conditions — a worker crash mid-processing, a Redis network partition — asynq may redeliver a task.
2. **Reconciler re-enqueue.** The reconciler (ADR-0011) is a safety net for stuck notifications. It re-enqueues notifications that look orphaned. If the reconciler's heuristic fires while a worker is actually processing the task (e.g., a slow provider call), we have two workers trying to send the same message.
3. **At-least-once semantics in asynq.** asynq guarantees at-least-once, not exactly-once. Network blips, redis HA failovers, and process crashes can all cause a task to be delivered more than once.

For a notification system, "sent twice" is a visible user-facing defect. A duplicate SMS costs money and trust.

## Decision

The worker, immediately after dequeuing a task and **before** any provider call, performs an **atomic status claim** against the database:

```sql
UPDATE notifications
SET    status = 'processing',
       updated_at = NOW()
WHERE  id = $1
  AND  status IN ('queued', 'retrying')
RETURNING *;
```

The worker then checks `result.RowsAffected`:

- **`== 1`** — this worker claimed the notification; proceed to the provider call.
- **`== 0`** — another worker (or a redelivery, or the reconciler) already claimed it. Log a structured warning and exit cleanly. Do not call the provider.

The same pattern applies on terminal status updates:

```sql
UPDATE notifications
SET    status = 'delivered',   -- or 'failed', 'retrying'
       updated_at = NOW(),
       /* result fields */
WHERE  id = $1
  AND  status = 'processing'
```

If the second `UPDATE` returns zero rows affected, something has gone wrong (the row was modified by another actor while we were processing) — the worker logs an error and surfaces it via metrics. This is rare in practice but cheap to detect.

## Consequences

**Positive:**

- **Exactly-once user-visible delivery semantics** in the face of at-least-once queue delivery, worker scaling, and reconciler races.
- Eliminates the most common notification-system bug (duplicate sends) without distributed locks, leader election, or distributed transactions.
- The defense is in the database, not in the worker logic, so it is impossible for a future worker bug to bypass it.

**Negative:**

- One extra database write per task before the provider call. At the brief's "millions per day" scale this is a few hundred extra writes per second — well within Postgres's capacity on modest hardware, but real cost.
- Status transitions are now slightly more rigid: a worker cannot move a notification from `delivered` back to anywhere. This is correct (terminal states are terminal) but worth documenting.
- The status state machine becomes load-bearing. ADR-relevant changes to the state machine must consider the atomic-claim WHERE clauses.

## Alternatives Considered

1. **Redis distributed lock** (e.g., Redlock) — rejected. Distributed locks have well-known correctness issues; we would introduce a new failure mode (lock not released after worker crash, lock acquired by two workers due to clock skew). The DB-level UPDATE is simpler, correct by construction, and uses infrastructure we already have.
2. **Database advisory lock** (`pg_advisory_lock`) — rejected. Advisory locks would work but require careful release on every code path, including panics. The conditional UPDATE has no equivalent burden.
3. **Asynq unique-task TTL alone** — rejected as the sole defense. asynq's uniqueness applies at enqueue time, but the reconciler re-enqueues, and asynq itself can redeliver. Unique-task TTL is a useful additional layer (it dedups at enqueue) but it does not protect against the worker-side races.
4. **Skip the claim, accept rare duplicates** — rejected. "Rare duplicates" is a user-facing defect, particularly painful for SMS (cost) and Push (annoyance). The cost of the extra DB write is far less than the cost of one duplicate user-visible notification.

## Related

- CLAUDE.md §3.10 (Atomic Status Claim Prevents Double Processing)
- ADR-0003 (Redis + asynq) — context on at-least-once semantics
- ADR-0011 (Reconciler) — the third source of potential double-processing
- `internal/adapters/postgres/notification_repository.go` (`ClaimForProcessing` method)
- `internal/application/process_notification.go` (uses the claim)
