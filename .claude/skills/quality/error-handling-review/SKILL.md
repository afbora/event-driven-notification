# Skill: error-handling-review

## Purpose

Audit the error handling in a piece of code so every error path is correct, wrapped consistently, surfaces useful information, and ends up at a sensible boundary.

## When To Use

- You are reviewing your own code before commit.
- You are reviewing someone else's code.
- A bug report mentions confusing error messages or lost stack context.

## Steps

### 1. Identify every error source in the code

Every `return err`, every `if err != nil`, every `panic`. Make a list.

### 2. For each error source, answer four questions

**Q1: Is the error wrapped with `fmt.Errorf("...: %w", err)`?**

If not, the calling code cannot tell what happened. Wrap it with context that names the operation, not just the file.

Bad:
```go
return err
```

Good:
```go
return fmt.Errorf("postgres: scan notification %s: %w", id, err)
```

**Q2: Is `errors.Is` or `errors.As` reachable for the sentinel error you care about?**

If the caller is supposed to detect `ErrNotFound`, the wrap chain must preserve it. Use `%w`, not `%v` or `%s`.

**Q3: Does the error eventually reach exactly one logger call?**

Logs happen at the boundary: HTTP handler, queue task processor, `main()`. Internal layers return errors; they do not log them. Double-logging is noise; no logging is a black hole.

Check: trace each error from origin to log. There should be exactly one log statement at the end of the chain.

**Q4: Is the error mapped to the correct user-facing response?**

For HTTP: domain `ErrNotFound` → 404 Problem. `ErrInvalidInput` → 400. `ErrConflict` → 409. Unhandled → 500.
For queue: permanent failure → mark notification failed, no retry. Transient → retry with backoff.
For CLI: print structured error and exit non-zero.

### 3. Check for `_ = something` patterns

`_ = json.Unmarshal(data, &x)` swallows errors silently. Each `_` for an error return needs a justification comment or a fix.

### 4. Check for `panic` outside startup

`panic` is acceptable in `main()` during dependency wiring. Anywhere else, replace with `return err` or convert to a logged-and-recovered scenario.

### 5. Check error messages are useful

A good error message includes:

- **What operation failed:** "scan notification row", not "scan failed".
- **Which input was involved:** the ID, the key, the channel name.
- **The underlying error:** via `%w`.

Bad: `errors.New("failed")`
Bad: `fmt.Errorf("error: %v", err)` (drops wrap)
Good: `fmt.Errorf("repository.NotificationRepository: scan notification %s: %w", id, err)`

### 6. Check sentinel errors live in the right package

Domain errors live in `internal/domain/errors.go`. Adapter errors that are part of the public API of the adapter live in that adapter's package. Internal-only errors can be unexported.

If you find a sentinel like `var ErrFailed = errors.New("failed")` in a handler, it almost certainly belongs in the domain.

### 7. Check no error string is used as control flow

```go
if strings.Contains(err.Error(), "not found") { ... } // ❌
```

Use `errors.Is(err, ErrNotFound)` instead. String comparison breaks on log format changes, locale changes, and library upgrades.

### 8. Verify the test coverage of error paths

Every error branch should have a test. Run:

```sh
make coverage
```

Look at the HTML report for the file. Red lines on the `return err` side mean the error path is untested.

## Verification

- [ ] All errors are wrapped with `%w` and contextual messages.
- [ ] `errors.Is` and `errors.As` work for every sentinel.
- [ ] Exactly one log statement per error chain, at the boundary.
- [ ] HTTP errors map to correct status codes via the problem translator.
- [ ] No `panic` outside startup.
- [ ] No silent `_ = err` without justification.
- [ ] No `err.Error()` string matching.
- [ ] Every error branch has a test.

## Common Mistakes

- Wrapping the same error twice ("postgres: query: postgres: scan: ..."). Wrap at meaningful boundaries, not every function.
- Adding too much context — entire SQL statements, full request bodies. Logs leak. Keep messages descriptive but small.
- Using `%v` instead of `%w` for errors, breaking the chain.
- Returning a generic `errors.New` in a place where a typed error would let callers handle the case specifically.
- Logging and returning ("just in case"). One log per chain.
- Treating all errors the same in HTTP — every domain error class gets its own status code.
