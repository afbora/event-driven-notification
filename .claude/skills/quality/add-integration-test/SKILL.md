# Skill: add-integration-test

## Purpose

Add an integration test that exercises an adapter (Postgres, Redis, Asynq) against a real backing service spun up via `testcontainers-go`.

## When To Use

- You are testing repository code, queue code, idempotency store, or rate limiter behaviour.
- Unit tests with fakes cannot catch the bug class you are worried about (SQL syntax, transaction semantics, Redis Lua atomicity, etc.).

## Prerequisites

- Docker is running locally (testcontainers needs it).
- The test target file is in `tests/integration/<adapter>/` (not next to source — keeps integration tests separate).
- Build tag `//go:build integration` is at the top of the file.

## Steps

### 1. Create the test file with the integration build tag

```go
//go:build integration

package postgres_test

import (
    "context"
    "testing"

    "github.com/stretchr/testify/require"
)
```

### 2. Use the shared test container helper

A helper in `tests/integration/testhelpers/` spins up Postgres/Redis once per test package and shares the connection. Do not start a fresh container per test — too slow.

```go
func TestNotificationRepository_Create(t *testing.T) {
    ctx := context.Background()
    pool := testhelpers.Postgres(t) // shared, migrated, cleaned per test

    repo := postgres.NewNotificationRepository(pool)
    // ... test
}
```

### 3. Reset state between tests

Use `t.Cleanup` and either:

- A transaction that is rolled back at the end (preferred for read-write isolation).
- A truncate of touched tables (acceptable for sequential tests).

```go
t.Cleanup(func() {
    _, _ = pool.Exec(ctx, "TRUNCATE notifications, notification_attempts RESTART IDENTITY CASCADE")
})
```

### 4. Make tests parallel-safe

If you use `t.Parallel()`, each test must have isolated state. Use random IDs in test data. Avoid shared rows.

If you cannot isolate, do not use `t.Parallel()`. Sequential is acceptable; flaky is not.

### 5. Test real failure modes

Integration tests are where you catch:

- Unique constraint violations.
- Cascading deletes.
- Transaction isolation behaviour.
- `SELECT FOR UPDATE` blocking.
- Lua script atomicity under contention.
- Connection pool exhaustion under load.

Write tests for these. Unit tests cannot.

### 6. Document slow tests

If a test takes > 1 second, add a comment explaining why. Reviewers will wonder.

### 7. Run with the integration tag

```sh
make test-integration
```

The Makefile sets `-tags=integration`. Regular `make test` skips these files.

## Verification

- [ ] `make test-integration` passes.
- [ ] Test runs in under 2 seconds (or has documented justification).
- [ ] State is cleaned up between tests.
- [ ] No flakiness across 5 consecutive runs.

## Common Mistakes

- Forgetting the `//go:build integration` tag, causing the test to run in `make test` and fail without Docker.
- Starting a new container per test (slow, wasteful).
- Sharing state across tests, causing order-dependent failures.
- Using `time.Sleep` to wait for async operations. Use `require.Eventually`.
- Hardcoding connection strings instead of using the testcontainers-provided endpoint.
- Asserting on the test container being healthy as part of the test logic. The helper handles that.
