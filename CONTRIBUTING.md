# Contributing

> Short, practical guide for working on this repo. Long-form
> architectural reasoning lives in [`CLAUDE.md`](./CLAUDE.md) and
> [`docs/adr/`](./docs/adr/).

## Local setup

```bash
git clone https://github.com/afbora/event-driven-notification.git
cd event-driven-notification

# Bring the full operational stack up. No `.env` needed — every
# env var is inlined in docker-compose.yml (CLAUDE.md §2.7).
docker compose up -d
make migrate-up

# Smoke test
curl -i http://localhost:8080/healthz/live
```

## Dev tools

```bash
make tools   # installs lint, sqlc, migrate, oapi-codegen, air, gocovmerge
```

## TDD rhythm

Every `feat` commit is preceded by a matching `test` commit. The git
log of any PR makes the rhythm visible:

```
test(scope): add failing test for X
feat(scope): implement X
```

Use `t.Run(tc.name, ...)` subtests with table-driven cases when a
function has more than one branch.

## Commit messages

Conventional Commits — imperative, lowercase, no trailing period.
Types in use: `feat`, `fix`, `test`, `refactor`, `chore`, `docs`,
`perf`, `ci`, `build`.

Examples:

```
test(domain): add failing test for notification status transitions
feat(domain): implement notification status state machine
docs(adr): add ADR-0007 on circuit breaker thresholds
ci: add SonarCloud upload step to backend workflow
```

## Branches & PRs

- `main` is always green and deployable.
- Feature branches: `feat/<short-description>`, `fix/<short-description>`, etc.
- One PR per logical change. Merge commits (NOT squash) so the TDD
  rhythm stays visible in `main`'s history.
- The repo's PR template auto-loads — fill out every section.

## Pre-commit checks (locally)

Before pushing:

```bash
go build ./...
go test ./...
golangci-lint run ./...
```

Or `make build test lint` if you prefer the targets.

## Branch protection (must be configured in repo Settings)

GitHub does not expose branch protection rules as code. Set these
manually under **Settings → Branches → main**:

- **Require a pull request before merging** — at least 1 approval.
- **Require status checks to pass before merging**:
  - `Lint`
  - `govulncheck`
  - `Unit tests (race + coverage)`
  - `Integration tests (testcontainers)`
  - `End-to-end tests (testcontainers full stack)`
  - `Build all binaries`
  - `Docker image build (smoke test)`
  - `SonarCloud upload` (and the SonarCloud quality-gate check it
    reports)
- **Require linear history** — protects the merge-commit pattern.
- **Require conversation resolution before merging**.
- **Do not allow bypassing the above settings** for the project lead
  (no exceptions while the assessment is being reviewed).

## SonarCloud setup

The CI workflow uploads coverage to SonarCloud if `SONAR_TOKEN` is
set as a repository secret:

1. Sign in at https://sonarcloud.io with your GitHub account.
2. Add the repo (`afbora/event-driven-notification`); accept the
   "GitHub" provisioning.
3. Generate a project token, copy it.
4. In GitHub: **Settings → Secrets and variables → Actions → New
   repository secret**, name `SONAR_TOKEN`, paste the token.

Once the secret is in place, every push to `main` and every PR
re-runs the `sonarcloud` job; the badge in [`README.md`](./README.md)
shows the live quality gate.

## Adding a new alert / metric / endpoint

- **Alert** — declare the rule in `deploy/prometheus/alerts.yml`
  AND add the matching section in [`docs/RUNBOOK.md`](./docs/RUNBOOK.md).
  The anchor must match the alert name's lowercase slug.
- **Metric** — declare in `internal/infrastructure/metrics/metrics.go`,
  add a verb method, register on `New()`. The exposition format is
  asserted in `metrics_test.go`.
- **HTTP endpoint** — edit `api/openapi.yaml` first, then
  `make openapi` regenerates the server interface. Implement the
  handler in `internal/adapters/http/` and add a test alongside.

## Skills

`.claude/skills/{core,quality,operations}/` encodes the project's
TDD + commit + ADR rituals. When working on a recurring kind of
task (new endpoint, new alert, new ADR, refactor without breaking),
check whether a skill already covers it — they exist precisely to
keep the codebase coherent.
