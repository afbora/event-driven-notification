<!--
Thanks for opening a PR! Fill out every section below — even a
one-line answer is better than a blank line. The reviewer reads top
to bottom; lead with what matters.
-->

## Summary

<!-- 1-3 bullet points. What does this PR change, and why now? -->

-
-

## Changes

<!-- Concrete list of touched files / new files / removed files,
     grouped by area. Use headings if the change spans multiple
     layers (domain / application / adapter / infrastructure). -->

-

## Testing

<!-- Tick what you actually ran. Add new boxes if a particular
     scenario was exercised manually. -->

- [ ] `make lint` — 0 issues
- [ ] `make test` — unit suite green
- [ ] `make test-integration` — integration suite green (adapter tests against pg/redis)
- [ ] `make test-e2e` — full e2e suite green
- [ ] `docker compose up -d` then a `curl` against the affected endpoint
- [ ] Coverage on touched packages did not decrease

## Related

<!-- Cross-reference the work to PLAN.md and any ADRs. -->

- PLAN.md phase: <!-- e.g. "Phase 4 task 12" -->
- ADR(s):       <!-- e.g. "docs/adr/0009-atomic-claim.md" or "none" -->
- Issue:        <!-- "#nn" or "none" -->

## Checklist

- [ ] Commit history follows Conventional Commits (`feat(scope): subject`, lowercase, imperative)
- [ ] Every `feat` commit is preceded by a matching `test` commit (TDD discipline)
- [ ] OpenAPI spec updated if the HTTP contract changed (`api/openapi.yaml`)
- [ ] RUNBOOK entry added if a new alert rule was introduced (`docs/RUNBOOK.md`)
- [ ] ADR written if a non-trivial architectural decision was made (`docs/adr/`)
- [ ] No `.env` file added — all env vars belong in `docker-compose.yml` (CLAUDE.md §2.7)
