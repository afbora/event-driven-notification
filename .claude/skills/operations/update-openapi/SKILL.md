# Skill: update-openapi

## Purpose

Modify the OpenAPI specification when the HTTP contract changes, and keep the generated Go and TypeScript code in sync.

## When To Use

- A request or response body shape is changing.
- A new query parameter or header is being added or removed.
- A new error response code is being returned.
- A field's validation constraint is changing.

## Prerequisites

- The change is intentional and reviewed (a contract change can break consumers).
- You have run `git status` and the working tree is clean before starting.

## Steps

### 1. Open `api/api/openapi.yaml`

The spec follows OpenAPI 3.0 (we deliberately avoid 3.1 for tooling compatibility).

Structure:

- `paths:` — endpoints
- `components/schemas/` — reusable type definitions
- `components/parameters/` — reusable parameter definitions
- `components/responses/` — reusable response definitions (especially `Problem` for RFC 7807)

### 2. Make the change in the spec

Examples:

**Add a field to a schema:**

```yaml
components:
  schemas:
    Notification:
      type: object
      required: [id, recipient, channel, content, status]
      properties:
        id:
          type: string
          format: uuid
        # ... existing fields
        scheduled_at:           # ← new
          type: string
          format: date-time
          nullable: true
          description: When this notification should be processed. Null = immediately.
```

**Add a new query parameter:**

```yaml
paths:
  /v1/notifications:
    get:
      parameters:
        # ... existing
        - name: scheduled_only       # ← new
          in: query
          schema:
            type: boolean
            default: false
          description: When true, return only notifications with scheduled_at in the future.
```

**Add a new error response:**

```yaml
paths:
  /v1/notifications:
    post:
      responses:
        '202': { ... }
        '400': { $ref: '#/components/responses/BadRequest' }
        '422':                                          # ← new
          description: Recipient unreachable for the requested channel.
          content:
            application/problem+json:
              schema:
                $ref: '#/components/schemas/Problem'
```

### 3. Lint the spec

```sh
make openapi-lint
```

This catches:

- Missing `description` fields.
- `$ref` to non-existent definitions.
- Type mismatches.
- Spec version incompatibility.

### 4. Regenerate the Go server interface

```sh
make openapi-generate
```

This updates `api/internal/adapters/http/generated/`. The generated interface now has the updated method signatures.

Compile errors will surface in handler code that has not yet implemented the new contract. Fix them.

### 5. Update handlers

Implement the new behaviour:

- Handler reads the new field, calls the use case with it.
- Use case (if signature changes) propagates the new field through the application layer.

### 6. Update tests

Handler tests need cases covering the new field/parameter.

### 7. Update API_EXAMPLES.md

If the change affects how a consumer calls the API, update the curl example.

### 8. Commit as a logical sequence

```
docs(openapi): add scheduled_at field to Notification schema
chore(adapter/http): regenerate server interface for scheduled_at
feat(adapter/http): pass scheduled_at to ScheduleNotification use case
test(adapter/http): cover scheduled_at handler behaviour
docs: update API examples for scheduled_at
```

## Verification

- [ ] `make openapi-lint` passes.
- [ ] Generated files (`generated/`) are committed.
- [ ] `make test` passes.
- [ ] `make lint` passes.
- [ ] Swagger UI shows the updated spec.
- [ ] API_EXAMPLES.md is updated.

## Common Mistakes

- Editing generated files. They will be overwritten next generate run.
- Forgetting to regenerate after the spec change, leading to a drift between spec and code.
- Breaking changes without a version bump or a deprecation note.
- Adding optional fields without a default, breaking older consumers.
- Removing fields that consumers still depend on.
- Adding required parameters to existing endpoints (breaks all existing requests).
- Tweaking the spec without running `make openapi-lint` first.
