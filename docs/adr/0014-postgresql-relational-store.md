# ADR-0014: PostgreSQL As The Relational Store (vs MySQL / MongoDB)

**Status:** Accepted
**Date:** 2026-05-26
**Deciders:** Ahmet Bora

## Context

[ADR-0002](0002-postgres-with-sqlc.md) chose `sqlc` over an ORM and named PostgreSQL in its title, but the actual decision it documents is the *generator-vs-ORM* one. The Postgres-vs-other-RDBMS question — "could you have shipped this on MySQL? MongoDB? CockroachDB?" — was treated as obvious and never written down. A peer code review on the post-fix repo asked for that paragraph explicitly: the project leans on Postgres-specific features in five concrete places, and a reviewer should not have to grep SQL to find out which.

The project's data layer takes hard dependencies on:

1. **`SELECT ... FOR UPDATE SKIP LOCKED`** — every reconciler sweep ([ADR-0011](0011-reconciler-no-outbox.md), [ADR-0013](0013-reconciler-stuck-queued-sweep.md)) uses it so multiple reconciler instances can scan in parallel without conflicting claims. Worker atomic claim ([ADR-0009](0009-atomic-status-claim.md)) is `UPDATE ... RETURNING` against the same concurrency model.
2. **Partial unique indexes** — `idx_notifications_idempotency_key UNIQUE ... WHERE idempotency_key IS NOT NULL AND idempotency_key <> ''` (the second-layer idempotency guard from [CLAUDE.md §3.9](../../CLAUDE.md), pinned by the Fix #1 conflict-detection path). Without the partial predicate every row would need a NOT NULL value, breaking the optional-key contract.
3. **JSONB** — `notification_logs.details` carries provider responses and reason strings. GIN-indexable, type-safe, query-able with `->>` / `@>` operators when forensic queries are needed.
4. **Transactional DDL** — migrations run inside a transaction so a partial failure leaves the schema at the previous version. Production rollback is a simple `migrate down 1`.
5. **`pgx/v5`** — mature, context-native, first-class Postgres driver for Go with binary protocol support and connection pooling. The `sqlc` generated code targets pgx natively.

The forces in choosing between Postgres and the obvious alternatives:

- **MySQL 8** — `SELECT ... FOR UPDATE SKIP LOCKED` is supported since 8.0.1 (good). But MySQL has no partial unique indexes (would have to fake it with a generated column + unique on the column, ugly) and its JSON type is less ergonomic than JSONB (no GIN-equivalent, slower indexing for ad-hoc queries). The project would compile but every concurrency-critical query would carry an asterisk.
- **MongoDB** — no `SKIP LOCKED` analog (the closest is `findAndModify` with optimistic retries), multi-document transactions are bounded to 60s and discouraged at scale, and the application's domain is naturally relational (notifications + batches + templates + logs are foreign-keyed). A document store would force schema-on-read patterns the use cases don't want.
- **CockroachDB** — Postgres wire-compatible, distributed by default, real `SELECT FOR UPDATE` (though SKIP LOCKED semantics differ subtly under their MVCC model). Genuinely interesting as a *future* horizontal-scale path; out of scope for the assessment, but worth naming as a non-breaking migration target.
- **SQLite** — sufficient for a single-process demo; excluded because the project ships three binaries (api / worker / reconciler) all writing concurrently, which collapses SQLite's writer-lock model.

## Decision

We use **PostgreSQL 16** as the relational store. The choice is justified by the five feature dependencies above, not by general preference. Each dependency maps to a concrete piece of the codebase:

| Feature | Used in |
|---|---|
| `FOR UPDATE SKIP LOCKED` | `internal/adapters/postgres/sqlc/queries/notifications.sql` — `ClaimForProcessing`, `FindOrphanedPending`, `FindStuckProcessing`, `FindOverdueRetrying`, `FindStuckQueued` |
| Partial unique index | `db/migrations/000001_initial_schema.up.sql` — `idx_notifications_idempotency_key UNIQUE ... WHERE` |
| JSONB | `db/migrations/000001_initial_schema.up.sql` — `notification_logs.details JSONB` |
| Transactional DDL | every `db/migrations/*.up.sql` runs inside `BEGIN`/`COMMIT` via `golang-migrate` |
| `pgx/v5` | `internal/adapters/postgres/*.go` — driver + `sqlc`-generated wrappers |

The Docker image is pinned to `postgres:16-alpine`. The connection string ships inline in `docker-compose.yml` (per [ADR-0010](0010-no-env-file.md)). Production deployments swap the image tag and override the DSN at the orchestration layer (Kubernetes ConfigMap, secrets manager).

## Consequences

**Positive:**

- The five feature dependencies above are first-class, not workarounds. Concurrency, idempotency, and forensic queries all use the native primitive instead of an emulation.
- `pgx/v5` + `sqlc` is the most mature SQL toolchain in Go right now. Generated query code is type-safe and benchmarked against alternatives in benchmarks the project doesn't have to repeat.
- The migration story is boring in the right way — `golang-migrate` against a single-node Postgres is a deeply-understood operation with rollback semantics that match production expectations.
- A future scale story has a concrete path: managed Postgres (RDS, Cloud SQL, Aurora), then read replicas, then sharding-aware tooling (Citus). None of those require a code rewrite.

**Negative:**

- Vendor lock at the relational layer: the application talks Postgres dialect SQL (partial indexes, `JSONB`, `SKIP LOCKED`) so a switch to MySQL would be a rewrite, not a config change. We accept this as the cost of using the native primitives instead of an LCD that works everywhere.
- The toolchain bias toward Postgres means a contributor unfamiliar with `JSONB` or partial-index syntax has a small learning ramp. Cushioned by the migration files being short, commented, and reviewable end-to-end.
- Distributed-by-default databases (CockroachDB, YugabyteDB) are not drop-in despite wire-compatibility — `SKIP LOCKED` semantics under their MVCC models differ enough that the reconciler queries would need re-verification. If the project's scale envelope grows past single-node Postgres, the migration is non-trivial but tractable; we name CockroachDB as the most likely target because the SQL surface overlaps most.

## Alternatives Considered

1. **MySQL 8** — rejected. `SKIP LOCKED` is there since 8.0.1 (good) but partial unique indexes are not (the idempotency-key guard would need a generated-column workaround) and `JSON` is weaker than `JSONB` for forensic queries on `notification_logs.details`. Every concurrency-critical query would compile under MySQL but with an asterisk; for a project whose load-bearing invariants are the reconciler sweeps + atomic claim, that asterisk is the deal-breaker.
2. **MongoDB** — rejected. No `SKIP LOCKED` analog (optimistic-retry patterns instead), multi-document transactions are bounded and discouraged at scale, and the data model is relational. A document store forces a schema-on-read pattern the use cases don't want, and the reconciler would have to be rebuilt around change-streams which is its own architectural axis.
3. **CockroachDB** — deferred. Postgres wire-compatible, naturally distributed, and a credible future scale path. Out of scope for the assessment because (a) the SKIP LOCKED semantics under its MVCC model would need re-verification per query and (b) the operational tooling story (backups, migrations, monitoring) is heavier than the assessment's "one Docker compose" promise. Worth a follow-up ADR if the project ever hits the limits of single-node Postgres.
4. **SQLite** — rejected. Sufficient for a single-process demo; the project ships three binaries writing concurrently, which collapses under SQLite's writer-lock model. Even with WAL mode the contention story is not where this project lives.
5. **A document store + a relational store side by side** — rejected as scope creep. Two storage systems multiply the dual-write story already documented in [ADR-0011](0011-reconciler-no-outbox.md); we deliberately chose a single relational store + Redis (for queue + cache) for the same reasons we rejected the outbox pattern.

## Related

- [ADR-0002](0002-postgres-with-sqlc.md) — chose sqlc over an ORM on top of Postgres; this ADR fills the *why-Postgres* gap that one left open
- [ADR-0009](0009-atomic-status-claim.md) — uses `UPDATE ... RETURNING` with the `WHERE status IN (...)` filter pattern
- [ADR-0011](0011-reconciler-no-outbox.md) — uses `FOR UPDATE SKIP LOCKED` for the reconciler sweeps
- [ADR-0013](0013-reconciler-stuck-queued-sweep.md) — extends the SKIP LOCKED pattern to the fourth sweep
- [ADR-0010](0010-no-env-file.md) — the connection string lives inline in `docker-compose.yml`, not in a `.env`
- `db/migrations/` — every migration is a `BEGIN`/`COMMIT`-wrapped SQL file, depending on transactional DDL
- `internal/adapters/postgres/` — pgx-based adapter; `sqlc/queries/notifications.sql` is the SKIP-LOCKED home
