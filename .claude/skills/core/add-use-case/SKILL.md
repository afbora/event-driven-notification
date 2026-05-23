# Skill: add-use-case

## Purpose

Add a new application use case (e.g., `CreateNotification`, `CancelBatch`) following the project's hexagonal architecture and TDD conventions.

## When To Use

- You are creating any new operation that lives in `internal/application/`.
- The operation orchestrates domain logic and ports (does not own raw I/O).
- The PR or task description says "add ... use case" or equivalent.

## Prerequisites

- The domain entities and value objects the use case depends on already exist in `internal/domain/`.
- The ports the use case will use are defined in `internal/ports/`. If a needed port does not exist, create it first (separate commit).
- You are on a feature branch as defined in `PLAN.md`.

## Steps

### 1. Write the failing test first

Create `internal/application/<use_case_name>_test.go`. Use table-driven tests. Cover:

- Happy path.
- Each domain error path (invalid input, not found, conflict, etc.).
- Concurrency safety if relevant.
- Context cancellation if the use case does any I/O.

Use hand-written fakes from `internal/testing/fakes/`, not generated mocks. If a needed fake does not exist, write it (separate commit, separate skill if needed).

```go
package application_test

import (
    "context"
    "testing"

    "github.com/stretchr/testify/require"
    "github.com/<repo>/api/internal/application"
    "github.com/<repo>/api/internal/domain"
    "github.com/<repo>/api/internal/testing/fakes"
)

func TestCreateNotification(t *testing.T) {
    t.Parallel()

    tests := []struct {
        name      string
        input     application.CreateNotificationInput
        setup     func(*fakes.NotificationRepository, *fakes.Queue)
        wantErr   error
        wantCheck func(*testing.T, application.CreateNotificationOutput)
    }{
        // ... cases here
    }

    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            t.Parallel()
            repo := fakes.NewNotificationRepository()
            queue := fakes.NewQueue()
            if tc.setup != nil {
                tc.setup(repo, queue)
            }
            uc := application.NewCreateNotification(repo, queue, fakes.NewClock())
            out, err := uc.Execute(context.Background(), tc.input)
            if tc.wantErr != nil {
                require.ErrorIs(t, err, tc.wantErr)
                return
            }
            require.NoError(t, err)
            tc.wantCheck(t, out)
        })
    }
}
```

Commit: `test(application): add failing test for <use case name> use case`

### 2. Define the use case interface and types

In `internal/application/<use_case_name>.go`:

```go
package application

import (
    "context"

    "github.com/<repo>/api/internal/domain"
    "github.com/<repo>/api/internal/ports"
)

// CreateNotificationInput is the request payload for the use case.
// All validation happens inside Execute; callers do not pre-validate.
type CreateNotificationInput struct {
    Recipient string
    Channel   domain.Channel
    Content   string
    Priority  domain.Priority
}

// CreateNotificationOutput is the response from a successful create.
type CreateNotificationOutput struct {
    NotificationID domain.NotificationID
}

// CreateNotification creates a notification, persists it as pending, and
// enqueues it for asynchronous delivery. It is idempotent within the
// configured window when the same Idempotency-Key is passed via context.
type CreateNotification struct {
    repo  ports.NotificationRepository
    queue ports.Queue
    clock ports.Clock
}

// NewCreateNotification constructs the use case with its dependencies.
func NewCreateNotification(repo ports.NotificationRepository, queue ports.Queue, clock ports.Clock) *CreateNotification {
    return &CreateNotification{repo: repo, queue: queue, clock: clock}
}

// Execute runs the use case.
func (uc *CreateNotification) Execute(ctx context.Context, in CreateNotificationInput) (CreateNotificationOutput, error) {
    // ... implementation
}
```

Commit: `feat(application): implement <use case name> use case`

### 3. If the use case needs a port that did not exist

Stop. Add the port in a separate, prior commit:

```
feat(ports): add <port name> interface
```

Then return to step 1.

### 4. Update the test until all cases pass

Run `make test`. Iterate until green.

### 5. Verify the use case is wired into `cmd/api` or `cmd/worker`

If this use case is invoked from an HTTP handler or a queue task processor, the dependency wiring in the corresponding `main.go` must instantiate it. If you have not built the wiring yet (because the HTTP/queue layer is a later phase), note this in your task summary so the human knows.

## Verification

- [ ] `make test` passes.
- [ ] `make lint` passes.
- [ ] The use case does not import any package outside `internal/domain/`, `internal/application/`, `internal/ports/`, or stdlib. Verify with the `check-hexagonal-boundaries` skill.
- [ ] Coverage for `internal/application/` is ≥ 90%.
- [ ] Every public symbol has a Go doc comment.

## Common Mistakes

- Returning `interface{}` or `any` from the use case. Always use concrete types or generics.
- Calling `time.Now()` directly inside the use case. Use `uc.clock.Now()`.
- Storing context in the struct. Context flows through `Execute`.
- Wrapping a domain error in a non-domain error and losing `errors.Is` reachability.
- Forgetting to handle context cancellation in long-running operations.
- Logging inside the use case. Logging happens at the boundary (handler or task processor), not in the use case.
