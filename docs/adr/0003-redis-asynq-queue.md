# ADR-0003: Redis + asynq As The Async Queue

**Status:** Accepted
**Date:** 2026-05-23
**Deciders:** Ahmet Bora

## Context

The brief specifies:

- "Process notifications asynchronously via queue workers."
- "Implement rate limiting: maximum 100 messages per second per channel."
- "Priority queue support (high, normal, low)."
- "Idempotency support to prevent duplicate sends."
- "Burst traffic (flash sales, breaking news)."

We need a queue that supports priorities, exponential backoff retry, scheduled tasks (for the bonus "Scheduled Notifications" item), unique task deduplication (for idempotency), and per-channel rate limiting. We also want operational tooling — DLQ inspection, retry control, in-flight visibility.

Candidate queue technologies:

- **RabbitMQ** — battle-tested, AMQP semantics, but requires a dedicated container (memory- and config-heavy) and per-feature glue code in Go.
- **Apache Kafka** — overkill for this scale; oriented around durable log streaming, not job processing.
- **NATS JetStream** — modern, lightweight, good Go ergonomics, but lacks first-class priority queues and the management UI is thin.
- **Redis Streams** — primitives are there but every higher-level concern (retry, DLQ, rate limit, priority) has to be built on top.
- **asynq** (`github.com/hibiken/asynq`) — Redis-backed Go-native task queue. Ships with priorities, exponential backoff, scheduled tasks, unique tasks, rate limiting, and `asynqmon` as a first-class management UI.

The forces:

- Redis is already in the stack (rate limiter, idempotency cache, WebSocket pub/sub backbone). Adding a queue on top means **one** external infrastructure piece, not two.
- We want a queue management UI for the reviewer without writing one ourselves (see ADR-0005 for the broader operational UI philosophy).

## Decision

The queue is implemented on **asynq** over **Redis 7**. `asynqmon` is included in `docker-compose.yml` for operational visibility (queue depth, in-flight tasks, retry inspection, DLQ).

Concrete settings (codified in `internal/adapters/asynq/`):

- Three priority queues: `high`, `default`, `low`, weighted 6 / 3 / 1.
- Retry policy: 5 attempts, exponential backoff `30s * 2^(attempt-1) + jitter(0, 30s)`.
- Unique tasks per `idempotency_key` with 24-hour TTL — second layer of idempotency on top of the API-level Redis cache (CLAUDE.md §3.9).
- Scheduled tasks via `client.EnqueueIn(...)` for the future-delivery bonus feature.
- Per-channel rate limit (outbound) implemented in the worker before each provider call (ADR-0006-adjacent — see §2.6 of CLAUDE.md).

## Consequences

**Positive:**

- One external dependency (Redis) covers four concerns: queue, rate limit, idempotency cache, WebSocket pub/sub.
- `asynqmon` gives the reviewer a queue inspection UI without us building one.
- Native priority queues match the brief literally.
- Scheduled tasks satisfy the "Scheduled Notifications" bonus item for free.

**Negative:**

- Redis becomes a critical piece of infrastructure. If Redis goes down, the worker pauses and the API returns 503 via the readiness probe.
- asynq is Go-only. A future polyglot setup would need a different queue. We accept this trade-off because the rest of the stack is Go.
- We are coupled to asynq's task payload format. The `ports.Queue` interface insulates application code from this, but the adapter is non-trivial to swap.

## Alternatives Considered

1. **RabbitMQ** — rejected. Adds a heavyweight container and we'd have to build priorities and DLQ tracking on top of standard AMQP. The brief points at lighter solutions.
2. **Kafka** — rejected. Designed for durable streaming, not job dispatch. Operationally heavy (ZooKeeper or KRaft), and the consumer model doesn't fit job semantics naturally.
3. **NATS JetStream** — close runner-up. Lightweight, fast, durable. Rejected because asynq's priority queues + management UI + Go ergonomics are better matched to this brief.
4. **Redis Streams directly** — rejected. We'd reinvent asynq's retry / DLQ / priority machinery. asynq is the right level of abstraction.
5. **AWS SQS / Google Cloud Tasks** — rejected. Vendor-locked and require credentials. The brief asks for a self-contained `docker-compose up`.

## Related

- CLAUDE.md §2.6 (two-layer rate limiting), §3.9 (idempotency), §5 (data flow)
- ADR-0010 (no `.env` — Redis credentials are inline)
- `internal/adapters/asynq/`
- `internal/adapters/redis/` (rate limiter, idempotency store, status broadcaster)
