# API examples

> Every public endpoint, every common shape, ready to paste into a
> terminal. Assumes `docker compose up -d && make migrate-up` has
> already run. `BASE` defaults to `http://localhost:8080`.

```bash
export BASE=http://localhost:8080
```

For interactive exploration use **Swagger UI** at `$BASE/docs`, or the
prebuilt **Bruno** collection under [`docs/bruno/`](./bruno/).

## Conventions

- `Idempotency-Key` is honored on every write endpoint (POST, PATCH).
  Repeats within 24h return the cached response with status `200`
  instead of `201` / `202`.
- `X-Correlation-ID` is read if supplied; otherwise the server
  generates a ULID. Echoed in the response header AND embedded in
  every log line and queue payload (CLAUDE.md §2.3).
- Errors are RFC 7807 `application/problem+json` (CLAUDE.md §3.5).
- List endpoints use cursor pagination: pass the response's
  `next_cursor` as `?cursor=...` to fetch the next page.

---

## Notifications

### Create

```bash
curl -i -X POST "$BASE/api/v1/notifications" \
  -H 'content-type: application/json' \
  -H 'idempotency-key: 5e9bcb15-7c61-4e8a-9fcd-1a90d2c1d111' \
  -H 'x-correlation-id: 01HXYZSAMPLECORR0001' \
  -d '{
    "channel":   "sms",
    "recipient": "+15555550001",
    "content":   "Your verification code is 4242",
    "priority":  "high"
  }'
```

Response: `202 Accepted`

```http
Location: /api/v1/notifications/01940000-0000-7000-8000-000000000abc
X-Correlation-ID: 01HXYZSAMPLECORR0001

{
  "id": "01940000-0000-7000-8000-000000000abc",
  "status": "queued",
  "channel": "sms",
  "recipient": "+15555550001",
  "content": "Your verification code is 4242",
  "priority": "high",
  "correlation_id": "01HXYZSAMPLECORR0001",
  "attempts": 0,
  "max_attempts": 5,
  "created_at": "...",
  "updated_at": "..."
}
```

**Duplicate request** (same `Idempotency-Key`):

```bash
# Repeat exact same body + key within 24h
# → 200 OK (NOT 202) · same body, same id
```

**Validation error** (unknown channel):

```bash
curl -X POST "$BASE/api/v1/notifications" \
  -H 'content-type: application/json' \
  -d '{ "channel":"fax", "recipient":"+1", "content":"x" }'
```

Response: `400 Bad Request` · `application/problem+json`

```json
{
  "type":   "/probs/invalid-channel",
  "title":  "Invalid Channel",
  "status": 400,
  "detail": "Channel must be one of: sms, email, push.",
  "correlation_id": "..."
}
```

### Get

```bash
curl "$BASE/api/v1/notifications/01940000-0000-7000-8000-000000000abc"
```

`404 Not Found` with `/probs/not-found` problem body when the id is unknown.

### List (filters + cursor pagination)

```bash
# Filters: status, channel, batch_id, created_after, created_before
curl "$BASE/api/v1/notifications?status=delivered&channel=sms&limit=25"

# Next page
curl "$BASE/api/v1/notifications?status=delivered&cursor=<next_cursor>&limit=25"
```

Response shape:

```json
{
  "items": [ { "id": "...", "status": "delivered", "...": "..." } ],
  "next_cursor": "opaque-token"
}
```

`next_cursor` is omitted on the last page — clients use absence as
"end of results".

### Cancel

```bash
curl -i -X PATCH "$BASE/api/v1/notifications/01940000-.../cancel"
```

- `200 OK` — cancelled (was pending / queued / retrying)
- `409 Conflict` (`/probs/invalid-transition`) — already terminal (delivered / failed / cancelled)
- `404 Not Found` — unknown id

### Trace (audit log)

```bash
curl "$BASE/api/v1/notifications/01940000-.../trace"
```

Response:

```json
{
  "notification_id": "01940000-...",
  "entries": [
    { "event": "created",    "correlation_id": "...", "created_at": "..." },
    { "event": "queued",     "correlation_id": "...", "created_at": "..." },
    { "event": "processing", "correlation_id": "...", "created_at": "..." },
    { "event": "delivered",  "correlation_id": "...", "created_at": "...",
      "details": { "provider": "twilio-mock", "status_code": 200 } }
  ]
}
```

Chronological order; every entry carries the same `correlation_id`.

---

## Batch

### Create batch

```bash
curl -i -X POST "$BASE/api/v1/notifications/batch" \
  -H 'content-type: application/json' \
  -H 'idempotency-key: 5e9bcb15-7c61-4e8a-9fcd-1a90d2c1d222' \
  -H 'x-correlation-id: 01HXYZBATCHCORR0001' \
  -d '{
    "notifications": [
      { "channel":"sms",   "recipient":"+15555550001", "content":"Bulk 1" },
      { "channel":"sms",   "recipient":"+15555550002", "content":"Bulk 2" },
      { "channel":"email", "recipient":"alice@example.com","content":"Bulk 3" }
    ]
  }'
```

Response: `202 Accepted`

```json
{
  "id": "01940000-0000-7000-8000-00000000bbbb",
  "size": 3,
  "correlation_id": "01HXYZBATCHCORR0001",
  "created_at": "..."
}
```

Member notifications are **omitted** from the POST 202 response — the
client fetches them via the GET endpoint below.

### Get batch (with members)

```bash
curl "$BASE/api/v1/notifications/batch/01940000-...-bbbb"
```

Response includes `notifications` array with every member.

---

## Templates

### Create

```bash
curl -i -X POST "$BASE/api/v1/templates" \
  -H 'content-type: application/json' \
  -d '{
    "name":    "welcome-sms",
    "channel": "sms",
    "body":    "Hello {{.Name}}, your code is {{.Code}}."
  }'
```

Response: `201 Created` with `Location` header.

### Get / list

```bash
curl "$BASE/api/v1/templates/01940000-..."

curl "$BASE/api/v1/templates?limit=50"
```

### Replace (PUT semantics)

```bash
curl -i -X PUT "$BASE/api/v1/templates/01940000-..." \
  -H 'content-type: application/json' \
  -d '{ "name":"welcome-sms", "channel":"sms", "body":"Hi {{.Name}} — updated body." }'
```

`CreatedAt` is preserved server-side; `UpdatedAt` is bumped.

### Delete

```bash
curl -i -X DELETE "$BASE/api/v1/templates/01940000-..."
```

- `204 No Content` — deleted
- `409 Conflict` — a notification still references this template (FK guard)
- `404 Not Found` — unknown id

### Render at notification time

```bash
curl -i -X POST "$BASE/api/v1/notifications" \
  -H 'content-type: application/json' \
  -d '{
    "channel":            "sms",
    "recipient":          "+15555550001",
    "content":            "placeholder (overridden)",
    "template_id":        "01940000-...-tmpl",
    "template_variables": { "Name":"Ada", "Code":4242 }
  }'
```

The persisted `content` field is the rendered template body —
`"Hello Ada, your code is 4242."`. The placeholder is ignored.

---

## WebSocket — real-time status

```bash
# Open the connection
wscat -c "ws://localhost:8080/api/v1/ws/notifications"

# Subscribe
> {"action":"subscribe","notification_id":"01940000-..."}

# Status updates arrive as they happen
< {"notification_id":"01940000-...","status":"processing"}
< {"notification_id":"01940000-...","status":"delivered"}

# Unsubscribe
> {"action":"unsubscribe","notification_id":"01940000-..."}
```

Closing the WebSocket drops every subscription the client held —
no manual cleanup required.

---

## Meta endpoints

### Health

```bash
curl "$BASE/healthz/live"   # process alive · 200 OK · {"status":"ok"}
curl "$BASE/healthz/ready"  # 200 if pg + redis reachable; 503 otherwise
```

When `/healthz/ready` returns 503 the body is RFC 7807:

```json
{
  "type":   "/probs/dependency-unavailable",
  "title":  "Dependency Unavailable",
  "status": 503,
  "detail": "One or more downstream dependencies are not responding."
}
```

### Metrics

```bash
# Prometheus exposition format — what the scraper consumes
curl "$BASE/metrics"

# JSON-friendly subset for dashboards / scripts
curl "$BASE/api/v1/metrics"
```

JSON shape:

```json
{
  "created_per_minute":   120,
  "delivered_per_minute": 118,
  "failed_per_minute":    2,
  "queue_depth":          42,
  "success_rate":         0.983
}
```

`success_rate` is omitted when the window has no traffic — its
absence means "no data", not zero.

---

## Pagination patterns

Every list endpoint follows the same cursor pattern:

```bash
# First page
RESP=$(curl -s "$BASE/api/v1/notifications?limit=10")
echo "$RESP" | jq '.items[].id'

# Next page — capture next_cursor from the previous response
NEXT=$(echo "$RESP" | jq -r '.next_cursor // empty')
[ -n "$NEXT" ] && curl -s "$BASE/api/v1/notifications?cursor=$NEXT&limit=10"
```

Cursors are opaque base64 — clients should never inspect them.

---

## Error catalog (selected)

| Type slug                          | Status | When                                                    |
|------------------------------------|--------|---------------------------------------------------------|
| `/probs/invalid-channel`           | 400    | channel ∉ {sms, email, push}                            |
| `/probs/invalid-recipient`         | 400    | malformed e164 phone / RFC 5322 email                   |
| `/probs/invalid-batch-size`        | 400    | batch has 0 or > 1000 notifications                     |
| `/probs/not-found`                 | 404    | id does not exist                                       |
| `/probs/invalid-transition`        | 409    | cancel on terminal status                               |
| `/probs/concurrent-update`         | 409    | another request mutated the row first                   |
| `/probs/dependency-unavailable`    | 503    | pg or redis unreachable (readiness probe)               |
| `/probs/internal`                  | 500    | unmapped error — operator should check logs by correlation_id |

The full catalog is the `errorMappings` table in
[`internal/adapters/http/problem.go`](../internal/adapters/http/problem.go).
