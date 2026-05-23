# Skill: debug-failing-test

## Purpose

Diagnose a failing test systematically, find the root cause, and fix it without introducing new problems.

## When To Use

- A test is failing locally or in CI.
- Especially when the failure is intermittent ("flaky") or the error message is cryptic.

## Steps

### 1. Reproduce the failure deterministically

If the test fails intermittently, run it in a loop:

```sh
go test -run TestName -count=20 -race ./internal/...
```

If 20 runs pass, increase to 100. A truly flaky test will fail eventually.

If it fails every time, proceed.

### 2. Read the actual error message

Strip the noise. Find the first line that contains the actual assertion failure. Common Go test outputs:

- `Error Trace:` — points to the line.
- `Error: Not equal:` — shows expected vs actual.
- `Error: Received unexpected error:` — shows the unwrapped error chain.
- `panic: ... goroutine N` — race or nil deref; check the stack.

Do not assume; read.

### 3. Identify the failure category

| Symptom | Likely cause |
|---|---|
| Different result expected vs actual | Logic bug in production code |
| `nil pointer dereference` | Missing initialization or unhandled error |
| `context deadline exceeded` | Code blocking; missing cancellation |
| `dial tcp: connection refused` | Test container not ready, or wrong endpoint |
| `unique constraint violation` in repeat runs | State leak between tests |
| Flaky pass/fail | Goroutine race, timing assumption, shared state |
| Compile error in tests | Refactor missed call sites |

### 4. Reach for the right tool

- **Logic bug:** Add `t.Logf` or `slog.Debug` to print intermediate values. Run with `-v`.
- **Race:** `go test -race`. Fix with sync primitives, not by removing the race detector.
- **Timing:** Replace `time.Sleep` with `require.Eventually`. Increase polling, not duration.
- **State leak:** Audit `t.Cleanup`. Check that every test isolates its own data.
- **Hidden error:** Look for `_ = err` or ignored returns.

### 5. Bisect when the cause is unclear

If you do not know which commit broke the test:

```sh
git bisect start
git bisect bad HEAD
git bisect good <last-known-green-commit>
# repeat: git bisect good / git bisect bad until found
```

### 6. Fix the cause, not the symptom

- **Do not** add `time.Sleep` to silence a race.
- **Do not** add `t.Skip` to mute a test.
- **Do not** loosen an assertion to make a wrong result acceptable.
- **Do** find the actual condition, fix it, and ensure the test now consistently passes the right thing.

### 7. Add a regression test if the root cause was not covered

If the bug existed in production code that no test caught, the existing test suite has a gap. Add a small, targeted test for the specific failure mode before closing the work.

### 8. Document the fix in the commit

Commit message should describe the root cause, not just the change:

```
fix(adapter/postgres): close pool connection in repository tests

The integration tests leaked a connection per run because the pool
was created inside the test but never closed via t.Cleanup. Under
parallel execution this exhausted the testcontainers Postgres pool
on the 41st test.
```

## Verification

- [ ] The test passes consistently across 20+ runs.
- [ ] No `time.Sleep` was added.
- [ ] No tests were skipped or weakened.
- [ ] If the cause was missed coverage, a regression test was added.

## Common Mistakes

- Silencing flakes with sleeps or retries. The flake is information; act on it.
- Fixing only the immediate symptom without understanding why it occurred.
- Refactoring "while I'm in here." The fix and the refactor are separate commits.
- Ignoring the race detector because it complicates things. The race is real; fix it.
- Not adding a regression test, so the same class of bug returns later.
