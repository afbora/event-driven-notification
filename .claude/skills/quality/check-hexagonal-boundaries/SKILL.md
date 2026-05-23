# Skill: check-hexagonal-boundaries

## Purpose

Verify that the hexagonal architecture boundaries are intact: `domain` and `application` packages depend only on stdlib and each other, never on adapters.

## When To Use

- Before merging a PR that touches `internal/domain/` or `internal/application/`.
- Periodically (CI runs this check automatically).
- When you suspect an inadvertent import has been introduced.

## Steps

### 1. Run the import check

```sh
make check-boundaries
```

This runs a script that lists external imports in domain and application packages:

```sh
go list -f '{{ join .Imports "\n" }}' ./internal/domain/... ./internal/application/...
```

### 2. Inspect the output

The allowed imports are:

- Go stdlib (anything starting with a lowercase letter and no dots before the first slash).
- This module's own packages (`github.com/<repo>/api/internal/domain/...`, `.../application/...`, `.../ports/...`).
- Vendored testing utilities (`github.com/stretchr/testify/...`) — test files only.

Forbidden imports include but are not limited to:

- `database/sql` or any DB driver
- `net/http` (must go through ports)
- `github.com/hibiken/asynq`
- `github.com/redis/go-redis/...`
- `github.com/jackc/pgx/...`
- `go.opentelemetry.io/...` (instrumentation is at adapter layer)
- `github.com/prometheus/...`

If any forbidden import appears, you have a boundary violation.

### 3. Identify the violation

Find the file with the bad import:

```sh
grep -r "database/sql" ./internal/domain/ ./internal/application/
```

### 4. Decide the right fix

| Symptom | Fix |
|---|---|
| Domain code calls a database | Add a port; let an adapter implement it. |
| Application code constructs an HTTP request | Add a port (e.g., `WebhookCaller`); the adapter wraps `net/http`. |
| Application code reads from Redis | Use the `IdempotencyStore` or `RateLimiter` port. |
| Domain uses `time.Now()` directly | Inject a `Clock` port. |
| Application logs with `slog` directly | This may be acceptable if logging via the slog interface only; verify with the human. |

### 5. Add the missing port

If no existing port covers the need, add one in `internal/ports/`:

```go
package ports

import "context"

// WebhookCaller dispatches an outbound HTTP POST to the configured URL.
type WebhookCaller interface {
    Call(ctx context.Context, url string, body []byte) (*Response, error)
}
```

Commit: `feat(ports): add WebhookCaller interface`

### 6. Move the implementation to an adapter

Move the code that used `net/http` (or other forbidden import) to `internal/adapters/...`. The adapter implements the new port.

### 7. Re-run the check

```sh
make check-boundaries
```

Output should be clean.

### 8. Wire up the new adapter in `main.go`

The application no longer constructs the implementation; `main.go` does, then passes it in.

## Verification

- [ ] `make check-boundaries` returns no violations.
- [ ] All tests still pass.
- [ ] The new port has at least one implementation (the production adapter) and one fake (for unit tests).
- [ ] The fake lives in `internal/testing/fakes/`.

## Common Mistakes

- "It's just one little import." Once one slips in, more follow. The check is binary.
- Implementing a port-like interface in the application package itself. Ports go in `internal/ports/`; implementations in `internal/adapters/`.
- Forgetting that test files count too. A domain test importing `net/http` may indicate the domain is doing something it should not.
- Adding the port but leaving the old direct import in place "just in case." Remove it.
- Using `go.mod` `replace` directives or build tags to hide violations. The check should be authoritative.
