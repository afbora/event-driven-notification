# PLAN.md — Phase-by-Phase Roadmap

> **Purpose:** This document is the execution plan for building the notification system. It is intentionally public in the repository so reviewers can see the order, rhythm, and reasoning behind the development process.
> **For Claude Code:** Work one phase at a time. Do not jump ahead. At the end of each phase, surface the work, the human reviews and commits, then we move to the next phase.

---

## Working Agreement

- **One phase at a time.** Each phase has a clear entry and exit criterion.
- **TDD rhythm visible in commits.** Every feature commit is preceded by a test commit.
- **Conventional Commits.** `<type>(<scope>): <subject>`, imperative, lowercase, no period.
- **Feature branches and PRs.** Each phase ships as one or more PRs. Branches named `feat/phase-N-short-description` or similar.
- **The human runs all `git` commands.** Claude Code surfaces commit messages and command sequences; the human executes them.
- **Stop and ask** at any ambiguity. Never guess.
- **Consult skills.** When a task matches one of the 15 skills in `.claude/skills/`, follow that skill's recipe rather than inventing a new approach.

---

## Phase Index

| # | Name | Estimated Effort | Branch |
|---|---|---|---|
| 1 | Foundation, Skeleton & Skills | 3h | `chore/phase-1-foundation` |
| 2 | Domain & Application (TDD) | 3h | `feat/phase-2-domain` |
| 3 | Adapters | 3h | `feat/phase-3-adapters` |
| 4 | HTTP API & WebSocket | 3.5h | `feat/phase-4-http-ws` |
| 5 | Integration, E2E & Load Tests | 3.5h | `test/phase-5-integration` |
| 6 | Observability & Alerting Stack | 3h | `feat/phase-6-observability` |
| 7 | CI/CD, Diagrams & Polish | 3h | `ci/phase-7-pipeline` |

**Total estimated: ~22 hours.** The brief allows extensions; quality is the priority. The additional time over the brief's 12-hour estimate covers comprehensive tests, the full observability stack (AlertManager + Grafana with runbook entries), the operational alerting interpretation of "Failure Handling" (ADR-0007), the reconciliation safety net for stuck notifications, the notification trace endpoint, a thorough CI/CD pipeline (lint, multi-suite tests, SonarCloud, govulncheck, CodeQL), a Bruno API collection for one-click endpoint exploration, k6 load testing with three scenarios (baseline / burst / rate-limit-trigger) backed by `LOAD_TEST.md`, and a thorough README with architecture diagrams, capacity analysis, and six status badges.

---

## Phase 1 — Foundation, Skeleton & Skills

**Goal:** A repository that compiles, lints, and runs `docker compose up` to bring up empty-but-wired services. No business logic yet. All 15 skill files in place so Claude Code has the conventions from day one. No `.env` file — everything in `docker-compose.yml`.

**Entry:** Empty repository with `CLAUDE.md`, `PLAN.md`, `BOOTSTRAP.md` committed.

**Tasks (each task = one commit):**

1. `chore: add gitignore, editorconfig, license`
2. `chore(claude): add 15 skill files in core, quality, operations categories`
   - Skills directory exists from the bootstrap; this commit captures it formally with a `.claude/skills/README.md` index.
3. `chore: initialize go module and folder structure`
   - Create `go.mod`, `cmd/{api,worker,reconciler,migrate}/main.go` with `func main() { fmt.Println("hello from <name>") }`.
   - Create `internal/{domain,application,ports,adapters,infrastructure}/.gitkeep`.
4. `chore: add golangci-lint configuration`
   - `.golangci.yml` with: govet, staticcheck, errcheck, gosec, revive, gocyclo, gocritic, unused, ineffassign, misspell.
5. `chore: add Makefile with test, lint, build, sqlc, migrate, openapi targets`
6. `chore: add Dockerfile (multi-stage build with distroless runtime)`
   - Single image, entrypoint switches between api/worker/reconciler via env var or command argument.
7. `chore: add docker-compose.yml with full service stack and inline env vars`
   - **Core:** postgres, redis, api, worker, reconciler.
   - **Operational UIs:** asynqmon, adminer, redis-commander.
   - **Observability:** prometheus, alertmanager, grafana.
   - `air` configured for `api`, `worker`, `reconciler` with volume mounts.
   - All services on a shared network with documented port mapping.
   - **No `.env` file.** Every required env var is set inline with a working default. Comments explain production overrides.
8. `docs: add initial README with quickstart and links to detailed docs`
   - The quickstart is literally `docker compose up -d` — nothing else.
9. `docs(api): add api/openapi.yaml skeleton with health endpoint only`
10. `docs(adr): add ADR-0001 through ADR-0011 documenting initial decisions`
    - 0001: Hexagonal architecture
    - 0002: PostgreSQL + sqlc (no ORM)
    - 0003: Redis + asynq for queue
    - 0004: Provider strategy pattern
    - 0005: No custom frontend; operational UI stack instead
    - 0006: WebSocket for real-time updates (brief literal)
    - 0007: Failure handling interpretation (includes alerting stack)
    - 0008: Three binaries, one image (api + worker + reconciler)
    - 0009: Atomic status claim pattern in worker
    - 0010: No `.env` file; all config in docker-compose.yml
    - 0011: Reconciler-based dual-write mitigation (no Outbox Pattern)
11. `ci: add backend ci workflow (lint, test, build)`
    - `.github/workflows/ci.yml`
    - Jobs: `lint` (golangci-lint), `test-unit` (race detector enabled), `build` (compiles all three binaries)
    - Triggers: push to `main`, all PRs
    - Caches: Go modules, build cache via `actions/setup-go@v5`
    - Uses `golangci/golangci-lint-action@v6` for the lint step
12. `ci: add pr title check workflow (conventional commits)`
    - `.github/workflows/pr-title.yml`
    - Uses `amannn/action-semantic-pull-request@v5`
    - Blocks merge if PR title does not follow Conventional Commits
13. `ci: add codeql security scanning workflow`
    - `.github/workflows/codeql.yml`
    - GitHub's built-in security scanning for Go
    - Triggers: push to `main`, all PRs, weekly schedule
    - Results appear in repository Security tab

**Exit criteria:**

- `docker compose up` brings every service up, even if APIs return 501/empty.
- `make lint` and `make test` pass (no tests yet, lint must be clean).
- All three GitHub Actions workflows (ci, pr-title, codeql) run green on PR.
- All 15 skill files exist and are committed.
- Operational UIs reachable: asynqmon on `http://localhost:8081`, Adminer on `http://localhost:8082`, Redis Commander on `http://localhost:8083`, Prometheus on `http://localhost:9090`, Grafana on `http://localhost:3001`, AlertManager on `http://localhost:9093` — even if they have nothing to display yet.
- No `.env` file exists.

**PR title:** `chore: phase 1 foundation, skeleton, and skills`

---

## Phase 2 — Domain & Application (TDD)

**Goal:** All business logic implemented as pure Go, fully tested, with zero external dependencies. The domain is so well-formed that we could ship it as a library.

**Entry:** Phase 1 merged. CI green.

**Approach:** Strict TDD. Every commit pair is `test` then `feat`. Use the `add-use-case` skill for every use case.

**Tasks:**

### 2A — Domain Layer

1. `test(domain): add failing tests for Channel value object`
2. `feat(domain): implement Channel value object (sms, email, push)`
3. `test(domain): add failing tests for Priority value object`
4. `feat(domain): implement Priority value object (low, normal, high)`
5. `test(domain): add failing tests for Status state machine`
6. `feat(domain): implement Status state machine with transition validation`
   - Valid: `pending → queued → processing → delivered | failed | retrying`; `retrying → processing`; `pending|queued|retrying → cancelled`
   - Invalid: any backward transition from `delivered`, `failed`, `cancelled`
7. `test(domain): add failing tests for Notification entity`
8. `feat(domain): implement Notification entity with content validation`
   - Recipient format validation per channel (E.164 for SMS, RFC 5322 for email, opaque token for push)
   - Content length limits per channel (160 for SMS, 10000 for email, 500 for push — matches the reference implementation)
9. `test(domain): add failing tests for Batch entity`
10. `feat(domain): implement Batch entity (1 to 1000 notifications)`
11. `test(domain): add failing tests for Template entity`
12. `feat(domain): implement Template entity with variable substitution`
13. `test(domain): add failing tests for NotificationLog entity`
14. `feat(domain): implement NotificationLog entity for trace endpoint`
15. `test(domain): add failing tests for DeliveryResult DTO`
16. `feat(domain): implement DeliveryResult DTO with retryable classification`
17. `test(domain): add failing tests for domain errors`
18. `feat(domain): implement sentinel errors and typed error variants`

### 2B — Ports

19. `feat(ports): define Repository, Queue, Provider, IdempotencyStore, RateLimiter, StatusBroadcaster, Clock interfaces`

### 2C — Application Layer (Use Cases)

20. `test(application): add failing test for CreateNotification use case`
21. `feat(application): implement CreateNotification use case`
22. `test(application): add failing test for CreateBatch use case`
23. `feat(application): implement CreateBatch use case`
24. `test(application): add failing test for CancelNotification use case`
25. `feat(application): implement CancelNotification use case`
26. `test(application): add failing test for ListNotifications use case`
27. `feat(application): implement ListNotifications use case with cursor pagination`
28. `test(application): add failing test for GetNotification use case`
29. `feat(application): implement GetNotification use case`
30. `test(application): add failing test for GetNotificationTrace use case`
31. `feat(application): implement GetNotificationTrace use case`
32. `test(application): add failing test for ProcessNotification use case (with atomic claim)`
33. `feat(application): implement ProcessNotification use case with atomic claim semantics`
34. `test(application): add failing test for ReconcileStuckNotifications use case`
35. `feat(application): implement ReconcileStuckNotifications use case`
    - Three reconciliation paths: orphaned `pending` (dual-write race), stuck `processing` (worker crash), overdue `retrying` (schedule loss). See ADR-0011 for the orphaned-pending rationale.
36. `test(application): add failing test for RenderTemplate use case`
37. `feat(application): implement RenderTemplate use case using text/template`
38. `test(application): add failing test for ScheduleNotification use case`
39. `feat(application): implement ScheduleNotification use case`

**Exit criteria:**

- All domain and application packages pass `go test ./internal/domain/... ./internal/application/...`.
- Coverage ≥ 90% for both packages (verified by `make coverage`).
- No imports of `database/sql`, `net/http`, `github.com/...` (except testify) in domain or application — verified by the `check-hexagonal-boundaries` skill.
- `make lint` clean.

**PR title:** `feat: phase 2 domain and application layer with full test coverage`

---

## Phase 3 — Adapters

**Goal:** Concrete implementations of every port, each tested in isolation. After this phase, the system can run end-to-end against real Postgres, Redis, and a mock provider.

**Entry:** Phase 2 merged.

**Tasks:**

### 3A — Database

1. `feat(migrations): add initial schema migrations`
   - Tables: `notifications`, `notification_logs`, `templates`, `batches`.
   - Indexes per CLAUDE.md §11.
2. `feat(adapter/postgres): add sqlc configuration and query files`
3. `test(adapter/postgres): add integration test scaffolding with testcontainers`
4. `test(adapter/postgres): add failing test for NotificationRepository.Create and Get`
5. `feat(adapter/postgres): implement NotificationRepository.Create and Get`
6. `test(adapter/postgres): add failing test for atomic ClaimForProcessing`
7. `feat(adapter/postgres): implement atomic ClaimForProcessing (UPDATE ... WHERE status IN (...) RETURNING)`
8. `test(adapter/postgres): add failing test for NotificationRepository.UpdateStatus`
9. `feat(adapter/postgres): implement NotificationRepository.UpdateStatus`
10. `test(adapter/postgres): add failing test for NotificationRepository.List with cursor pagination`
11. `feat(adapter/postgres): implement NotificationRepository.List with cursor pagination`
12. `test(adapter/postgres): add failing test for FindOrphanedPending, FindStuckProcessing, FindOverdueRetrying with SELECT FOR UPDATE SKIP LOCKED`
13. `feat(adapter/postgres): implement reconciler query methods with SELECT FOR UPDATE SKIP LOCKED`
    - All three queries use `SELECT ... FOR UPDATE SKIP LOCKED` to allow horizontal scaling of the reconciler binary without conflicting claims. See CLAUDE.md §3.11.
14. `feat(adapter/postgres): implement BatchRepository`
15. `feat(adapter/postgres): implement TemplateRepository`
16. `feat(adapter/postgres): implement NotificationLogRepository (for trace endpoint)`

### 3B — Redis Adapters

17. `test(adapter/redis): add failing test for IdempotencyStore`
18. `feat(adapter/redis): implement IdempotencyStore with TTL`
19. `test(adapter/redis): add failing test for OutboundRateLimiter (channel)`
20. `feat(adapter/redis): implement OutboundRateLimiter with atomic Lua script`
21. `test(adapter/redis): add failing test for StatusBroadcaster (pub/sub)`
22. `feat(adapter/redis): implement StatusBroadcaster for WebSocket fan-out`

### 3C — Queue (Asynq)

23. `feat(adapter/asynq): define task payload types`
24. `test(adapter/asynq): add failing test for Queue.Enqueue with priority`
25. `feat(adapter/asynq): implement Queue.Enqueue with priority and idempotency`
26. `feat(adapter/asynq): implement Queue.EnqueueScheduled for future delivery`
27. `feat(adapter/asynq): implement Queue.Cancel for pending tasks`
28. `feat(adapter/asynq): implement task processor that invokes ProcessNotification use case`

### 3D — Provider Strategy

29. `test(adapter/provider): add failing test for MockProvider`
30. `feat(adapter/provider): implement MockProvider with configurable failure rate`
31. `test(adapter/provider): add failing test for WebhookProvider against httptest server`
32. `feat(adapter/provider): implement WebhookProvider with timeout and structured error mapping`
33. `feat(adapter/provider): implement ProviderRegistry for channel routing`
34. `feat(infrastructure/circuit): wrap Provider in circuit breaker decorator`

### 3E — WebSocket

35. `test(adapter/websocket): add failing test for Hub subscribe/unsubscribe`
36. `feat(adapter/websocket): implement Hub with per-notification subscriptions`
37. `test(adapter/websocket): add failing test for Hub fan-out from Redis pub/sub`
38. `feat(adapter/websocket): implement Redis pub/sub consumer feeding Hub`

**Exit criteria:**

- All adapter tests pass (unit + integration via testcontainers).
- Coverage ≥ 70% for adapter packages.
- Migrations apply cleanly forward and backward.
- `make lint` clean.

**PR title:** `feat: phase 3 adapter implementations`

---

## Phase 4 — HTTP API & WebSocket

**Goal:** Spec-driven HTTP API with full middleware chain (correlation ID, inbound rate limiter, idempotency), request validation, RFC 7807 errors, OpenAPI documentation, Swagger UI, and WebSocket endpoint for real-time updates.

**Entry:** Phase 3 merged.

**Tasks:**

1. `feat(adapter/http): set up chi router with middleware chain`
   - Order: recover → correlation ID → request log → metrics → inbound rate limit → idempotency → handler
2. `feat(adapter/http): implement CorrelationIDMiddleware (read or generate ULID)`
3. `feat(adapter/http): implement InboundRateLimitMiddleware (60 req/min/IP)`
4. `feat(adapter/http): implement IdempotencyMiddleware (Redis-backed, 24h TTL)`
5. `feat(adapter/http): implement RFC 7807 error response translator`
6. `feat(adapter/http): generate server interface from openapi.yaml via oapi-codegen`
7. `test(adapter/http): add failing test for POST /api/v1/notifications`
8. `feat(adapter/http): implement POST /api/v1/notifications handler`
9. `test(adapter/http): add failing test for POST /api/v1/notifications/batch`
10. `feat(adapter/http): implement POST /api/v1/notifications/batch handler`
11. `test(adapter/http): add failing test for GET /api/v1/notifications/{id}`
12. `feat(adapter/http): implement GET /api/v1/notifications/{id} handler`
13. `test(adapter/http): add failing test for GET /api/v1/notifications with filters and pagination`
14. `feat(adapter/http): implement GET /api/v1/notifications handler`
15. `test(adapter/http): add failing test for PATCH /api/v1/notifications/{id}/cancel`
16. `feat(adapter/http): implement PATCH /api/v1/notifications/{id}/cancel handler`
17. `test(adapter/http): add failing test for GET /api/v1/notifications/{id}/trace`
18. `feat(adapter/http): implement GET /api/v1/notifications/{id}/trace handler`
19. `test(adapter/http): add failing test for GET /api/v1/notifications/batch/{id}`
20. `feat(adapter/http): implement GET /api/v1/notifications/batch/{id} handler`
21. `feat(adapter/http): implement template CRUD endpoints`
22. `feat(adapter/http): implement GET /api/v1/ws/notifications (WebSocket upgrade)`
23. `feat(adapter/http): implement GET /healthz/live and /healthz/ready`
24. `feat(adapter/http): implement GET /metrics (Prometheus)`
25. `feat(adapter/http): implement GET /api/v1/metrics (JSON-friendly subset)`
26. `feat(adapter/http): mount Swagger UI at /docs serving openapi.yaml`
27. `feat(cmd/api): wire all dependencies in main.go`
28. `feat(cmd/worker): wire worker with asynq processor in main.go`
29. `feat(cmd/reconciler): wire reconciler with 1-minute tick in main.go`
30. `feat(cmd/migrate): implement migration runner CLI`
31. `docs: generate Bruno API collection from openapi.yaml`
    - `docs/bruno/` directory with one `.bru` file per endpoint
    - Includes example payloads, environment file for `BASE_URL`
    - Bruno chosen over Postman: open-source, git-friendly, plain-text format (`.bru`), no cloud account required
    - Reviewer can `bruno docs/bruno` or use the Bruno desktop app
    - README references the collection in the API section

**Exit criteria:**

- All endpoints implemented and unit-tested.
- WebSocket subscribe/unsubscribe verified end-to-end.
- OpenAPI spec is complete and matches code (validated by `oapi-codegen`).
- `curl` examples in README work end-to-end against `docker compose up`.
- Swagger UI accessible at `http://localhost:8080/docs`.
- Bruno collection executes successfully against `http://localhost:8080`.

**PR title:** `feat: phase 4 http api, websocket, and binary wiring`

---

## Phase 5 — Integration, E2E & Load Tests

**Goal:** Confidence that the system works as a whole — both functionally (e2e behaviour) and at the targeted scale (load characteristics).

**Entry:** Phase 4 merged.

**Tasks:**

1. `test(e2e): scaffold e2e test harness with testcontainers (postgres + redis + asynq)`
2. `test(e2e): notification lifecycle — create, process, observe delivered status`
3. `test(e2e): notification with simulated provider failure — verify retry and DLQ`
4. `test(e2e): batch creation — verify all enqueued and processed`
5. `test(e2e): idempotency — duplicate Idempotency-Key returns cached response`
6. `test(e2e): inbound rate limit — verify 60 req/min/IP cap returns 429`
7. `test(e2e): outbound rate limit — verify channel rate cap is enforced under load`
8. `test(e2e): cancellation — verify pending/queued/retrying cancellable, delivered/failed not`
9. `test(e2e): scheduled notification — verify processing is delayed until scheduled_at`
10. `test(e2e): template — verify variable substitution renders correctly`
11. `test(e2e): cursor pagination — verify next/prev navigation`
12. `test(e2e): correlation ID — verify propagation through API → queue → worker → logs`
13. `test(e2e): trace endpoint — verify all status transitions appear in chronological order`
14. `test(e2e): atomic claim — simulate concurrent worker race, verify exactly one wins`
15. `test(e2e): reconciler — simulate stuck processing, verify reconciler marks failed`
16. `test(e2e): reconciler — simulate orphaned pending (dual-write race), verify reconciler re-enqueues`
17. `test(e2e): reconciler concurrency — run two reconciler instances against same data, verify SKIP LOCKED prevents double-claim`
18. `test(e2e): websocket — connect, subscribe, observe status updates fanned out`
19. `chore(test): add coverage report aggregation across unit and integration suites`

### 5B — Load & Capacity Validation

20. `chore(loadtest): scaffold k6 load test directory with shared helpers`
    - `tests/load/` directory; uses `k6` (no Go runtime needed; Docker-based runner)
    - `docker-compose.loadtest.yml` adds a `k6` service that targets the api service
21. `feat(loadtest): add baseline throughput scenario`
    - Sustained 300 req/sec for 60s (≈ outbound rate limit × 3 channels)
    - Verifies system processes incoming load without DLQ growth
    - Asserts p95 API accept latency < 200ms
22. `feat(loadtest): add burst traffic scenario (flash sale simulation)`
    - 1000 req/sec for 10s, then idle 50s
    - Verifies queue absorbs burst; all notifications eventually delivered
    - Asserts zero DLQ growth, queue drains within 60s of burst end
23. `feat(loadtest): add rate limit trigger scenario`
    - 200 req/sec to single channel (above 100/sec outbound limit)
    - Verifies outbound rate limiter releases tasks back to queue without failures
    - Asserts no notifications enter DLQ due to rate limiting
24. `docs(loadtest): write LOAD_TEST.md with results, methodology, environment notes`
    - Methodology: tools used, hardware notes, repeatability instructions
    - Results: throughput tables, latency percentiles, queue depth graphs (k6 markdown summary)
    - Limitations: single-machine setup, mock provider with 0 latency, real-provider numbers will differ

**Exit criteria:**

- All e2e tests pass against `docker compose up`.
- Total coverage report shows ≥ 80% project-wide.
- Test suite (`make test-all`) runs in under 5 minutes.
- All three k6 load scenarios run cleanly (`make load-test`).
- `docs/LOAD_TEST.md` documents results, methodology, and limitations.

**PR title:** `test: phase 5 integration, end-to-end, and load testing`

---

## Phase 6 — Observability & Alerting Stack

**Goal:** Every operationally significant event is logged, measured, traceable, and — if it matters — alertable. Operators have dashboards, alerts, and runbooks. This phase realises §2.1 and §3.8 of CLAUDE.md.

**Entry:** Phase 5 merged.

**Tasks:**

### 6A — Application Instrumentation

1. `feat(infrastructure/logger): configure slog with JSON output and correlation context`
2. `feat(infrastructure/metrics): define and register Prometheus collectors per §12.1`
3. `feat(infrastructure/tracing): wire OpenTelemetry SDK with no-op exporter and HTTP/queue instrumentation`
4. `feat(infrastructure/health): implement liveness (process up) and readiness (db+redis reachable) checks`
5. `feat(adapter/http): instrument all handlers with metrics middleware`
6. `feat(adapter/asynq): instrument task processor with metrics`
7. `feat(adapter/websocket): instrument Hub with active client gauge`

### 6B — Prometheus Configuration

8. `feat(deploy/prometheus): add prometheus.yml with scrape configs for api, worker, reconciler`
9. `feat(deploy/prometheus): add alerts.yml with rules per §12.5`
   - Use `add-alert-rule` skill. Required alerts (minimum):
     - `HighQueueDepth` — queue depth > 10000 for 5m → critical
     - `WorkerProcessingStalled` — zero throughput for 5m → critical
     - `ReconcilerNotRunning` — no reconciler activity for 5m → critical
     - `HighFailureRate` — failure rate > 50% over 5m → critical
     - `DLQGrowthRate` — DLQ growing > 100/min → critical
     - `DatabaseConnectionPoolExhausted` — pool used > 90% for 5m → critical
     - `RedisUnreachable` — Redis up == 0 for 1m → critical
     - `HighProcessingLatency` — p99 > 30s for 10m → warning
     - `CircuitBreakerOpen` — breaker state == open for 5m → warning
     - `InboundRateLimitFrequentlyHit` — limit hits > 100/min for 10m → warning
     - `OutboundRateLimitFrequentlyHit` — limit hits > 10/min for 10m → warning
     - `StuckProcessingDetected` — reconciler marked > 10 notifications as worker_timeout in 5m → warning

### 6C — AlertManager Configuration

10. `feat(deploy/alertmanager): add alertmanager.yml with log-receiver routes`
    - Critical alerts route to `webhook-critical` (local log receiver in dev).
    - Warning alerts route to `webhook-warning`.
    - Production swap-in points documented in comments (Slack, PagerDuty, email).

### 6D — Grafana Dashboards

11. `feat(deploy/grafana): add datasource provisioning for Prometheus`
12. `feat(deploy/grafana): add dashboard — Notification System Overview (business)`
    - Panels: total notifications today, delivery success rate per channel, queue depth (live), throughput timeseries, p50/p95/p99 latency, circuit breaker states, DLQ size, active WebSocket clients.
13. `feat(deploy/grafana): add dashboard — HTTP API Performance`
    - Panels: requests per second per endpoint, error rate per endpoint, latency histogram, 4xx vs 5xx breakdown, inbound rate limit hits.
14. `feat(deploy/grafana): add dashboard — Worker & Queue Health`
    - Panels: workers active, tasks dequeued per minute, retry counts, DLQ inspection, provider call latency per channel, atomic claim success rate, reconciler activity.
15. `feat(deploy/grafana): add dashboard provisioning manifest`

### 6E — Runbook

16. `docs(runbook): add RUNBOOK.md with entry per alert rule`
    - Each entry includes: alert name, what it means, what to check, common causes, remediation steps, escalation criteria, related dashboards.

**Exit criteria:**

- `/metrics` endpoint returns valid Prometheus output covering all defined collectors.
- Every log line contains: `time`, `level`, `msg`, `correlation_id`, `service`.
- Health probes return correct status under DB/Redis outages.
- Prometheus successfully scrapes api, worker, reconciler.
- AlertManager receives test alerts and routes them to log receivers.
- All three Grafana dashboards load and show data after `docker compose up` plus a few minutes of generated traffic.
- Every alert in `alerts.yml` has a corresponding section in `RUNBOOK.md`.

**PR title:** `feat: phase 6 full observability and alerting stack`

---

## Phase 7 — CI/CD, Diagrams & Polish

**Goal:** A repository that is genuinely ready for a reviewer to clone, run, and judge. Architecture is visualised. Documentation is complete.

**Entry:** Phase 6 merged.

**Tasks:**

### 7A — CI Pipeline Expansion

1. `ci: extend ci workflow with integration test job`
   - New job `test-integration` runs `go test -tags=integration ./tests/integration/...`
   - Uses `services:` block to spin up Postgres and Redis containers in the runner
   - Caches Go modules; parallelisable with the unit test job
2. `ci: add e2e test job to ci workflow`
   - New job `test-e2e` runs `go test -tags=e2e ./tests/e2e/...`
   - Uses `testcontainers-go` (no need for `services:` block — testcontainers manages Docker)
   - Sequential after build; depends on `build` job
3. `ci: add coverage aggregation and SonarCloud upload`
   - New job `sonarcloud` depends on all test jobs
   - Aggregates `.coverprofile` outputs from unit, integration, e2e
   - Converts to SonarCloud format via `sonar-scanner`
   - Uses `sonarsource/sonarcloud-github-action@master`
   - Requires `SONAR_TOKEN` secret (documented in CONTRIBUTING.md)
4. `ci: add govulncheck step to ci workflow`
   - New step in the lint job (or separate job) running `govulncheck ./...`
   - Fails the build if any known vulnerability is detected
   - This is the Go equivalent of `composer audit` — supply chain awareness signal
5. `ci: add docker build smoke test job`
   - New job `docker-build` runs `docker build .` to verify the Dockerfile builds cleanly
   - Catches Dockerfile regressions that local `make build` cannot
   - Does NOT push anywhere — purely a smoke test
6. `chore: add sonar-project.properties with quality gate config`
   - Project key, organization, sources/tests paths, exclusions
   - Coverage report paths from Go's `.coverprofile` format
   - Quality gate set to "Sonar way" with project-specific overrides documented inline
7. `chore: add .github/PULL_REQUEST_TEMPLATE.md`
   - Sections: summary, changes, testing, related ADR/PLAN.md phase, checklist
8. `chore: add .github/CODEOWNERS`
   - Single owner (Bora) for all paths; documents review responsibility
9. `docs: add CONTRIBUTING.md with branch protection setup and PR workflow`
   - Describes branch protection rules required on `main`:
     - Require PR before merging
     - Require status checks: lint, test-unit, test-integration, test-e2e, sonarcloud
     - Require linear history
     - Require conversation resolution
   - Notes that these must be configured manually in repo Settings (GitHub does not provide them as code)
10. `docs: add CI status, SonarCloud Quality Gate, Coverage, Go Report Card, Go version, and License badges to README`
    - Six badges at the top of the README, immediately above the hero diagram
11. `chore: register the project with Go Report Card`
    - Visit https://goreportcard.com/report/github.com/<user>/notification-system
    - First visit triggers initial scan; subsequent badge fetches show the score
    - Aim for A+ grade (golangci-lint clean = high score automatically)

### 7B — Architecture Diagrams

12. `docs(images): add hero architecture diagram (SVG, multi-coloured)`
   - The hero diagram is exported from a vector source and committed as `docs/images/architecture-hero.svg`.
   - Renders inline in GitHub README at the top, above the fold.
13. `docs: add request lifecycle mermaid diagram in README`
   - Sequence diagram showing POST → API → DB → Queue → Worker (atomic claim) → Provider → DB update → Redis pub/sub → WebSocket fan-out.
14. `docs: add failure handling flow mermaid diagram in README`
   - Decision flow: provider response → classify (2xx/4xx/5xx/timeout) → retry path or terminal state, with circuit breaker and reconciler shown.
15. `docs: add hexagonal layering mermaid diagram in README`
   - Concentric layer view: domain ← application ← ports ← adapters.

### 7C — Documentation

16. `docs: write comprehensive README`
   - Sections (in order): 6 status badges, hero diagram, what is this, quickstart (`docker compose up`), services & tools table (all operational UIs with URLs and purposes), architecture (mermaid diagrams), tech stack, **all API endpoints with curl examples** + link to Bruno collection, observability (link to dashboards + alerts + trace endpoint), **capacity & performance** (theoretical limits + link to LOAD_TEST.md results), design decisions, scaling considerations, troubleshooting, future work.
17. `docs: write API_EXAMPLES.md with curl invocations for every endpoint`
18. `docs: review and finalise all ADRs`

### 7D — Final Touches

19. `chore: review commit history for consistency`
20. `chore: tag v1.0.0 release`

**Exit criteria:**

- A reviewer who has never seen the project can `git clone && docker compose up` and reach a working API within 5 minutes (first-time Docker image build).
- README hero diagram is visible at the top, all six badges render, all mermaid diagrams render in GitHub.
- README answers every question a reviewer is likely to have.
- CI is green on `main`. SonarCloud quality gate passes. Go Report Card grade is A or A+.
- govulncheck reports zero known vulnerabilities.
- Commit history reads like a thoughtful, TDD-driven project narrative.

**PR title:** `ci: phase 7 cicd, diagrams, and final polish`

---

## What Comes After

After phase 7, the assessment is submission-ready. Possible follow-ups, **not** in scope:

- Distributed tracing with Jaeger or Tempo container (currently no-op exporter; SDK already wired).
- Custom business dashboard (Next.js or similar) for non-technical internal users — currently served via Grafana.
- WebSocket private channels with authentication for production multi-tenant use.
- Provider-specific implementations (Twilio for SMS, SendGrid for email, FCM for push) replacing the webhook abstraction.
- Webhook delivery for API consumers to receive status callbacks.
- AlertManager production routing (Slack, PagerDuty, email) — currently routes to log receivers in dev.
- Database partitioning of `notifications` by month for >100M row deployments.
- CQRS read model in Elasticsearch for dashboard filter performance at scale.

These are listed in the README under "Future Work" with brief notes on how the architecture supports them, demonstrating extensibility without implementing them.

---

## Notes for the Reviewer

If you are reading this as a reviewer: thank you for taking the time. The order and granularity above reflect how this project was actually built — not a retrofitted narrative. Each commit can be inspected in isolation; each phase corresponds to a PR you can review at the level of detail you find useful.

The brief mentioned 12 hours as the expected duration. This project intentionally exceeded that to deliver the quality bar the brief also asked for. Every additional hour went into tests, documentation, observability, and the alerting stack — not into shipping more features. The bonus item "Failure Handling" is interpreted (in CLAUDE.md §2.1 and ADR-0007) to include operational alerting, which justifies the AlertManager + Grafana addition. The bonus item "WebSocket Updates" is implemented literally (CLAUDE.md §2.5 and ADR-0006). The brief's stated need for "millions of notifications daily" and "burst traffic (flash sales, breaking news)" is not just claimed but validated with k6 load tests; see `docs/LOAD_TEST.md` for methodology and results.

There is no custom frontend (ADR-0005). The brief's "frontend skills" criterion is addressed through a thoughtful operational UI stack (Swagger UI, asynqmon, Grafana, Adminer, Redis Commander) — each tool suited to its audience and purpose. Building a bespoke dashboard would duplicate functionality and detract from backend quality, which is the primary skill being assessed for a Senior Golang position.
