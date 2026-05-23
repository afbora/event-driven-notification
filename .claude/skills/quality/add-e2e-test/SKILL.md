# Skill: add-e2e-test

## Purpose

Add an end-to-end test that exercises the full system stack: HTTP request → use case → repository → queue → worker → provider → DB update.

## When To Use

- You are testing a complete user-visible scenario (notification lifecycle, batch processing, idempotency end-to-end).
- You need confidence that the parts wired together produce the right behaviour.

## Prerequisites

- The full stack is wired in `cmd/api/main.go` and `cmd/worker/main.go`.
- E2E test infrastructure exists in `tests/e2e/`.
- Build tag `//go:build e2e` is at the top of the file.

## Steps

### 1. Create the test file

```go
//go:build e2e

package e2e_test

import (
    "context"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
)
```

### 2. Use the e2e harness

The harness in `tests/e2e/harness/` starts Postgres, Redis, the API server, and the worker in-process with `httptest`. It returns:

- `harness.BaseURL` — the API server URL.
- `harness.Client` — preconfigured HTTP client.
- `harness.MockProvider` — replaces real providers; configurable per-test failure rate.
- `harness.Cleanup()` — tears everything down.

```go
func TestNotificationLifecycle(t *testing.T) {
    h := harness.Start(t) // takes care of cleanup via t.Cleanup

    // Send a notification
    resp := h.Client.PostJSON("/api/v1/notifications", CreateNotificationRequest{
        Recipient: "+905551234567",
        Channel:   "sms",
        Content:   "test message",
        Priority:  "normal",
    })
    require.Equal(t, 202, resp.StatusCode)

    id := decodeID(resp)

    // Wait for worker to process
    require.Eventually(t, func() bool {
        n := h.Client.GetNotification(id)
        return n.Status == "sent"
    }, 5*time.Second, 50*time.Millisecond)

    // Verify side effects
    require.Equal(t, 1, h.MockProvider.SendCount(domain.ChannelSMS))
}
```

### 3. Test the scenario, not the parts

E2E tests are slow. Use them for high-value scenarios:

- Full happy-path lifecycle.
- Failure-then-retry-then-success.
- Failure-then-DLQ.
- Idempotency under duplicate POSTs.
- Cancellation while in queue.
- Scheduled delivery timing.
- Rate limit enforcement under load.

Do not duplicate unit/integration test coverage. If something is testable at a lower level, test it there.

### 4. Make scenarios self-contained

Each test sets up its own data and asserts its own outcome. No reliance on test order. Use `harness.Start(t)` per test (cleanup is automatic).

### 5. Test error paths

E2E is also where you verify the **error contract** end-to-end:

```go
// Provider fails 3 times then succeeds
h.MockProvider.SetFailures(domain.ChannelSMS, 3)

resp := h.Client.PostJSON("/api/v1/notifications", validRequest)
id := decodeID(resp)

require.Eventually(t, func() bool {
    n := h.Client.GetNotification(id)
    return n.Status == "sent" && n.Attempts == 4
}, 30*time.Second, 100*time.Millisecond) // allow for backoff
```

### 6. Run with the e2e tag

```sh
make test-e2e
```

## Verification

- [ ] `make test-e2e` passes.
- [ ] No `time.Sleep` calls; always `require.Eventually`.
- [ ] Test runs in under 30 seconds (e2e has higher tolerance than integration).
- [ ] No flakiness across 5 consecutive runs.

## Common Mistakes

- Replicating unit test coverage in e2e. E2E is for scenarios that span the system.
- Hardcoding timing assumptions. Backoff is exponential; `Eventually` is your friend.
- Asserting on internal state (DB rows) when an API call would do the same. Stay at the public boundary.
- Not setting `MockProvider` failure rates explicitly. Default behaviour may differ from what the test assumes.
- Skipping cleanup. `harness.Start(t)` registers cleanup automatically; don't bypass it.
