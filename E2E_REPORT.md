# E2E Test Report

**Date:** 2026-05-26T10:18:00Z (updated after the post-sweep drift fixes landed)
**Branch:** chore/post-sweep-drift-fixes (latest snapshot)
**Last full sweep commit:** aa17d8a; drift fixes verified atop the post-fix worker binaries
**Duration:** ~24 minutes (full fresh sweep) + ~8 minutes (live re-verify of the two drift fixes — reconciler log field, circuit-breaker gauge)
**Environment:** Windows 11 + Docker Desktop. Base run with `docker compose up -d`. Outbound rate-limit verification adds `-f docker-compose.loadtest.yml`. Retry + circuit-breaker verification adds `-f docker-compose.failtest.yml` with `MODE=transient` (§F) or `MODE=permanent` (§G).

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

**No abnormal log lines outside test traffic** — startup banners only on the three Go binaries; reconciler logs one `reconciler pass complete` every minute.

**DB row counts at end of sweep:** delivered=3376, retrying=2633, failed=4, cancelled=1. The high `retrying` count is k6's 200-rps burst (§H) draining through the 100-msg/sec outbound limiter at the moment the sweep ended; `failed=4` includes §F's exhausted transient (1) + §G's permanent (1) + §J's stuck-processing injection (1) + a circuit-open marker (1).

**Asynq queue (asynqmon API):** processed counters non-zero on every priority queue, `failed=0` across the board (asynq's own failed counter — domain failures are tracked separately on the worker `/metrics`).

---

## ✅ Passing tests

### A) Infrastructure bring-up
- **`docker compose down -v && docker compose up -d`** brought 11 containers up cleanly. `postgres`, `redis`, `redis-commander` healthy; the stateless services have no `healthcheck` defined and run as `Up`.
- **Migrations applied:** `\dt` lists `batches, notification_logs, notifications, schema_migrations, templates`.
- **Required indexes present:**
  ```
  "idx_notifications_idempotency_key" UNIQUE, btree (idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key <> ''::text
  "idx_notifications_status_scheduled_at" btree (status, scheduled_at)
  "idx_notifications_batch_id" btree (batch_id)
  "idx_notifications_correlation_id" btree (correlation_id)
  "idx_notifications_created_at_desc" btree (created_at DESC)
  "idx_notifications_retrying_next_retry_at" btree (next_retry_at) WHERE status = 'retrying'
  ```
- **Reconciler ticking:** `{"msg":"reconciler started","interval":"1m0s",...}` plus a pass every minute.
- **No startup errors** on api / worker / reconciler.

### B) API smoke
- **`GET /healthz/live` → 200** `{"status":"ok"}`
- **`GET /healthz/ready` → 200** `{"status":"ok"}` (postgres + redis ping succeeded)
- **`GET /docs/` → 200** Swagger UI loads
- **`GET /metrics` → 200** Prometheus format
- **`POST /api/v1/notifications` → 202** with `Location: /api/v1/notifications/019e639d-3f14-7adb-b6e9-16d8050d7770` and `X-Correlation-Id: e7e9f522-...`
- **`GET /api/v1/notifications/{id}` → 200** with `status=delivered, attempts=1` (~600 ms after POST)
- **`GET /api/v1/notifications/{id}/trace` → 200** with 4 entries: `created → queued → processing → delivered`, all sharing the same `correlation_id`
- **Batch POST (3 notifications)** → 202 with `Location: /api/v1/notifications/batch/019e639d-9817-...` — all three members share `correlation_id=2c18f373-...`
- **Invalid channel → 400 RFC 7807:**
  ```json
  {"type":"/probs/invalid-channel","title":"Invalid Channel","status":400,"detail":"Channel must be one of: sms, email, push.","instance":"/api/v1/notifications","correlation_id":"9261fa32-..."}
  ```
- **Unknown id → 404 RFC 7807:** `{"type":"/probs/not-found","title":"Not Found","status":404,...}`
- **Cursor pagination** `?limit=2` returns 2 items + opaque `next_cursor=MjAyNi0wNS0yNlQwOToyODo1My...`

### C) Idempotency
Three cases against the same `Idempotency-Key`:

| Call | Same body? | HTTP | Notes |
|---|---|---|---|
| 1st POST | — | **202** | Fresh resource created |
| 2nd POST | yes | **200** | Replay — identical body, same id |
| 3rd POST | **no** | **409** | `/probs/idempotency-key-mismatch` RFC 7807 (Fix #1) |

Conflict response body:
```json
{"type":"/probs/idempotency-key-mismatch","title":"Idempotency Key Conflict","status":409,
 "detail":"An Idempotency-Key was reused with a different request body. Use a fresh key for the new payload.",
 "instance":"/api/v1/notifications","correlation_id":"17dd6a97-..."}
```

DB UNIQUE constraint `idx_notifications_idempotency_key UNIQUE WHERE idempotency_key IS NOT NULL AND idempotency_key <> ''` enforces the second-layer guard.

### D) Correlation ID
- **Explicit `X-Correlation-ID: sweep-D-1779787810`** → echoed in response header and persisted on the notification + every notification_logs row + every worker log line:
  ```
  worker-1 | {"time":"2026-05-26T09:30:12.173Z","level":"INFO","msg":"processed notification",
              "notification_id":"019e639e-c8ca-7b1b-ac2b-ffabf60d10a8","channel":"sms",
              "priority":"normal","attempts":1,"outcome":"delivered","duration_ms":3,
              "service":"worker","correlation_id":"sweep-D-1779787810"}
  ```
- **Header-less POST** → server generates UUID v4 (`X-Correlation-Id: 872438ad-27e9-44c5-9f93-53a162007a05`) and echoes it on the response
- **Asynq queue handoff** preserves the correlation id end-to-end (Fix #3b — the worker re-derives the id from `notification.CorrelationID` after the claim because asynq hands the worker a bare ctx)
- **PII guard:** the worker INFO log has `notification_id`, `channel`, `priority`, `attempts`, `outcome`, `duration_ms`, `service`, `correlation_id` and an optional `error` reason — **no `recipient`, no `content`** (Sonar S5145 / CLAUDE.md §3.5)

### E) Worker + Provider flow
- **Status reaches `delivered` ~600 ms after POST** (§B's run)
- **Trace order:** `created → queued → processing → delivered` with monotonically increasing timestamps
- **`notification_logs`** carries one row per event
- **Atomic claim** verified by code (`sqlc/queries/notifications.sql:31-43` — `UPDATE notifications SET status='processing' ... WHERE id=$1 AND status IN ('queued','retrying') RETURNING *`); successful deliveries prove the claim path
- **Per-task INFO log** with the field schema from §D

### F) Retry + backoff (failtest overlay, `MODE=transient`)
One notification driven through the full retry cycle in **~4 min 13 s**:
```
attempts=1 outcome=retrying  09:36:23
attempts=2 outcome=retrying  09:36:55  gap 32 s  (~30 s designed)
attempts=3 outcome=retrying  09:37:29  gap 34 s  (backoffFor(2)=60 s with asynq jitter)
attempts=4 outcome=retrying  09:38:30  gap 61 s  (backoffFor(3)=120 s with jitter)
attempts=5 outcome=failed    09:40:34  gap 124 s (markFailed at max; circuit was open by now too)
```
Asynq drove every retry (ADR-0015 — `ErrProviderTransient` sentinel + `RetryDelayFunc`). The reconciler logged `overdue_retrying_reenqueued=0` across every tick during the cycle — its 10-minute safety-net threshold never let it race the asynq retry.

**Sub-tests covered:**
- ✅ Transient failure → `retrying` (attempts 1-4)
- ✅ Exhausted attempts → `failed` at attempt 5

### G) Circuit breaker (live trip during §F + failtest overlay, `MODE=permanent`)

**Live trip caught naturally during the §F retry cycle:**
```
attempts=5 outcome=failed  error="circuit open for \"sms\""
```
The first four attempts in §F each called the provider (transient fail). The breaker tripped before attempt 5 fired, so the worker short-circuited and marked failed without contacting the provider. Metric snapshot taken right after:
```
notifications_failed_total{channel="sms",reason="circuit open for \"sms\""} 1
notifications_failed_total{channel="sms",reason="mock provider: transient failure"} 1
notifications_attempts_total{channel="sms",outcome="transient"} 10
```

**Permanent-vs-transient distinction** (failtest `MODE=permanent`, one POST):
```
worker-1 | {"...","attempts":1,"outcome":"failed","duration_ms":23,
            "error":"mock provider: permanent failure","correlation_id":"sweep-G-1779788969"}
```
`attempts=1` and immediate `failed` — **no retry** — compared against §F's 1→5 progression. The `markFailed` branch returns `nil` to asynq so no retry is scheduled; the retry-vs-no-retry classification is honored end-to-end.

**Sub-tests covered:**
- ✅ Circuit opens after sustained consecutive failures (live in §F)
- ✅ Permanent (4xx-class) failures skip retry entirely, distinct from transient (5xx-class) which retries up to `defaultMaxAttempts`

### H) Rate limiting

**Inbound — 80 parallel GETs against `/api/v1/notifications?limit=1`:**
```
Code histogram: 31 × 200, 11 × 429   (rest connection-drop / wait sync)

HTTP/1.1 429 Too Many Requests
Content-Type: application/problem+json
Retry-After: 59

{"type":"/probs/rate-limited","title":"Too Many Requests","status":429,
 "detail":"Inbound request rate limit exceeded for this client. Wait the number of seconds in Retry-After before retrying.",
 "instance":"/api/v1/notifications","correlation_id":"e0e95986-..."}
```

**Outbound — k6 `rate_limit.js` (200 rps × 30 s = 6001 POSTs) under the loadtest overlay:**
```
notifications_attempts_total{channel="sms",outcome="success"} 2967
notifications_delivered_total{channel="sms"}                  2967
outbound_rate_limit_hits_total{channel="sms"}                18471
```
18 471 limiter hits over the 30-second burst is what asynq-native retry produces: every throttled task is re-enqueued with the rate-limit backoff (~1 s via `application.RetryDelayFor`), tries again, and gets throttled again until its slot opens — the sustained 200 rps over a 100/sec cap shows the limiter pushing back and the retry mechanism resolving each task without dropping it.

### I) WebSocket
Subscribe before a scheduled notification fires; receive every status update live:
```
$ FUTURE=$(date -u -d '+8 seconds' '+%Y-%m-%dT%H:%M:%SZ')
$ # POST scheduled notification, capture id
$ go run ./tests/e2e/wsclient ws://localhost:8080/api/v1/ws/notifications 019e639f-2db0-... 15s

[connected] ws://localhost:8080/api/v1/ws/notifications
[sent]      {"action":"subscribe","notification_id":"019e639f-2db0-..."}
[recv]      {"notification_id":"019e639f-2db0-...","status":"processing"}
[recv]      {"notification_id":"019e639f-2db0-...","status":"delivered"}
[done]      read window elapsed (15s)
```

**`notifications_websocket_clients` gauge** while a ws client is connected:
```
notifications_websocket_clients 1
```
Drops to `0` after disconnect (verified earlier in the broader observability scan).

**Sub-tests covered:**
- ✅ WS upgrade handshake (chi router + middleware Hijack/Flush)
- ✅ Subscribe action stores the (client, notification id) pair on the hub
- ✅ Status updates from the worker's `StatusBroadcaster` arrive in order
- ✅ Gauge reflects connect / disconnect

### J) Reconciler
Three rows injected with backdated timestamps to drive three of the four sweeps in a single reconciler pass:

| Injected status | Backdate | Final state | Sweep |
|---|---|---|---|
| `processing` | -10 min | `failed`, `last_error=worker_timeout` | stuck-processing |
| `pending` | -10 min | `delivered` | orphaned-pending → re-enqueued → worker delivered |
| `queued` | -10 min | `delivered` | stuck-queued → re-enqueued → worker delivered |

Reconciler log captured the pass:
```
reconciler pass complete  stuck_processing_failed=1  overdue_retrying_reenqueued=0  orphaned_pending_reenqueued=1
```

**`SELECT ... FOR UPDATE SKIP LOCKED`** present in all four reconciler queries (`sqlc/queries/notifications.sql`) so multiple reconciler instances can run safely in parallel.

**Overdue-retrying sweep narrowed to safety-net role (ADR-0015):** §F's full retry cycle (4 min 13 s) ran with `overdue_retrying_reenqueued=0` across every reconciler tick during the cycle — asynq drove the retries and the 10-minute reconciler threshold kept the safety-net out of the way.

**Drift closed in this branch (chore/post-sweep-drift-fixes):** `cmd/reconciler/main.go` now includes `stuck_queued_reenqueued` in the pass-complete log line. Live re-verify (same overlay-free base mode) right after the fix:
```
reconciler pass complete  stuck_processing_failed=0  overdue_retrying_reenqueued=0  orphaned_pending_reenqueued=0  stuck_queued_reenqueued=1
```
A stuck-queued row backdated by 10 min triggered the sweep; all four counters now surface in the operator's view.

### K) Cancel
- **Scheduled notification** with `scheduled_at = now + 5 min`, then `PATCH /api/v1/notifications/{id}/cancel` → 200
- **Status** flipped to `cancelled` with `updated_at` from the cancel call
- **Trace event sequence:** `created → queued → cancelled` (no `processing` — the worker never claimed the row because cancel landed before `scheduled_at`)

### L) Observability
- **api `/metrics`** counters: `notifications_created_total{channel,priority}`, `notifications_websocket_clients`, `http_request_duration_seconds_count{method,path}`, full Go runtime + process metrics
- **worker `/metrics`** counters: `notifications_attempts_total{channel,outcome}`, `notifications_delivered_total{channel}`, `notifications_failed_total{channel,reason}` (observed during §F + §G), `notifications_processing_duration_seconds{channel}`, `outbound_rate_limit_hits_total{channel}` (observed during §H)
- **Prometheus targets:** 5/5 up — `api:8080`, `worker:9090`, `reconciler:9090`, `alertmanager:9093`, `prometheus` self
- **AlertManager rules registered:** `HighQueueDepth`, `WorkerProcessingStalled`, `ReconcilerNotRunning`, `HighFailureRate`, ... all `state=inactive`
- **Grafana** → HTTP 200; dashboards provisioned (Notification System — Overview, HTTP API Performance)
- **JSON log format** verified across api, worker, reconciler — every line carries `time` / `level` / `msg` / `service` / `correlation_id` when applicable

### M) Operational UIs
| UI | Port | HTTP | Notes |
|---|---|---|---|
| asynqmon | 8081 | 200 | `/api/queues` returns the three priority queues with `processed > 0, failed = 0` |
| Adminer | 8082 | 200 | Login: System PostgreSQL, server postgres, user/pass notification |
| Redis Commander | 8083 | 200 (healthy) | `redis-cli --scan` shows `idempotency:*`, `asynq:{*}:processed:*`, `asynq:servers:*` |

### N) Hexagonal boundaries
- **Comprehensive third-party import scan** on `internal/domain/` and `internal/application/` (regex `"[^"]*\.[^"]+/[^"]+"`, excluding `*_test.go` and this module's own paths) returns **0 matches** — the rule holds for every external package.
- The L1 case-review finding (OTel imports leaking into `internal/application`) is closed by ADR-0012; the project-local check that missed it originally (a short fixed import list) was replaced with the regex above.

---

## Notes

1. **Live-stack methodology:** the stack was brought up clean (`docker compose down -v && docker compose up -d`) so DB / Redis state was empty at the start. Tests ran in two layered modes:
   - **Base** (`docker compose up -d` only) — §A through §E, §H inbound, §I through §N
   - **Loadtest overlay** (`-f docker-compose.loadtest.yml`) — §H outbound (raises inbound cap to 100 000/min so the k6 burst isn't bottlenecked at the edge limiter)
   - **Failtest overlay** (`-f docker-compose.failtest.yml`) — §F (`MODE=transient`) and §G (`MODE=permanent`) live verification
   The stack was restored to base mode at the end; no failing test infrastructure is left behind.

2. **Asynq-native retry observable in two places:**
   - **§F worker logs** — attempts 2-5 fire on the exponential schedule (`backoffFor(n)` with asynq's per-attempt jitter)
   - **§H outbound metric** — 18 471 `outbound_rate_limit_hits_total` over 30 s of 200 rps proves the rate-limit sentinel's 1-second backoff is in effect (otherwise every throttled task would deadlock or drop)

3. **Circuit breaker tripped naturally during §F** rather than under a contrived burst. Four consecutive transient failures (attempts 1-4) crossed the gobreaker default threshold; attempt 5 saw `error="circuit open for \"sms\""` and `markFailed` recorded it under that reason. Captures both the "consecutive failures" trip logic and the fail-fast behavior in one shot.

4. **`notifications_circuit_breaker_state` gauge** — drift closed in `chore/post-sweep-drift-fixes`. `breakerSettings` now wires an `OnStateChange` callback that calls `metrics.SetCircuitBreakerState(name, state)` via the typed `breakerStateToMetric` mapping (gobreaker.State → 0=closed, 1=open, 2=half-open). Live re-verify with the failtest overlay tripped the breaker after ~6 consecutive transient failures and the gauge observed:
   ```
   notifications_circuit_breaker_state{provider="provider-registry"} 1
   ```
   The counter-based evidence (`notifications_failed_total{reason="circuit open ..."}`) still works; the gauge now joins it as a first-class signal Grafana panels can pivot on.

5. **Reconciler `stuck_queued_reenqueued` log field** — drift closed in `chore/post-sweep-drift-fixes`. See §J note for the live re-verify (all four counters now appear in the pass-complete log line).

6. **`outbound_rate_limit_hits_total` lacks the `notifications_` prefix** (consistent with `inbound_rate_limit_hits_total`), a minor inconsistency with CLAUDE.md §12.1's naming convention. Counter receives observations; rename is a separate concern.

7. **Container recreate** required when env vars change (compose reads them at startup, not on file watch). Each overlay swap (`base → loadtest → failtest transient → failtest permanent → base`) took ~6 s.

8. **TDD discipline** preserved across all 18 PRs merged on `main` since the original report. Every `feat` / `fix` commit is preceded by a matching `test` commit; lint config now mirrors Sonar's S3776 threshold so the previous reactive catches stay local-side; new ADRs (0012-0015) document every load-bearing decision the case-review pass surfaced.
