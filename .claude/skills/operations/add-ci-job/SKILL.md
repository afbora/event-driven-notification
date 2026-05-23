# Skill: add-ci-job

## Purpose

Add a job, step, or new workflow to GitHub Actions following this project's CI conventions (caching, fail-fast behaviour, artifact handling, secret management).

## When To Use

- You are adding a new check (lint, test, security scan, build verification) to the CI pipeline.
- You are creating an entirely new workflow file (`.github/workflows/*.yml`).
- You are modifying an existing job to add a step or change its trigger conditions.

## Prerequisites

- The check you are adding is meaningful (not a duplicate of existing coverage).
- You know whether it should block PR merge (status check) or be advisory only.
- If the check requires a secret (e.g., `SONAR_TOKEN`), you understand where to document it.

## Steps

### 1. Decide: new workflow or modify existing?

Use a **new workflow file** when:

- The trigger is fundamentally different (e.g., scheduled cron vs PR push).
- The concern is separate from CI (e.g., release publishing, security scanning).
- The set of jobs is large and would clutter `ci.yml`.

**Modify an existing workflow** when:

- The check belongs to the same lifecycle as existing jobs (lint, test, build).
- It shares triggers and caching with sibling jobs.

### 2. Workflow file structure

Every workflow file follows this template:

```yaml
name: <Workflow Name>

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

permissions:
  contents: read
  # Add others only if needed; default to least privilege

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  <job-id>:
    name: <Human-Readable Job Name>
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: 'go.mod'
          cache: true

      - name: <Step name>
        run: |
          <commands>
```

### 3. Caching convention

For Go projects:

```yaml
- uses: actions/setup-go@v5
  with:
    go-version-file: 'go.mod'
    cache: true              # Caches both module cache and build cache
```

`setup-go@v5` handles caching automatically via the cache key derived from `go.sum`. Do not invent custom cache keys unless the default fails.

For non-Go caching (e.g., `docker/build-push-action` layer cache), use:

```yaml
cache-from: type=gha
cache-to: type=gha,mode=max
```

### 4. Fail-fast vs continue-on-error

By default, a step's failure fails the job. Override only with good reason:

```yaml
- name: Optional check
  continue-on-error: true   # Job continues, but PR shows yellow X
  run: <command>
```

Use `continue-on-error` for:

- New, experimental checks where flakes are expected.
- Advisory-only checks (informational, not blocking).

Do **not** use it to hide failing checks. If a check is failing legitimately, fix the root cause or remove the check.

### 5. Adding a new job to existing workflow

Append to the `jobs:` section. Mind dependencies:

```yaml
jobs:
  lint: ...

  test-unit:
    needs: [lint]            # Sequential
    ...

  test-integration:
    needs: [lint]            # Parallel with test-unit
    ...

  sonarcloud:
    needs: [test-unit, test-integration]  # Aggregates results
    ...
```

Default to **parallel** when jobs are independent. Add `needs:` only when an earlier job's artifact or output is required.

### 6. Secrets handling

Secrets are referenced as `${{ secrets.NAME }}`. Three rules:

1. **Never log a secret.** GitHub auto-masks but do not test the masking by `echo`-ing.
2. **Document every secret in `CONTRIBUTING.md`** — name, purpose, where to obtain it.
3. **Use least-privilege scopes** for fine-grained PATs. The default `GITHUB_TOKEN` is preferred when sufficient.

Standard secrets in this project:

| Secret | Purpose | Where to obtain |
|---|---|---|
| `GITHUB_TOKEN` | Default token, auto-provisioned | Built-in |
| `SONAR_TOKEN` | SonarCloud authentication | SonarCloud project settings |

### 7. Triggering on the right events

Common patterns:

```yaml
# CI workflow — every PR and main push
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

# Scheduled scan — weekly Sunday at 03:00 UTC
on:
  schedule:
    - cron: '0 3 * * 0'
  push:
    branches: [main]   # Also run on main push
  pull_request:
    branches: [main]

# PR title check — only on PR open/edit
on:
  pull_request:
    types: [opened, edited, synchronize]
```

### 8. Adding a security/audit step

Recommended security steps (one or more per project):

```yaml
- name: govulncheck
  run: |
    go install golang.org/x/vuln/cmd/govulncheck@latest
    govulncheck ./...

- name: golangci-lint with gosec
  uses: golangci/golangci-lint-action@v6
  with:
    version: latest
    args: --timeout=5m

# CodeQL runs in its own workflow file
```

### 9. Upload artifacts for diagnosability

When a job produces output a human might want to inspect (test reports, coverage profiles, build logs):

```yaml
- name: Upload coverage
  if: always()              # Even if previous steps failed
  uses: actions/upload-artifact@v4
  with:
    name: coverage-${{ github.job }}
    path: coverage.out
    retention-days: 7
```

Use `if: always()` for diagnostic artifacts so they upload even on failure.

### 10. Verify locally before pushing

Many GitHub Actions steps map to local commands:

- `golangci-lint run` — same as the CI lint step
- `go test ./...` — same as unit tests
- `go test -tags=integration ./tests/integration/...` — same as integration tests
- `govulncheck ./...` — same as the vulnerability scan

Run all these locally before pushing. CI is the safety net, not the primary feedback loop.

### 11. Update CONTRIBUTING.md if you added a new secret or check

Branch protection rules reference required status checks by name. If you added a new job that should block merge, update CONTRIBUTING.md so future maintainers know to add it to branch protection.

### 12. Commit

```
ci: add <check name> step to lint job
ci: add new workflow for nightly security scan
docs: document SONAR_TOKEN secret in CONTRIBUTING.md
```

One commit per logical unit. A new workflow file is one commit; adding it to branch protection documentation is another.

## Verification

- [ ] Workflow file passes YAML lint (`yamllint .github/workflows/*.yml`).
- [ ] Push to a feature branch and verify the workflow runs as expected.
- [ ] Run any new check locally to confirm it would pass against current code.
- [ ] If the check is blocking, update CONTRIBUTING.md branch protection list.
- [ ] If a new secret was introduced, document it in CONTRIBUTING.md with acquisition instructions.

## Common Mistakes

- Using `${{ secrets.NAME }}` in `run:` blocks that get logged. Use environment variables instead, and let GitHub auto-mask them.
- Forgetting `cache: true` on `setup-go@v5`, leading to 90+ second download times per run.
- Hard-coding Go version instead of using `go-version-file: 'go.mod'`. Drifts when go.mod is upgraded.
- Setting `continue-on-error: true` to hide a real failure. Fix the underlying check or remove it.
- Adding a job that is fast but on the critical path of a job that needs parallelism — use `needs:` carefully.
- Triggering scheduled scans without also triggering on push, so a broken `main` is not detected for a week.
- Forgetting `concurrency:` block, leading to multiple workflows racing on the same PR.
- Adding a workflow that consumes secrets but never documenting them. New maintainers cannot reproduce CI locally.
- Using third-party actions without pinning to a specific version. `@master` or `@latest` is a supply chain risk. Prefer `@v5` (tagged) or `@SHA` (immutable).
