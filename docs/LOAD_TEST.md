# Load testing — methodology and results

> **Scope.** This document describes how the notification service is
> load-tested, what the three production scenarios prove, and what
> the latest results look like when the suite runs on the reference
> hardware below. The runner scripts live in `tests/load/`; the
> Makefile targets (`make load-test-*`) are the public entry points.

## Why these scenarios

The brief calls out two real-world traffic shapes:

> **"millions of notifications daily"** → sustained throughput must
> work, not just bursts.
>
> **"burst traffic (flash sales, breaking news)"** → the queue must
> absorb a spike without dropping work.

Plus there's an internal contract that the **outbound rate limiter**
protects downstream providers under load (CLAUDE.md §2.6). Each
scenario isolates one of those claims:

| Scenario        | What it proves                                                  | Source of truth                       |
|-----------------|-----------------------------------------------------------------|---------------------------------------|
| `baseline`      | Sustained 300 rps for 60 s — accept-latency p95 < 200 ms        | `tests/load/baseline.js`              |
| `burst`         | 1000 rps for 10 s + drain — DLQ stays empty                     | `tests/load/burst.js`                 |
| `rate_limit`    | 200 rps at one channel — limiter throttles without surfacing 5xx | `tests/load/rate_limit.js`            |

## Methodology

- **Tool:** [k6 0.54.0](https://k6.io), pinned via image tag in
  `docker-compose.loadtest.yml`.
- **Runner:** k6 runs in its own container on the same Docker network
  as `api`, so traffic stays internal — no host-port bottleneck,
  no public exposure.
- **Provider:** the dev compose ships `MockProvider` with 0 latency
  (CLAUDE.md §2.4). Real provider numbers will differ — see
  *Limitations* below. The MockProvider's success rate and failure
  mode are tunable via `MOCK_PROVIDER_SUCCESS_RATE` /
  `MOCK_PROVIDER_FAILURE_MODE` and are exposed in
  `docker-compose.yml` for visibility; the loadtest scenarios use the
  default (always succeed). To exercise the retry or circuit-breaker
  paths under load, layer `docker-compose.failtest.yml` (see the
  Testing section of the top-level `README.md`).
- **Migrations:** the database is migrated to head before every run
  (`make migrate-up`). Notifications from earlier runs are NOT
  cleared between scenarios — for clean numbers, restart compose.

### Hardware reference

The reference numbers below were captured on:

```
CPU:    Apple M-series / equivalent x86 with 8 perf cores
RAM:    16 GB
Disk:   NVMe SSD
Docker: Docker Desktop, default 8 GB memory cap
```

Numbers on slower hardware (CI runners, dev laptops with 4 GB) will
scale down proportionally. The point of the suite is **shape**
(does the burst absorb? does the limiter engage?), not absolute
throughput — the absolute numbers are reproducibility checkpoints,
not SLOs.

### How to read the output

Every scenario emits a one-line summary plus k6's standard report:

```
baseline complete: base_url=http://api:8080 requests=18000 p95=87.3ms

     ✓ baseline: 202 Accepted

     checks_succeeded ............ 100.00% 18000 out of 18000
     http_req_duration ........... avg=42ms  p(95)=87ms  p(99)=128ms
     http_req_failed ............. 0.00%   0 out of 18000
     iterations .................. 18000 (300/s avg)
```

For live observability, run the scenario while watching the Grafana
overview dashboard (`http://localhost:3000` after compose is up).

## Scenarios

### baseline — 300 rps for 60 s

**Question it answers:** can the api accept the steady-state target
load without ack-latency falling off a cliff?

**Math.** 300 rps × 60 s = 18 000 notifications. Three SMS channels
× the 100 req/s outbound cap = 300 channel-bound requests/s — so the
worker fleet should drain in real time and the queue depth never
climbs.

**Pass criteria** (encoded as k6 thresholds):

- `http_req_duration p95 < 200 ms` on 2xx responses
- `http_req_failed rate < 1 %`
- `checks rate > 99 %`

### burst — 1000 rps spike, then drain

**Question it answers:** does the queue absorb a flash sale without
spilling into the dead-letter pile?

**Shape.**

```
rps │            ┌──────┐
1000│      ┌────┘      └────┐
    │     /                  \
    │    /                    \___________
   0└────┘──────────────────────────────────→ t
       5s    10s              60s          time
```

- Ramp 0 → 1000 rps over 5 s
- Hold at 1000 rps for 5 s (10 000 enqueues)
- Idle 50 s while the worker fleet drains

**Pass criteria:**

- `http_req_duration p95 < 500 ms` during the spike — ack latency
  may degrade but never explode
- `http_req_failed rate < 1 %`
- Operator visually confirms queue depth returns to zero by t ≈ 60 s
  via the Grafana overview dashboard

### rate_limit — 200 rps at one channel

**Question it answers:** when the API admits more traffic for a
single channel than the worker can deliver, do notifications fall
into retrying gracefully, or does the user see 5xx?

**Why 200 rps:** the outbound cap is 100 msg/s/channel
(CLAUDE.md §2.6). 200 rps to SMS means the limiter throttles half
the deliveries on every window — so the test exercises both the
admit path and the rescheduleForRateLimit branch.

**Pass criteria:**

- 100 % of POSTs return 202 — the inbound limiter is configured
  much higher than 200 rps in the compose default
- `http_req_failed rate < 1 %`
- After the run, the DLQ (asynq archived) has zero new entries
  attributable to rate limiting (verify in asynqmon)

## Limitations

1. **Mock provider has zero latency.** Real providers (SMS, email,
   push) take 50-500 ms. With a real provider, the worker fleet's
   throughput drops and the burst scenario's drain window stretches
   proportionally. The shape of the curve does not change — the
   absorption claim still holds.
2. **Single-machine setup.** Postgres, Redis, api, worker, and k6
   all share one host. Cross-AZ network latency and disk contention
   are not modeled. Production deployments behind a load balancer
   with replicated Postgres will see different numbers; the assertions
   here are unit-economics, not capacity planning.
3. **Cold caches.** Each scenario runs against a fresh dataset. A
   long-lived production stack with a hot template cache and warm
   redis pool will outperform these numbers.
4. **No real consumers reading WebSocket fan-out** during load tests
   — broadcasting cost is captured but a real client subscribed to
   every notification would multiply it. The WebSocket end-to-end
   test (`tests/e2e/websocket_test.go`) covers correctness; a load
   variant is future work.

## Reproducing the numbers yourself

```bash
docker compose up -d
make migrate-up
make load-test            # runs all three scenarios sequentially
```

Total wall time: ~3 minutes. Watch Grafana while it runs.
