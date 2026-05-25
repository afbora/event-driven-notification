# Architectural Decision Records (ADRs)

This directory contains the architectural decisions made for this project, in [Michael Nygard's format](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions). Each ADR is immutable once accepted; superseding an ADR means adding a new one that references it.

## Index

| ADR | Title | Status |
|-----|-------|--------|
| [0001](0001-hexagonal-architecture.md) | Hexagonal Architecture (Ports & Adapters) | Accepted |
| [0002](0002-postgres-with-sqlc.md) | PostgreSQL With sqlc, No ORM | Accepted |
| [0003](0003-redis-asynq-queue.md) | Redis + asynq As The Async Queue | Accepted |
| [0004](0004-provider-strategy-pattern.md) | Provider Strategy Pattern For Channels | Accepted |
| [0005](0005-no-custom-frontend.md) | No Custom Frontend; Operational UI Stack Instead | Accepted |
| [0006](0006-websocket-for-realtime-updates.md) | WebSocket For Real-Time Status Updates | Accepted |
| [0007](0007-failure-handling-interpretation.md) | "Failure Handling" Interpreted As End-To-End Failure Response | Accepted |
| [0008](0008-three-binaries-one-image.md) | Three Binaries (api, worker, reconciler) Packaged In One Image | Accepted |
| [0009](0009-atomic-status-claim.md) | Atomic Status Claim In The Worker | Accepted |
| [0010](0010-no-env-file.md) | No `.env` File; All Configuration Inline In docker-compose.yml | Accepted |
| [0011](0011-reconciler-no-outbox.md) | Reconciler-Based Dual-Write Mitigation; No Outbox Pattern | Accepted |
| [0012](0012-tracer-port.md) | Wrap OpenTelemetry Behind ports.Tracer To Keep the Application Layer Stdlib-Pure | Accepted |

## Adding A New ADR

See [`.claude/skills/operations/add-adr/SKILL.md`](../../.claude/skills/operations/add-adr/SKILL.md) for the procedure and template. The short version:

1. Pick the next 4-digit number.
2. Use the Nygard template (Title / Status / Date / Deciders / Context / Decision / Consequences / Alternatives Considered / Related).
3. Reference the ADR from the code or document that implements the decision.
4. Update this index.
5. Commit with `docs(adr): add ADR-NNNN on <topic>`.
