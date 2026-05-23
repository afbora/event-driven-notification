# Skill: add-migration

## Purpose

Add a database migration (DDL or DML) that is versioned, reversible, and compatible with the existing sqlc-generated query layer.

## When To Use

- You are changing the database schema: adding/altering/dropping tables, columns, indexes, constraints, or types.
- You are seeding required reference data (rare; prefer code-based seeds).

## Prerequisites

- `golang-migrate` is installed locally or available via `make migrate-create`.
- The change is necessary; you have considered whether it can be done with an existing schema first.
- You have an entry in the relevant ADR if this is a significant schema change.

## Steps

### 1. Create the migration file pair

```sh
make migrate-create name=add_notifications_priority_index
```

This creates two files in `api/migrations/`:

- `NNNNNN_add_notifications_priority_index.up.sql`
- `NNNNNN_add_notifications_priority_index.down.sql`

Both files **must** exist and **must** contain real, reversible SQL. An empty `down` migration is a defect.

### 2. Write the `up` migration

Follow these rules:

- One logical change per migration. Do not bundle "add column X and rename table Y" together.
- Use `IF NOT EXISTS` / `IF EXISTS` when adding/removing constraints to make migrations idempotent for crash recovery.
- For new columns: `ADD COLUMN name TYPE NOT NULL DEFAULT <value>` so existing rows are valid.
- For indexes on large tables (notifications): `CREATE INDEX CONCURRENTLY` to avoid locks. Wrap with explicit transaction handling because `CONCURRENTLY` cannot run in a transaction.
- For renames: avoid them. Add new, copy, drop old in separate migrations. Renames break in-flight deployments.

Example:

```sql
-- up.sql
ALTER TABLE notifications
    ADD COLUMN IF NOT EXISTS priority TEXT NOT NULL DEFAULT 'normal';

ALTER TABLE notifications
    ADD CONSTRAINT notifications_priority_check
    CHECK (priority IN ('low', 'normal', 'high'));

CREATE INDEX IF NOT EXISTS idx_notifications_priority
    ON notifications (priority)
    WHERE status = 'pending';
```

### 3. Write the `down` migration

The `down` undoes the `up`. It must work even if the schema has had subsequent (unrelated) migrations applied.

```sql
-- down.sql
DROP INDEX IF EXISTS idx_notifications_priority;
ALTER TABLE notifications DROP CONSTRAINT IF EXISTS notifications_priority_check;
ALTER TABLE notifications DROP COLUMN IF EXISTS priority;
```

### 4. Test the migration in both directions

```sh
make migrate-up
make migrate-down
make migrate-up
```

Verify against the test database. Catch silent failures (column not dropped, index still exists) with explicit `SELECT` checks if needed.

Commit: `feat(migrations): add <description> migration`

### 5. Update sqlc queries if the schema change affects queries

If you added a column you want to read or write, update `api/internal/adapters/postgres/sqlc/queries/<resource>.sql`:

```sql
-- name: CreateNotification :one
INSERT INTO notifications (id, recipient, channel, content, priority, status, ...)
VALUES (...)
RETURNING *;
```

Regenerate Go code:

```sh
make sqlc-generate
```

Commit: `chore(adapter/postgres): regenerate sqlc for new schema`

### 6. Update the repository implementation if needed

The generated sqlc code changes will cause compile errors in `notification_repo.go` if it maps between domain types and database types. Fix the mappings.

Commit: `feat(adapter/postgres): use new <column> in repository`

### 7. Add integration test coverage for the new schema behaviour

In `tests/integration/postgres/notification_repo_test.go`, add a test that exercises the new column or index. Use the testcontainers Postgres.

Commit: `test(adapter/postgres): cover <new behaviour> in integration test`

## Verification

- [ ] `make migrate-up` succeeds against a clean database.
- [ ] `make migrate-down` succeeds and leaves no leftover artifacts.
- [ ] `make migrate-up` succeeds again after a down (idempotency check).
- [ ] `make sqlc-generate` runs cleanly.
- [ ] `make test` and `make test-integration` pass.
- [ ] No existing migration files were modified — migrations are immutable.

## Common Mistakes

- Empty or stub `down` migration. Always reversible.
- Adding NOT NULL columns without a default value. Existing rows will fail.
- Forgetting to regenerate sqlc after a schema change. Compile errors will surprise you later.
- Editing a migration that has been merged to `main`. Create a new one instead.
- Using `CREATE INDEX` (blocking) on a production-size table instead of `CONCURRENTLY`.
- Bundling unrelated changes into one migration, making it impossible to roll back partially.
- Renaming columns or tables. Always: add new, backfill, switch reads, drop old — across multiple migrations and deployments.
