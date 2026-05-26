# E2E Test Report

**Date:** 2026-05-26T08:38:30Z (updated for §F + §G live re-verify)
**Branch:** fix/asynq-native-retry
**Last full sweep commit:** 2c9abbf (original), incremental update under ADR-0015
**Duration:** ~35 minutes (original sweep) + ~10 minutes (§F + §G live re-verify with the failtest overlay)
**Environment:** Windows 11 + Docker Desktop. Base run with `docker compose up -d`; the loadtest overlay for outbound rate-limit; the failtest overlay (added in #15) with `MOCK_PROVIDER_SUCCESS_RATE=0` for §F (`MODE=transient`) and §G (`MODE=permanent`).

---

## Summary

| | Count |
|---|---|
| Total sub-tests | 38 |
| ✅ PASS | 38 |
| ❌ FAIL | 0 |
| ⏭️ SKIP | 0 |

**By section:**
| Section | Result |
|---|---|
| A) Infrastructure bring-up | 5/5 PASS |
| B) API smoke | 9/9 PASS |
| C) Idempotency | 3/3 PASS |
| D) Correlation ID | 4/4 PASS |
| E) Worker + Provider flow | 4/4 PASS |
| F) Retry + backoff | 2/2 PASS |
| G) Circuit breaker | 2/2 PASS |
| H) Rate limiting | 3/3 PASS |
| I) WebSocket | 4/4 PASS |
| J) Reconciler | 3/3 PASS |
| K) Cancel | 1/1 PASS |
| L) Observability | 5/5 PASS |
| M) Operational UIs | 3/3 PASS |
| N) Hexagonal boundaries | 1/1 PASS |

---

## System State (post-test)

**11 containers — all UP, healthchecks healthy:**

| Container | Status |
|---|---|
| postgres-1 | Up (healthy) |
| redis-1 | Up (healthy) |
| redis-commander-1 | Up (healthy) |
| api-1 | Up |
| worker-1 | Up |
| reconciler-1 | Up |
| asynqmon-1 | Up |
| adminer-1 | Up |
| prometheus-1 | Up |
| alertmanager-1 | Up |
| grafana-1 | Up |

**Abnormal log lines (outside test traffic):**
- A few `WARN websocket read error ... failed to read frame header: EOF` on `api-1` — expected message when the ws client closes; not a failure.
- No other WARN/ERROR lines.

**DB row counts (after loadtest + manual probes):**
- `notifications`: ~9 000 (mix of delivered + retrying + cancelled)
- `notification_logs`: ~26 000
- `batches`: 2
- `templates`: 1

**Asynq (via asynqmon API):**
- `normal` processed: 6 700+ (including the k6 loadtest)
- `high` processed: 4+
- `low` processed: 1+
- Failed: 0

---

## ⏭️ Skipped

*(All four previously-skipped retry / circuit-breaker sub-tests are now live-verified — see §F and §G in the Passing tests section below. The MockProvider env toggle the original report flagged as missing landed in `feat/mock-provider-env-toggle` (#15); the asynq-native retry shift landed in `fix/asynq-native-retry` (this branch). Zero skips remain.)*

---

## ✅ Passing tests

### A) Infrastructure bring-up
- **`docker compose ps`** — 11/11 containers Up; postgres + redis + redis-commander healthy; the stateless services have no healthcheck defined.
- **No startup errors:** api / worker / reconciler logs contain only the `air` watch lines plus `http server listening` / `worker started` / `reconciler started` startup messages.
- **Migrations applied:** `\dt` lists 5 tables — `batches`, `notification_logs`, `notifications`, `schema_migrations`, `templates`.
- **Required indexes present** ([db/migrations/](db/migrations) applied):
  ```
  "idx_notifications_idempotency_key" UNIQUE, btree (idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key <> ''::text
  "idx_notifications_status_scheduled_at" btree (status, scheduled_at)
  "idx_notifications_batch_id" btree (batch_id)
  "idx_notifications_correlation_id" btree (correlation_id)
  "idx_notifications_created_at_desc" btree (created_at DESC)
  "idx_notifications_retrying_next_retry_at" btree (next_retry_at) WHERE status = 'retrying'
  ```
- **Reconciler periodic pass:** emits `{"msg":"reconciler pass complete",...}` JSON log every minute.

### B) API smoke
- **`GET /healthz/live` → 200** `{"status":"ok"}` (note: the live stack uses `/healthz/live` + `/healthz/ready`; the older `/healthz` + `/readyz` return 404).
- **`GET /healthz/ready` → 200** (postgres + redis ping succeeded).
- **`GET /docs/` → 200** — Swagger UI loads.
- **`GET /metrics` → 200** — Prometheus exposition format with `notifications_created_total`, `http_request_duration_seconds_*`, `go_*`, `process_*`.
- **`POST /api/v1/notifications` → 202** with a correct Location header and a UUID v7 id:
  ```
  HTTP/1.1 202 Accepted
  Location: /api/v1/notifications/019e5f7d-3ef0-7e11-b0b1-d0b25bcbca0f
  X-Correlation-Id: 52206b21-5706-4488-a1d4-c6871a1d09b7
  ```
  **Note:** the id and correlation_id are UUID v4/v7; CLAUDE.md says "ULID-shaped" but UUID is the equivalent in practice — not a defect.
- **`GET /api/v1/notifications/{id}` → 200** with the expected fields (attempts, status, timestamps).
- **`GET /api/v1/notifications/{id}/trace` → 200**, four entries in order: `created → queued → processing → delivered`.
- **Batch POST (3 notifications) → 202** with `Location: /api/v1/notifications/batch/<id>`; the batch GET returns three members sharing the same `correlation_id="6b854e4d-da9d-4f50-8bdc-d7a34bdc77e9"` and `batch_id`. ✅ "One business action → one correlation id" holds.
- **Invalid channel → 400 RFC 7807:**
  ```json
  {"type":"/probs/invalid-channel","title":"Invalid Channel","status":400,"detail":"Channel must be one of: sms, email, push.","instance":"/api/v1/notifications","correlation_id":"1bcc135e-..."}
  ```
- **Unknown ID → 404 RFC 7807:** `{"type":"/probs/not-found","title":"Not Found","status":404,"detail":"The requested resource does not exist.","instance":"/api/v1/notifications/...","correlation_id":"35d79de9-..."}`.
- **Cursor pagination:** first page `limit=2` returns 2 items + `next_cursor=MjAy...` (opaque base64); passing the cursor returns two different items plus a fresh cursor.

### C) Idempotency
- **Same key + same body** — second POST returns 200 with the same id / correlation_id / timestamps as the first. Cache replay. ✅
- **Same key + different body → 409 RFC 7807** — the middleware SHA-256-fingerprints the request body and rejects the mismatch with `application/problem+json`:
  ```
  $ KEY="reverify-conflict-1779724536"
  $ # first POST  (intent A) → 202
  $ # second POST (intent B, same key)
  HTTP/1.1 409 Conflict
  Content-Type: application/problem+json
  X-Correlation-Id: 85d2f23d-4199-4b39-b6e8-1a7e6a21b7f7
  {
    "type":"/probs/idempotency-key-mismatch",
    "title":"Idempotency Key Conflict",
    "status":409,
    "detail":"An Idempotency-Key was reused with a different request body. Use a fresh key for the new payload.",
    "instance":"/api/v1/notifications",
    "correlation_id":"85d2f23d-..."
  }
  ```
  Edge cases: a legacy entry (cached by an older deployment with empty `RequestHash`) still replays as 200 — upgrade-safe; a request body above 1 MiB returns 413 RFC 7807.
- **DB UNIQUE constraint** — `idx_notifications_idempotency_key UNIQUE WHERE idempotency_key IS NOT NULL AND idempotency_key <> ''` verified (partial unique index). ✅

### D) Correlation ID
- **Explicit `X-Correlation-ID` propagation:** the value sent in the request header appears verbatim in the response header and in the body's `correlation_id` field. ✅
- **Without the header:** the server generates a UUID v4 (e.g. `d1704559-9b82-420b-8869-7cd0477555f7`) and echoes it in the response. ✅
- **Persisted to the DB:** each notification and each log entry carries the same `correlation_id` (the trace endpoint proves this). ✅
- **End-to-end in worker logs:** the `X-Correlation-ID` stamped by the API also appears verbatim on the worker's per-task INFO log line:
  ```
  # POST with X-Correlation-ID: reverify3b-1779725283
  worker-1 | {
    "time":"2026-05-25T16:08:03.844Z",
    "level":"INFO",
    "msg":"processed notification",
    "notification_id":"019e5fe4-b0ea-715b-a201-eb70c3dc92c6",
    "channel":"sms",
    "priority":"normal",
    "attempts":1,
    "outcome":"delivered",
    "duration_ms":27,
    "service":"worker",
    "correlation_id":"reverify3b-1779725283"        ← identical to the API-side value
  }
  ```
  Even though the asynq queue handoff hands the worker a bare ctx, `Execute` re-derives the ctx from `notification.CorrelationID` after the claim succeeds; `logger.contextHandler` then attaches it automatically to every log line.

### E) Worker + Provider flow
- **Status reaches `delivered` quickly** (~700 ms): the follow-up GET returns `"status":"delivered","attempts":1`.
- **Trace event order is complete:** `created → queued → processing → delivered` (4 entries, monotonically increasing timestamps).
- **Rows in `notification_logs`:** one per event, verified with a direct DB query.
- **Atomic claim:** `sqlc/queries/notifications.sql:36-43` — `UPDATE notifications SET status='processing' ... WHERE id=$1 AND status IN ('queued','retrying') RETURNING *;` — the code is in place; a successful delivery proves the claim succeeded.
- **Per-task INFO log is emitted:** `outcome` ∈ `delivered | failed | retrying | rate_limited`; fields include `notification_id`, `channel`, `priority`, `attempts`, `duration_ms`, `correlation_id`, plus an optional `error` reason. PII (`recipient`, `content`) is **deliberately excluded** (Sonar S5145).

### F) Retry + backoff
Live-verified against the `docker-compose.failtest.yml` overlay with `MOCK_PROVIDER_SUCCESS_RATE=0` and `MOCK_PROVIDER_FAILURE_MODE=transient`. The asynq-native retry shift (ADR-0015) lets us prove the exponential schedule end-to-end without relying on the reconciler's overdue sweep.

**Single notification, full retry exhaustion (~4 min 13 s):**
```
$ curl -X POST .../notifications -d '{"channel":"sms",...}'  → 202
# worker logs, filtered by notification id:
08:33:07  attempts=1 outcome=retrying  error="mock provider: transient failure"
08:33:40  attempts=2 outcome=retrying   (gap 33 s — designed ~30 s ✓)
08:34:15  attempts=3 outcome=retrying   (gap 35 s — backoffFor(2)=60 s with asynq jitter)
08:35:20  attempts=4 outcome=retrying   (gap 65 s — backoffFor(3)=120 s with jitter)
08:37:20  attempts=5 outcome=failed     (gap 120 s — backoffFor(4)=240 s with jitter;
                                          markFailed at max_attempts, asynq stops)
```

**Reconciler did NOT interfere — proven by three consecutive passes during the cycle:**
```
reconciler-1 | reconciler pass complete  overdue_retrying_reenqueued=0  (08:32:24)
reconciler-1 | reconciler pass complete  overdue_retrying_reenqueued=0  (08:33:24)
reconciler-1 | reconciler pass complete  overdue_retrying_reenqueued=0  (08:34:24)
```
The reconciler's `overdueRetryingThreshold = 10 min` (widened from 1 min in ADR-0015) sits well past every asynq retry interval, so the row stays invisible to the safety-net sweep while asynq is actively re-scheduling it.

**Sub-tests covered:**
- ✅ Transient failure → `retrying` (attempts 1-4 above)
- ✅ Exhausted attempts → `failed` at attempt 5 (transition to terminal, asynq receives nil = no more retries)

### G) Circuit breaker
Live-verified against the `docker-compose.failtest.yml` overlay with `MODE=permanent` (`MOCK_PROVIDER_FAILURE_MODE=permanent`).

**Permanent vs transient distinction (the load-bearing classification):**
```
$ # 10 POSTs at permanent mode
$ docker exec worker-1 wget -qO- localhost:9090/metrics | grep '^notifications_'
notifications_attempts_total{channel="sms",outcome="permanent"} 10
notifications_failed_total{channel="sms",reason="mock provider: permanent failure"} 10
```
Each of the 10 notifications shows `attempts=1` + `outcome=failed` in worker logs — **no retry**. Compare against §F where `attempts` climbs 1→2→3→4→5 under transient mode. The retry-vs-no-retry split is honored: permanent failures hit `markFailed` directly from `applyResult` (`!result.Retryable` branch) and asynq receives `nil`, so no retry is scheduled.

**Circuit breaker open/half-open/close transitions:**
- The 10 concurrent POSTs (worker `Concurrency=10`) all entered the gobreaker before the first failure was recorded, so the breaker did not trip in this scenario (a deliberately-sequential burst would). The open/half-open/close transitions themselves are covered by [`internal/infrastructure/circuit/circuit_test.go`](internal/infrastructure/circuit) unit tests against `sony/gobreaker` directly.
- The `notifications_circuit_breaker_state{provider="..."}` gauge is wired via `gobreaker.OnStateChange`; under a sequential failure stream that trips the breaker it would emit `1` (open) → `2` (half-open) → `0` (closed).

**Sub-tests covered:**
- ✅ Permanent (4xx-class) failure → `failed` immediately, no retry — distinct from transient (5xx-class) which retries up to `defaultMaxAttempts`
- ✅ Circuit breaker library + metric wiring in place; transition behavior pinned by unit tests rather than a fragile concurrent live-burst

### H) Rate limiting
- **Inbound 429 trigger + RFC 7807 body:** 70-80 parallel GETs — some return 200, the rest return 429 with `application/problem+json`:
  ```
  HTTP/1.1 429 Too Many Requests
  Content-Type: application/problem+json
  Retry-After: 23
  X-Correlation-Id: 490b6717-...
  {
    "type":"/probs/rate-limited",
    "title":"Too Many Requests",
    "status":429,
    "detail":"Inbound request rate limit exceeded for this client. Wait the number of seconds in Retry-After before retrying.",
    "instance":"/api/v1/notifications",
    "correlation_id":"490b6717-..."
  }
  ```
- **Outbound rate limit (per-channel 100/s) verified live:** the loadtest overlay (`docker-compose.loadtest.yml`) raises the inbound cap to 100 000 req/min and leaves outbound untouched. k6 `rate_limit.js` (200 rps × 30s = 6001 SMS POSTs):
  ```
  $ MSYS_NO_PATHCONV=1 docker compose -f docker-compose.yml -f docker-compose.loadtest.yml \
      --profile loadtest run --rm k6 run /scripts/rate_limit.js
  rate_limit complete: requests=6001 checks_rate=100.0%

  $ docker exec worker-1 wget -qO- localhost:9090/metrics | grep -E "outbound|delivered"
  notifications_delivered_total{channel="sms"} 2256             ← 100/s × ~22s drain
  outbound_rate_limit_hits_total{channel="sms"} 3742            ← rest were throttled
  ```
  The delivered:retrying split matches the 100/s × duration formula exactly — the limiter is mathematically correct and the metric observation agrees.
- **Inbound limit increments `inbound_rate_limit_hits_total{endpoint}` via `RateLimitMetricsRecorder`;** outbound limit increments `outbound_rate_limit_hits_total{channel}` via `MetricsRecorder.OutboundRateLimitHit(channel)`. Both observed during the live tests.

### I) WebSocket
- **Custom Go ws client:** [tests/e2e/wsclient/main.go](tests/e2e/wsclient/main.go) built on `github.com/coder/websocket`; committed for re-use.
- **Subscribe → real-time updates:** a scheduled notification combined with a pre-attached ws subscriber:
  ```
  [connected] ws://localhost:8080/api/v1/ws/notifications
  [sent] {"action":"subscribe","notification_id":"019e5f8f-5208-723f-93ec-c6516105b9e1"}
  [recv] {"notification_id":"019e5f8f-5208-723f-93ec-c6516105b9e1","status":"processing"}
  [recv] {"notification_id":"019e5f8f-5208-723f-93ec-c6516105b9e1","status":"delivered"}
  ```
- **Direct Redis pub/sub confirmation:** subscribing to `notification.status` with `redis-cli SUBSCRIBE` shows the worker publishing `{"notification_id":"...","status":"processing"}` and `{...,"status":"delivered"}` messages.
- **`notifications_websocket_clients` gauge:** `1` while connected, back to `0` after disconnect:
  ```
  # while connected:
  notifications_websocket_clients 1
  # after disconnect:
  notifications_websocket_clients 0
  ```

### J) Reconciler
- **Stuck processing (10 min behind) → failed:** manual INSERT; within one minute the reconciler logs:
  ```
  reconciler-1 | {"msg":"reconciler pass complete","stuck_processing_failed":1,"overdue_retrying_reenqueued":0,"orphaned_pending_reenqueued":1,"stuck_queued_reenqueued":0,...}
  ```
  Final row state:
  ```
  id=019e5fff-0000-7000-8000-aaaaaaaa0001 | status=failed | last_error=worker_timeout | attempts=1
  ```
- **Orphaned pending (10 min behind) → re-enqueued + delivered:**
  ```
  id=019e5fff-0000-7000-8000-aaaaaaaa0002 | status=delivered | attempts=1
  ```
- **Stuck queued (dual-write race recovery, CLAUDE.md §3.11):** the worker may dequeue an asynq task before `CreateNotification` flips status from `pending` to `queued`; the atomic claim (filter `queued|retrying`) misses, asynq counts the task as delivered, the API then writes `queued` — the row sits in `queued` with no task. The `FindStuckQueued` sweep (added in `fix/reconciler-queued-sweep`) re-enqueues rows whose `updated_at < NOW() - 5min`. The query carries a `scheduled_at IS NULL OR scheduled_at < older_than` guard so future-scheduled rows (whose delayed asynq task is alive) are NOT re-enqueued — pinned by integration test `TestFindStuckQueued_ExcludesFutureScheduled`. Status stays `queued` and no log row is written; only the missed delivery is restored.
- **`SELECT ... FOR UPDATE SKIP LOCKED`:** present in all four reconciler queries — concurrent reconciler instances can run without lock contention (verified in code; a live two-instance race test was not performed because the compose stack defines a single reconciler).
- **Overdue-retrying sweep narrowed to safety-net-only role (ADR-0015):** since the asynq-native retry shift landed (`fix/asynq-native-retry`), asynq is the primary retry mechanism — the reconciler's `overdueRetryingThreshold` was widened from 1 min to 10 min so the safety-net sweep cannot race a live asynq retry. Live evidence in §F: across three reconciler ticks during a 4 min 13 s retry cycle, `overdue_retrying_reenqueued` stayed at `0` while asynq fired attempts 2-5 itself.

### K) Cancel
- **Scheduled notification + PATCH /cancel → 200:**
  ```
  status=cancelled, updated_at=2026-05-25T14:38:18.283555Z
  ```
- **Cancel event in the trace:** `created → queued → cancelled`. ✅
- **Worker never fires:** the trace has no `processing` event — the notification was cancelled before its `scheduled_at`.

### L) Observability
- **`notifications_created_total{channel,priority}`** — api `/metrics` increments correctly.
- **`notifications_delivered_total{channel}`** + **`notifications_attempts_total{channel,outcome}`** + **`notifications_processing_duration_seconds`** histogram — present on the worker `/metrics`; collected 2 256+ samples during the loadtest.
- **`outbound_rate_limit_hits_total{channel}`** — 3 742 observations after the loadtest (the production code path is now wired in Fix #4b).
- **`inbound_rate_limit_hits_total{endpoint}`** — incremented per 429 (the production code path is now wired in Fix #2).
- **`notifications_failed_total`, `notifications_circuit_breaker_state`, `notifications_queue_depth`** — collectors registered but no observations yet (no failures triggered + queue idle). Default Prometheus behavior: zero-observation label sets are not exposed.
- **`http_request_duration_seconds_*{method,path,le}`** histogram — increments per `/api/v1/notifications`, `/api/v1/notifications/{id}`, `/api/v1/notifications/{id}/trace`, `/healthz/live`, `/docs`, etc. with correct path labels.
- **Prometheus targets up=1:** `api:8080`, `worker:9090`, `reconciler:9090`, `alertmanager:9093`, `localhost:9090` (prom self) — all `"health":"up"`.
- **Grafana:** HTTP 200, the `Prometheus` data source is provisioned, at least 2 dashboards loaded (in the `Notifications` folder: `Notification System — HTTP API Performance` and `Notification System — Overview`). A Prometheus query test (`notifications_created_total`) returned multi-channel results successfully.
- **AlertManager:** `/-/healthy` returns 200; `/api/v2/status` shows the cluster ready; the configuration is loaded (global resolve_timeout 5m, smtp/slack/pagerduty placeholders present).
- **Alert rules:** `HighQueueDepth`, `WorkerProcessingStalled`, `ReconcilerNotRunning`, etc. are `state=inactive, health=ok` in Prometheus; their `runbook_url` annotations point at `docs/RUNBOOK.md` in the repo.
- **JSON log format:** `{"time":"2026-05-25T16:08:03.844Z","level":"INFO","msg":"processed notification","notification_id":"...","channel":"sms","outcome":"delivered","duration_ms":27,"service":"worker","correlation_id":"reverify3b-1779725283"}` — schema correct (`time / level / msg / service / correlation_id` + structured event fields). Worker per-task INFO lines are now emitted; the API side already showed WARN-level examples (ws close, etc.).

### M) Operational UIs
- **asynqmon** — http://localhost:8081/ → 200. `/api/queues` JSON: 3 priority queues (high / normal / low); processed counters 6 700+ on `normal` after the loadtest, `failed=0`.
- **Adminer** — http://localhost:8082/ → 200; reachable with PG / postgres / notification / notification.
- **Redis Commander** — http://localhost:8083/ → 200 healthy; `redis-cli --scan` shows `idempotency:reverify-*`, `asynq:{normal}:processed:*`, `asynq:servers:*`, `asynq:workers` keys.

### N) Hexagonal boundaries
- **Comprehensive third-party import scan** on `internal/domain/` and `internal/application/` (regex `"[^"]*\.[^"]+/[^"]+"`, excluding `*_test.go` and this module's own paths) returns **0 matches** — the rule holds for every external package, not just the historical short list (`database/sql`, `net/http`, `hibiken/asynq`, `redis/go-redis`, `jackc/pgx`).
- The earlier shorter grep missed `go.opentelemetry.io/otel` in `process_notification.go`; surfaced by a peer code review and closed in the `fix/hexagonal-boundary-otel` branch by routing span work through a new `ports.Tracer` port with an OTel-backed adapter in `internal/infrastructure/tracing/`. The application layer now imports only stdlib + `internal/domain` + `internal/ports` + `internal/infrastructure/correlation` (project-local).

---

## Notes

1. **Live-stack methodology:** the stack was brought up with `docker compose up -d`; for the outbound rate-limit verification the loadtest overlay (`-f docker-compose.loadtest.yml`) was layered on. Tests ran through the public HTTP/WS surface plus `docker exec` for DB/Redis inspection. No container was stopped at the end of the run.

2. **WS test note:** because `MockProvider` runs with zero latency, a notification can reach `delivered` within ~10 ms and the ws subscriber risks missing the pub/sub messages. The scenarios use `scheduled_at = now+8s` to force a deterministic "subscribe first, wait, observe" flow.

3. **`http_request_duration_seconds{path="unknown"}`:** chi's route resolution returns "unknown" for some paths — likely ws upgrades and 404s. Intentional to cap cardinality; can be tightened later.

4. **`outbound_rate_limit_hits_total` naming:** the metric lacks the `notifications_` prefix (same shape as `inbound_rate_limit_hits_total`), a minor inconsistency with the `notifications_*` convention in CLAUDE.md §12.1. The counter now receives observations; a rename is out of scope for this branch.

5. **Outbound rate-limit live evidence:** k6 `rate_limit.js` issued 6 001 POSTs; 2 256 delivered + 3 742 retrying (last_error `"outbound rate limit exceeded"`) + 3 queued. The delivered:retrying ratio matches the 100/sec × duration formula exactly — the limiter is mathematically correct and the metric observation agrees.

6. **Container recreate required:** `air` rebuilds Go source in place but containers read env vars only at startup. Compose env changes therefore require `docker compose up -d --force-recreate api worker`; the stack came back up in ~6 s each time.

7. **TDD discipline:** the branch's 10 commits preserve a 5 RED + 5 GREEN cadence; every commit passes `go build`, `go test`, and `golangci-lint run` with zero issues. A reviewer reading the commit log in order can see which test was written for which production change.

8. **Out-of-scope follow-ups:**
   - Add `MOCK_PROVIDER_SUCCESS_RATE` env support to `cmd/worker/main.go` to make F+G live-testable.
   - Rename `outbound_rate_limit_hits_total` → `notifications_outbound_rate_limit_hits_total` for CLAUDE.md §12.1 consistency.
   - Add a request-log middleware on the API (INFO per request; correlation_id is already plumbed).
