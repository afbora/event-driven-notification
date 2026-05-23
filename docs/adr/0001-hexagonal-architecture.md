# ADR-0001: Hexagonal Architecture (Ports & Adapters)

**Status:** Accepted
**Date:** 2026-05-23
**Deciders:** Ahmet Bora

## Context

The notification system must be evaluated against several criteria that are sensitive to how code is organised: testability, separation of concerns, ease of swapping infrastructure, and the reviewer's ability to read the codebase top-down. We considered three styles:

1. **MVC-style layering** (controllers / services / repositories) — familiar but tends to leak framework types (HTTP request structs, database rows) into business logic.
2. **Clean Architecture** with concentric layers and explicit `Entities → Use Cases → Interface Adapters → Frameworks` rings — close to what we want but heavier than necessary for a service this size.
3. **Hexagonal Architecture (Ports & Adapters)** as popularised by Alistair Cockburn — interfaces define the edges; adapters implement them; the domain sits in the middle knowing nothing about the outside world.

The forces:

- **Testability** — we want the domain and application layers to be unit-testable without spinning up Postgres, Redis, or HTTP servers.
- **Substitutability** — the brief calls for SMS / Email / Push channels and explicitly lists "future channels" as a possible extension. Provider implementations must be swappable.
- **Reviewer readability** — the reviewer should be able to point at any file and say what layer it belongs to.

## Decision

The codebase is organised around the Ports & Adapters pattern, with a **physical** boundary enforced by directory structure and import rules:

```
internal/
  domain/          # entities, value objects, sentinel errors — pure Go, stdlib only
  application/     # use cases — orchestrate domain via ports
  ports/           # interfaces (Repository, Queue, Provider, ...)
  adapters/        # concrete implementations of ports
  infrastructure/  # cross-cutting plumbing (config, logger, metrics, tracing)
```

**Import rules** (enforced by the `check-hexagonal-boundaries` skill):

- `internal/domain/` imports **only** the Go standard library.
- `internal/application/` imports `internal/domain/` and `internal/ports/`.
- `internal/adapters/` imports `internal/ports/`, `internal/domain/`, and any external library it adapts (pgx, asynq, go-redis, chi).
- `cmd/*/main.go` is the only place that knows about concrete adapters; it wires them into use cases via the port interfaces.

A reviewer should be able to delete `internal/adapters/postgres/` and the domain still compiles.

## Consequences

**Positive:**

- Domain and application packages are testable with hand-written fakes for ports; no integration test infrastructure required for ≥90% of business logic coverage.
- Adding a new notification channel means writing one new `Provider` implementation — no switch statement edits in the application layer (see ADR-0004).
- A reviewer can grep `internal/domain/` and `internal/application/` to read the business logic without HTTP, SQL, or queue noise.
- Swapping infrastructure (e.g., Redis → NATS for the queue) touches only the relevant adapter package.

**Negative:**

- Up-front boilerplate: every external interaction needs a port interface defined before the first adapter is written. For a service this small the overhead is real.
- Two indirection layers between the HTTP handler and the database (handler → use case → repository). Junior engineers sometimes find this counter-intuitive at first.
- Naming discipline matters; without it the structure decays. The `check-hexagonal-boundaries` skill is the safeguard.

## Alternatives Considered

1. **MVC layering** — rejected because it doesn't enforce a hard boundary between business logic and infrastructure types. A controller method can accidentally take a `*gorm.DB` parameter; nothing stops it.
2. **Clean Architecture (Uncle Bob)** — close but the four concentric rings (Entities / Use Cases / Interface Adapters / Frameworks) duplicate the ports/adapters split with extra ceremony. We use the simpler vocabulary.
3. **Flat package per feature** (`internal/notifications/`, `internal/templates/`) — works at smaller scale but co-locates HTTP, DB, and domain code in one package, which makes it easy to leak infrastructure types into business logic over time.

## Related

- CLAUDE.md §3.3 (Domain Purity), §5 (Architecture Overview)
- `.claude/skills/quality/check-hexagonal-boundaries/SKILL.md`
- ADR-0002 (PostgreSQL + sqlc) — shows the adapter pattern for persistence
- ADR-0004 (Provider Strategy) — shows the port pattern for delivery
