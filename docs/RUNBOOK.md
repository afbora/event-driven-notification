# RUNBOOK

> Operator-facing playbook for every Prometheus alert in
> `deploy/prometheus/alerts.yml` (CLAUDE.md §3.8 / §12.5). When a
> page fires, the alert annotation's `runbook_url` points at the
> matching section below. Every entry follows the same shape:
> **What • Check • Causes • Remediate • Escalate • Dashboards**.
>
> Severity convention:
>
>   - **critical** — page someone now. Customer impact or imminent
>     data loss.
>   - **warning** — work-hours follow-up. Degraded behavior the
>     service can survive for an hour without anyone losing sleep.

## Dashboards (quick links)

- [Notifications Overview](http://localhost:3001/d/notifications-overview)
- [HTTP API Performance](http://localhost:3001/d/notifications-api)
- [Worker & Queue Health](http://localhost:3001/d/notifications-worker)

---

## HighQueueDepth

**Severity:** critical · **Fires:** queue depth > 10000 for 5 min.

**What it means:** the asynq priority queues are accumulating faster
than the worker fleet can drain them. New POSTs still succeed, but
delivery latency is climbing.

**Check:**

- Worker & Queue Health → Queue depth panel — which priority is
  growing?
- Worker & Queue Health → Active workers — is the fleet smaller
  than expected?
- Logs: `docker compose logs worker | tail -100`.

**Common causes:**

- Worker pod crash-looping.
- A spike of incoming traffic exceeded planned capacity.
- Outbound rate limiter throttling the provider (look at
  `outbound_rate_limit_hits_total`).
- Provider latency degraded — each in-flight call holds a worker
  slot longer.

**Remediate:**

1. `docker compose ps worker` — confirm running, not restart-looping.
2. Scale up: `docker compose up -d --scale worker=3` (or your
   orchestrator's scale command).
3. If provider is slow, accept a temporary backlog — alert clears
   on its own when latency returns.

**Escalate:** if depth grows for 30 min after scaling, the queue
backbone (Redis) may be sick — page DB / infrastructure on-call.

---

## WorkerProcessingStalled

**Severity:** critical · **Fires:** zero throughput for 5 min with
a non-empty queue.

**What it means:** workers are scheduled and queue has work, but
nothing is moving. Either the worker process is wedged or the
provider call is hanging.

**Check:**

- `docker compose logs worker | tail -50` — recurring panics? stalled
  goroutine traces?
- Worker & Queue Health → Provider call latency — has it gone to
  infinity?
- DB: `SELECT id, status, updated_at FROM notifications WHERE status = 'processing' ORDER BY updated_at LIMIT 20;`
  Rows older than 5 min point at worker hangs.

**Common causes:**

- A provider implementation deadlock.
- Outbound network blocked (firewall, DNS).
- Database deadlock or extreme contention on the atomic-claim row.

**Remediate:**

1. Restart the worker fleet: `docker compose restart worker`. The
   reconciler picks up stuck `processing` rows after 5 min and
   marks them failed — they will be re-enqueued automatically.
2. If the issue is a wedged provider, expect `CircuitBreakerOpen`
   to fire soon — the breaker will short-circuit further calls.

**Escalate:** Page the on-call channel-provider integration owner if
restart does not unblock within 10 min.

---

## ReconcilerNotRunning

**Severity:** critical · **Fires:** reconciler scrape target missing
or down for 5 min.

**What it means:** the safety-net process that cleans up stuck or
orphaned notifications is gone. Stuck rows will pile up silently.

**Check:**

- `docker compose ps reconciler` — exited? failing health check?
- Prometheus → Status → Targets — is the `reconciler` job DOWN?
- `docker compose logs reconciler | tail -50` — fatal exit?

**Common causes:**

- Container OOM killed.
- Config file parse error on startup.
- Postgres unreachable from the reconciler container.

**Remediate:**

1. `docker compose up -d reconciler` to restart.
2. If migration drift is suspected: `make migrate-up` from the
   project root.

**Escalate:** if Postgres is the underlying issue, this alert will
fire alongside `RedisUnreachable` and `DatabaseConnectionPoolExhausted` —
page DB on-call.

---

## HighFailureRate

**Severity:** critical · **Fires:** failure rate > 50 % over the
last 5 min.

**What it means:** more than half of delivery attempts are failing.
Could be a provider outage, a misconfiguration, or a bad deploy.

**Check:**

- Notifications Overview → Delivery success rate per channel — which
  channel is responsible?
- Worker & Queue Health → DLQ inspection table — `reason` label tells
  you what kind of failure (permanent vs transient vs worker_timeout).
- Recent deploy? `git log --oneline -10` on the deployed branch.

**Common causes:**

- Provider credentials rotated / expired.
- Provider API contract change (4xx everywhere).
- Bad release introducing a regression in the worker code path.

**Remediate:**

1. If a channel is 100 % failing, disable that channel via config
   (future feature) or roll back the deploy.
2. If credentials are bad, refresh from the secrets manager and
   `docker compose restart worker`.

**Escalate:** every 5 min of unresolved high failure rate during
business hours.

---

## DLQGrowthRate

**Severity:** critical · **Fires:** failed notifications attributable
to retry exhaustion or worker timeout growing > 100/min.

**What it means:** the system has stopped recovering from transient
failures — they are now landing in the terminal `failed` state at
a rate that will deplete inventory fast.

**Check:**

- Worker & Queue Health → DLQ inspection — group by `channel` and
  `reason`.
- Worker & Queue Health → Attempt outcomes — is `transient` count
  also spiking?

**Common causes:**

- Sustained provider degradation (no transient call ever succeeds).
- Reconciler not running (so retries never get re-enqueued and
  hit max attempts).

**Remediate:**

1. Verify reconciler is alive (`ReconcilerNotRunning` should also
   be firing if not).
2. Check provider status pages.
3. Consider pausing inbound traffic temporarily by lowering
   `INBOUND_RATE_LIMIT` until the provider recovers.

**Escalate:** page provider liaison.

---

## DatabaseConnectionPoolExhausted

**Severity:** critical · **Fires:** pool usage > 90 % for 5 min.

**What it means:** Postgres connection pool is nearly out of slots.
The next batch of requests will start blocking on `pool.Acquire`,
which cascades into HTTP timeouts.

**Check:**

- `docker compose exec postgres psql -U notification -c "SELECT count(*) FROM pg_stat_activity WHERE datname = 'notification';"`
- Worker & Queue Health → DLQ inspection table for `update status`
  errors that indicate connection failures.

**Common causes:**

- Long-running query blocking the pool.
- Spike of concurrent worker pods overwhelming the pool size.
- Database maintenance window slowed every query.

**Remediate:**

1. Identify the blocking query: `SELECT pid, state, query_start, query FROM pg_stat_activity WHERE state != 'idle' ORDER BY query_start LIMIT 20;`.
2. Cancel pathological queries: `SELECT pg_cancel_backend(<pid>);`.
3. Increase pool size in `internal/infrastructure/config/config.go`
   and re-deploy if traffic legitimately requires more.

**Escalate:** DBA on-call if no obvious culprit query.

---

## RedisUnreachable

**Severity:** critical · **Fires:** all app instances down for 1 min.

**What it means:** every api / worker / reconciler instance is
failing its scrape — typically because they can't reach Redis (or
Postgres). The whole notification stack is down.

**Check:**

- `docker compose ps` — what's actually running?
- `docker compose logs redis postgres | tail -20`.
- Network: `docker network inspect notification-net`.

**Common causes:**

- Redis container OOM / crash.
- Network plugin restart.
- Disk full preventing Redis from accepting writes.

**Remediate:**

1. Restart Redis: `docker compose restart redis`.
2. Then restart the app tier: `docker compose restart api worker reconciler`.
3. If Redis won't come back up, free disk first.

**Escalate:** infrastructure on-call immediately — this is the
loudest alert in the catalog.

---

## HighProcessingLatency

**Severity:** warning · **Fires:** p99 > 30 s for 10 min.

**What it means:** the tail of notification processing is taking
unusually long. Most notifications still succeed, but a slow tail
points at upcoming saturation.

**Check:**

- Notifications Overview → Processing latency (p50 / p95 / p99) —
  is p95 also climbing or is it only p99?
- Worker & Queue Health → Provider call latency per channel —
  channel-specific?

**Common causes:**

- One channel's provider is slower than usual.
- Database is under load (check `DatabaseConnectionPoolExhausted` is
  not also firing).
- A GC pause storm on the worker (unusual; check `go_gc_duration_seconds`
  in Grafana).

**Remediate:** no immediate action required at warning severity.
Log a follow-up to investigate during business hours.

---

## CircuitBreakerOpen

**Severity:** warning · **Fires:** breaker open (state = 1) for 5 min.

**What it means:** sony/gobreaker tripped on consecutive failures
from a provider; further calls fail-fast for the open window. The
worker is **not** stalled — failed attempts trickle back as retrying.

**Check:**

- Notifications Overview → Circuit breaker states — which provider?
- Worker & Queue Health → Attempt outcomes — `transient` rate
  should have collapsed (breaker prevents most calls).

**Common causes:**

- Provider outage (the system reacted correctly).
- Network blip that did not resolve.
- A misconfigured provider (always-failing).

**Remediate:** the breaker recovers on its own once the half-open
probe succeeds. Verify the provider is back via its status page.

---

## InboundRateLimitFrequentlyHit

**Severity:** warning · **Fires:** 429 emissions > 100/min for 10 min.

**What it means:** the inbound limiter is rejecting a steady stream
of requests. Either we are being abused, or the production cap is
undersized for legitimate traffic.

**Check:**

- HTTP API Performance → Inbound rate limit hits per minute —
  same source IPs repeatedly? same endpoint?
- Sample a few `X-Real-IP` / `X-Forwarded-For` from access logs.

**Common causes:**

- A misbehaving client retrying without backoff.
- A new integration that legitimately needs a higher cap.

**Remediate:** if abuse, capture IPs for blocklist; if legitimate,
raise `INBOUND_RATE_LIMIT` via env and redeploy api.

---

## OutboundRateLimitFrequentlyHit

**Severity:** warning · **Fires:** outbound throttle hits > 10/min
for 10 min.

**What it means:** the worker is constantly trying to deliver faster
than the per-channel cap. Notifications fall into retrying and
get rescheduled by the reconciler.

**Check:**

- Worker & Queue Health → Outbound rate limit hits per minute —
  which channel?
- Notifications Overview → Throughput — is created/min > the
  channel cap?

**Common causes:**

- Marketing campaign sent a bulk to one channel.
- Channel cap set too conservatively.

**Remediate:** raise `OUTBOUND_RATE_LIMIT` if the provider can
genuinely handle more. Otherwise let the limiter do its job —
notifications still deliver, just slightly delayed.

---

## StuckProcessingDetected

**Severity:** warning · **Fires:** reconciler marked > 10 rows as
`worker_timeout` in 5 min.

**What it means:** the reconciler is doing its job, but worker
crashes are happening at a notable rate.

**Check:**

- `docker compose logs worker | grep -i panic` — fresh stack traces?
- Worker & Queue Health → Worker instances active — is the count
  bouncing?

**Common causes:**

- A regression making the worker panic on a specific payload shape.
- Memory pressure (OOM killer reaping containers).

**Remediate:**

1. Identify the panic root cause from logs; redeploy with a fix
   or a recover/log shield.
2. Bump worker memory limit in `docker-compose.yml` if OOM.

---

## Adding a new alert

1. Define the rule in `deploy/prometheus/alerts.yml` with
   `for`, `severity`, and `annotations.{summary, runbook_url}`.
2. Add the matching section in this file with the standard
   What / Check / Causes / Remediate / Escalate / Dashboards
   shape.
3. The `runbook_url` anchor must match the GitHub-style slug of the
   alert name (lowercased, no spaces).
