# Event-Driven Notification System

A scalable notification system that ingests requests via HTTP, persists them, dispatches them
asynchronously through SMS / Email / Push channels with intelligent retry, and exposes real-time
status updates via WebSocket.

Built as a Senior Software Engineer (Golang) technical assessment submission for **Insider One**.
Full brief: [`docs/brief.pdf`](docs/brief.pdf).

> **Status:** Phase 1 of 7 — foundation, skeleton, and skills. The HTTP API, queue workers, and
> observability instrumentation land in later phases per [`PLAN.md`](PLAN.md). The repository as
> it stands compiles, lints, and brings up an empty-but-wired service stack.

## Quickstart

```bash
docker compose up -d
```

No `.env` file is required — every environment variable lives inline in `docker-compose.yml` with
a working default. The reasoning is documented in CLAUDE.md §2.7.

## Service endpoints (after `docker compose up`)

| Service          | URL                            | Notes                                                  |
|------------------|--------------------------------|--------------------------------------------------------|
| API              | http://localhost:8080          | HTTP + WebSocket endpoints (wired in phase 4)          |
| asynqmon         | http://localhost:8081          | Queue inspection, dead-letter management               |
| Adminer          | http://localhost:8082          | Postgres GUI — system: PostgreSQL · server: `postgres` |
| Redis Commander  | http://localhost:8083          | Redis GUI                                              |
| Prometheus       | http://localhost:9090          | Metrics scraper                                        |
| AlertManager     | http://localhost:9093          | Alert routing                                          |
| Grafana          | http://localhost:3001          | Dashboards (`admin` / `admin`)                         |

Postgres listens on `5432`, Redis on `6379`. Credentials for both: `notification` / `notification`.

## Project layout

```
cmd/                Three runtime binaries (api, worker, reconciler) plus a migrate CLI.
internal/
  domain/           Pure domain — entities, value objects, invariants.
  application/      Use cases (CreateNotification, ProcessNotification, ...).
  ports/            Interfaces (Repository, Queue, Provider, ...).
  adapters/         Concrete implementations (postgres, redis, asynq, http, ...).
  infrastructure/   Cross-cutting (config, logger, metrics, tracing, circuit breaker).
api/                OpenAPI spec — source of truth for the HTTP contract.
deploy/             Prometheus / Alertmanager config (Grafana provisioning in phase 6).
docs/               Brief, ADRs, runbook, load-test results.
tests/              Integration and e2e suites (phase 5).
.claude/skills/     15 skills encoding this project's recurring rituals.
```

## Development

```bash
make help          # List every Makefile target with description
make build         # Compile all four cmd/* binaries into ./bin
make test          # Run unit tests
make lint          # Run golangci-lint
make tools         # Install Go tooling (lint, sqlc, migrate, oapi-codegen, air)
```

See the [`Makefile`](Makefile) for the full target inventory.

## Documentation

| Document                           | Purpose                                                              |
|------------------------------------|----------------------------------------------------------------------|
| [`CLAUDE.md`](CLAUDE.md)           | Project operating manual — architecture, conventions, anti-patterns. |
| [`PLAN.md`](PLAN.md)               | Phase-by-phase roadmap (7 phases, ~22 hours).                        |
| [`docs/brief.pdf`](docs/brief.pdf) | Original Insider One assessment brief.                               |

The comprehensive README — with architecture diagrams, API examples, capacity analysis, the full
stack rationale, and status badges — arrives in phase 7. This document is the initial scaffold.

## License

[MIT](LICENSE)
