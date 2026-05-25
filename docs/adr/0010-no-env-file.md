# ADR-0010: No `.env` File; All Configuration Inline In docker-compose.yml

**Status:** Accepted
**Date:** 2026-05-23
**Deciders:** Ahmet Bora

## Context

The conventional pattern for a Docker-Compose project is:

- A `.env.example` checked into the repository, documenting expected variables.
- A local `.env` (in `.gitignore`) where each developer copies and tweaks values.
- `docker-compose.yml` references variables as `${VAR}` and reads them from the local `.env`.

This pattern is widely understood but has a hidden cost for an assessment-style project:

- The "quickstart" becomes `cp .env.example .env && docker compose up`. Two steps, not one. Reviewers who skim the README sometimes miss the copy step and get a confusing failure.
- `.env.example` and the actual variables in `docker-compose.yml` can drift out of sync, and the drift is silent.
- Sensitive values that "should" be set differently in dev vs prod end up tracked in two places (the example and the compose file), inviting accidents.

The brief's explicit requirement:

> **Docker Compose:** One-command setup (`docker-compose up`)

"One-command" is hard to honor if the prerequisite is "copy this file first."

## Decision

This project has **no `.env` file and no `.env.example`**. Every environment variable required by every service is set inline in `docker-compose.yml`, with a working default that allows the entire stack to start with a single command:

```bash
docker compose up -d
```

Concretely:

- `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB` are hardcoded as `notification` / `notification` / `notification` for local development.
- `DATABASE_URL` is constructed inline in the `api`, `worker`, and `reconciler` services using the same credentials.
- `REDIS_URL`, `HTTP_PORT`, `LOG_LEVEL`, `WORKER_CONCURRENCY`, `RECONCILER_INTERVAL`, `OTEL_EXPORTER_OTLP_ENDPOINT` (left empty for no-op exporter) all have inline defaults.
- Grafana's admin credentials are `admin` / `admin` — explicitly weak so no one ever mistakes them for production values.

Each environment variable carries a one-line comment in `docker-compose.yml` explaining what it does. Where a production override would differ materially (e.g., the OpenTelemetry endpoint), the comment notes where the production value would come from (Kubernetes ConfigMap, secrets manager).

## Consequences

**Positive:**

- The quickstart is genuinely one command. A reviewer can clone the repo and have a working API in under 60 seconds (first-time Docker image build aside).
- No chicken-and-egg: no `.env` file means no risk of one drifting or being committed accidentally.
- The compose file is the single source of truth for development configuration.
- It is impossible to "forget" to set a variable; the compose file fails fast if a service expects something undeclared.

**Negative:**

- Cannot store secrets in the compose file safely. We accept this for dev (credentials are deliberately weak and well-known). For production we rely on the orchestration layer (Kubernetes Secret resources, AWS Secrets Manager, HashiCorp Vault). This is documented as a future-work note in the README.
- Developers who want to override a value locally must either edit the compose file (and avoid committing) or use a `docker-compose.override.yml` (Compose's built-in mechanism). Both are well-supported by Docker Compose; neither is as ergonomic as a `.env` edit.
- Slightly unusual pattern for reviewers who have seen `.env.example` in every project they have ever worked on. We mitigate by being explicit about the choice — README mentions it, this ADR explains the rationale.

**Defensive measure:**

`.gitignore` includes `.env` and `.env.*` patterns so that even if a future contributor mistakenly creates a `.env` file, it does not get committed.

## Alternatives Considered

1. **`.env.example` checked in + `.env` local** — rejected for the reasons above. Workable but adds friction.
2. **Hardcode values in Go (`const DefaultDatabaseURL = ...`)** — rejected. Violates 12-Factor App's "store config in the environment" principle. Makes production deployment harder (you can't swap an env var; you must rebuild).
3. **Configuration file in YAML (`config.yaml` loaded by viper)** — possible. Adds a file format and a parsing step. We already use environment variables everywhere; adding YAML on top is unnecessary indirection.
4. **Pulumi / Terraform / similar for "real" environments** — out of scope for this assessment. The README's "production" notes acknowledge this is where real configuration would live in a deployed system.

## Related

- CLAUDE.md §2.7 (No `.env` File), §3.7 (Configuration Is External)
- BOOTSTRAP.md ("Reminder: No `.env` File" section)
- README.md quickstart
- `docker-compose.yml` (the entire file is the artifact of this decision)
