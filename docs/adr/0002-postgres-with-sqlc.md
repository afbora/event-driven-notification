# ADR-0002: PostgreSQL With sqlc, No ORM

**Status:** Accepted
**Date:** 2026-05-23
**Deciders:** Ahmet Bora

## Context

The system needs durable storage for notifications, batches, templates, and per-notification audit logs. The brief calls for "millions of notifications daily," so the persistence layer must scale horizontally with sensible indexes and avoid runtime reflection overhead.

The Go ecosystem offers several flavours of database access:

- **GORM / Ent** — full-featured ORMs with auto-migrations and chainable query DSLs.
- **sqlx** — thin wrapper over `database/sql`, struct-tag-based scanning.
- **pgx** — the fastest Postgres driver in Go, raw SQL + connection pooling.
- **sqlc** — generates type-safe Go code from raw SQL files at build time.

The forces:

- We want SQL to be the source of truth (the reviewer can audit one `.sql` file per query).
- We want type safety without runtime reflection on hot paths.
- We want predictable query plans; ORM-generated SQL is often pathological at scale.
- We want versioned, reversible schema changes that are language-agnostic.

## Decision

The persistence layer uses **PostgreSQL 16** as the database, **pgx/v5** as the driver, **sqlc** to generate type-safe Go from hand-written SQL, and **golang-migrate** for versioned migrations.

Concretely:

- `internal/adapters/postgres/sqlc/queries/*.sql` holds named queries. Each has `-- name: ListNotificationsByStatus :many` annotations that sqlc reads.
- `make sqlc` regenerates `internal/adapters/postgres/sqlc/*.go` with one function per named query, typed parameters in, typed structs out.
- `db/migrations/NNNNNN_*.up.sql` and `*.down.sql` hold schema changes. Every `up` has a `down`.
- The application layer never sees `*sql.Rows` or `pgx.Conn` — repositories return domain types only (`*Notification`, `Batch`, etc.).

## Consequences

**Positive:**

- Zero runtime reflection on the data path; the cost of a query is the cost of pgx's binary protocol plus the marshal into a struct sqlc generated for that exact column set.
- SQL is reviewable as SQL. A DBA can read `queries/notifications.sql` without learning Go.
- Compile-time safety: a query parameter rename or schema change immediately breaks Go code at the right call sites.
- Migrations are language-agnostic — a future Python/Rust service could share the schema using the same `golang-migrate`-compatible files.

**Negative:**

- A small amount of boilerplate per query (write the SQL, write the named-query annotation, run sqlc).
- sqlc's generated code is committed to the repo; a refresh after a query change must not be forgotten (CI catches it via `make sqlc && git diff --exit-code`).
- No fancy query builder; multi-tenant dynamic-WHERE filters need explicit query variants instead of a chainable DSL.

## Alternatives Considered

1. **GORM** — rejected. Reflective struct tags, surprising auto-migration semantics, query patterns that don't always survive `EXPLAIN`. The "ORM tax" is real and the project goal is production-grade engineering, not feature-quick prototyping.
2. **Ent** — schema-first, generated client, much like sqlc in spirit. Rejected because it pulls a whole framework (graph traversal, custom DSL) for what is mostly straightforward CRUD plus a few JOINs.
3. **`database/sql` + manual scan** — possible but every repository method becomes repetitive boilerplate. sqlc gives the same result with one extra build step.
4. **pgx without sqlc** — clean and idiomatic, but you give up the compile-time guarantee that the Go struct matches the column list of the query. sqlc is worth the small extra step.

## Related

- CLAUDE.md §6 (Technology Stack), §11 (Database Conventions)
- `.claude/skills/core/add-migration/SKILL.md`
- ADR-0001 (Hexagonal Architecture) — the repository ports defined there are implemented in `internal/adapters/postgres/`
