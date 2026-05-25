# ADR-0011: Reconciler-Based Dual-Write Mitigation; No Outbox Pattern

**Status:** Accepted
**Date:** 2026-05-23
**Deciders:** Ahmet Bora

## Context

When the API persists a notification and then enqueues it for processing, two writes happen in sequence to two different systems:

```
1. INSERT INTO notifications (..., status='pending')   -- PostgreSQL
2. asynq.Enqueue(notificationID, ...)                  -- Redis
```

If step 2 fails after step 1 succeeds — Redis transient outage, network blip, API process crash between the two calls — the notification is **orphaned**: persisted but never enqueued. It will sit in `status='pending'` forever unless something rescues it.

This is the classic **dual-write problem**. The textbook solutions are:

1. **Outbox Pattern.** Add an `outbox` table; the API writes the notification and the outbox row in a **single transaction**. A separate poller reads the outbox and publishes to the queue, marking rows as published.
2. **Two-Phase Commit (2PC).** Distributed transaction across Postgres and Redis. Postgres supports 2PC; Redis does not in any usable way. Not viable.
3. **Reverse the order**: enqueue first, then persist. Rejected because the queue payload includes the notification ID, which doesn't exist yet. Bootstrapping the ID separately introduces its own problems.
4. **Reconciliation safety net.** A background process periodically sweeps for orphaned `pending` rows and re-enqueues them.

The forces in choosing between outbox and reconciliation:

- We already have a reconciler in the design (CLAUDE.md §3.11) for **other** failure modes — worker crashes leave `processing` rows stuck; Redis flushes lose `retrying` schedules. Adding orphaned `pending` rows to its sweep is a small extension.
- The outbox pattern adds a new table, a new poller process, and a new failure mode (outbox poller falling behind). For a project of this size and scope, the operational overhead is non-trivial.
- Outbox guarantees lower latency for the rescue (poller usually runs every few seconds); reconciler runs every minute. Five-minute orphan detection is acceptable for notification semantics.
- Notifications are not financial transactions. A five-minute delay before rescue is acceptable; a missed notification is not.

## Decision

We do **not** implement the Outbox Pattern. We use the existing reconciliation safety net (ADR-0009 adjacent; CLAUDE.md §3.11) to also catch the dual-write race:

The reconciler (`cmd/reconciler`, running once per minute) sweeps three categories:

1. **Stuck processing:** notifications in `status='processing'` for more than 5 minutes → mark `failed` with reason `worker_timeout` (or re-enqueue if policy says so).
2. **Overdue retrying:** notifications in `status='retrying'` whose `next_retry_at` is past by more than 1 minute → re-enqueue.
3. **Orphaned pending** (this ADR): notifications in `status='pending'` for more than 5 minutes → **re-enqueue**.

All three queries use `SELECT ... FOR UPDATE SKIP LOCKED` so multiple reconciler instances can run safely in parallel.

The five-minute threshold for orphaned pending is deliberately conservative: the normal `pending → queued` transition happens in milliseconds, so anything sitting in `pending` for five minutes is almost certainly a dual-write casualty (or a bug we want to surface as `OrphanedPendingFound` metric).

## Consequences

**Positive:**

- No new table, no new poller, no new container. The defense reuses infrastructure that exists for other reasons.
- The dual-write race is closed with a known maximum data delay of ~5 minutes — acceptable for notification semantics.
- A single mental model for "things going wrong in the worker": the reconciler is the safety net for **all** of them.
- Easy to surface via metrics — every reconciler pass emits how many rows it touched, by category.

**Negative:**

- Maximum latency for an orphaned notification is the reconciler interval (1 minute) plus the orphan threshold (5 minutes) = up to ~6 minutes from API success to actual delivery. For a system whose normal latency is sub-second, this is a 360× outlier. We accept it on the basis that the case is rare.
- A user could observe `status='pending'` for several minutes on the trace endpoint during such a recovery. The status semantics need to document this clearly.
- If the reconciler itself is down for an extended period, orphaned rows accumulate. Mitigation: the `ReconcilerNotRunning` alert fires after 5 minutes of inactivity (CLAUDE.md §12.5).

## Alternatives Considered

1. **Outbox pattern** — rejected for this project. Operationally heavier (new table, new poller, new metrics, new failure mode). The recovery latency advantage (seconds vs minutes) does not justify the complexity at this scale and is not required by the brief.
2. **Synchronous 2PC across Postgres + Redis** — rejected. Redis does not support 2PC meaningfully. Even if it did, 2PC is operationally unloved in production for good reasons (latency under partition, locking behavior).
3. **Enqueue first, then persist** — rejected. The queue payload references the notification ID; we would have to generate the ID before persisting, then handle the case where the persist fails after the enqueue succeeded (a worker would process a notification that doesn't exist in the DB).
4. **Retry the dual write inside the same request** — partial mitigation but doesn't help if the API process crashes between the two calls. We do already use a bounded retry-with-jitter on the enqueue side as belt-and-braces, but the reconciler is the load-bearing defense.

## Related

- CLAUDE.md §3.11 (Reconciliation Safety Net), §5 (Failure paths)
- ADR-0009 (Atomic Status Claim) — closes a different race (worker double-processing)
- `cmd/reconciler/main.go`
- `internal/application/reconcile_stuck_notifications.go`
- `internal/adapters/postgres/notification_repository.go` (`FindOrphanedPending`, `FindStuckProcessing`, `FindOverdueRetrying`)
