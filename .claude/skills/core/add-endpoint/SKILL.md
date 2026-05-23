# Skill: add-endpoint

## Purpose

Add a new HTTP endpoint to the API, spec-first, with handler, tests, and documentation.

## When To Use

- You are adding a new path or method to the public HTTP API.
- The endpoint will be reachable from API consumers or the dashboard.

## Prerequisites

- The use case the endpoint will invoke already exists (see `add-use-case`).
- You understand which existing middleware applies (auth, rate limit, idempotency).

## Steps

### 1. Update the OpenAPI spec first

Open `api/api/openapi.yaml`. Add the new path under `paths:`. Include:

- HTTP method and summary.
- Path parameters with types and descriptions.
- Query parameters with types, descriptions, and defaults.
- Request body schema with `$ref` to a `components/schemas/` definition.
- All response codes (200/201/202/400/404/409/500) with body schemas. Use `Problem` schema for 4xx and 5xx.
- `Idempotency-Key` parameter on POST endpoints.

Validate the spec:

```sh
make openapi-lint
```

Commit: `docs(openapi): add <method> <path> specification`

### 2. Regenerate the server interface

```sh
make openapi-generate
```

This refreshes `api/internal/adapters/http/generated/`. The generated interface gains a new method for the new endpoint. Do not edit generated files.

Commit: `chore(adapter/http): regenerate server interface for new endpoint`

### 3. Write the failing handler test

In `api/internal/adapters/http/handlers/<resource>_test.go`:

- Build a fake use case and inject it into the handler.
- Use `httptest.NewRequest` and `httptest.NewRecorder`.
- Test happy path: 2xx with expected body.
- Test each error path: 400 (validation), 404 (not found), 409 (idempotency conflict), 500 (downstream error).
- Test request validation: missing required fields, wrong types, value out of range.
- Test that `Idempotency-Key` is read and forwarded if applicable.

Commit: `test(adapter/http): add failing test for <method> <path>`

### 4. Implement the handler

In `api/internal/adapters/http/handlers/<resource>.go`:

```go
func (h *NotificationHandler) Create(w http.ResponseWriter, r *http.Request) {
    var req CreateNotificationRequest
    if err := decode(r, &req); err != nil {
        problem.Write(w, r, problem.BadRequest("invalid body", err))
        return
    }
    if err := h.validator.Struct(req); err != nil {
        problem.Write(w, r, problem.ValidationFailed(err))
        return
    }

    out, err := h.uc.Execute(r.Context(), req.toInput())
    if err != nil {
        problem.WriteDomainError(w, r, err)
        return
    }

    w.Header().Set("Location", "/api/v1/notifications/"+string(out.NotificationID))
    write(w, http.StatusAccepted, CreateNotificationResponse{ID: out.NotificationID})
}
```

Commit: `feat(adapter/http): implement <method> <path> handler`

### 5. Register the route

In `api/internal/adapters/http/router.go`, add the route inside the appropriate `chi.Router.Route` block. Make sure middleware ordering is correct (correlation ID first, then auth, then idempotency, then handler).

### 6. Update `cmd/api/main.go` if wiring is needed

If the handler needs new dependencies, instantiate them in `main()` and pass them to the handler constructor.

### 7. Add a curl example to API_EXAMPLES.md

Write a working `curl` command for the endpoint, including headers and a realistic body. Show the expected response. Cover both success and one error case.

Commit: `docs: add API examples for <method> <path>`

## Verification

- [ ] `make openapi-lint` passes.
- [ ] `make test` passes.
- [ ] `make lint` passes.
- [ ] Manual smoke test against `docker compose up`:
  ```sh
  curl -i -X POST http://localhost:8080/api/v1/<path> -H 'Content-Type: application/json' -d '...'
  ```
- [ ] Swagger UI at `http://localhost:8080/docs` shows the new endpoint.

## Common Mistakes

- Writing the handler before updating the OpenAPI spec. The spec is the source of truth.
- Validating manually instead of using the `validator` struct tags.
- Returning raw error messages to the client. Always translate through `problem.WriteDomainError`.
- Forgetting the `Location` header on POST endpoints.
- Skipping the `Idempotency-Key` parameter on mutation endpoints.
- Adding business logic to the handler. The handler decodes, validates, calls a use case, encodes. That is all.
