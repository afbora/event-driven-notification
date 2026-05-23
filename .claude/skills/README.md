# .claude/skills/ — Project-Specific Skill Library

This directory contains 15 skill files that encode the recurring rituals of this project. Each skill is a recipe Claude Code should follow when its current task matches the skill's purpose.

## Why Skills Exist

Even with a thorough `CLAUDE.md`, day-to-day tasks have specific steps that are easy to forget under time pressure: regenerate the OpenAPI server interface, add a metric label, write the ADR, set up the atomic claim correctly. Skills make those steps explicit and reusable.

Skills are **also documentation for humans.** A reviewer reading `add-use-case/SKILL.md` learns how use cases are added in this codebase without reading any Go.

## Categories

### `core/` — Day-to-day development (4 skills)

| Skill | When to use |
|---|---|
| `add-use-case` | Creating a new application use case |
| `add-endpoint` | Adding a new HTTP endpoint |
| `add-migration` | Changing the database schema |
| `add-provider` | Adding a new notification channel or provider implementation |

### `quality/` — Testing and code health (7 skills)

| Skill | When to use |
|---|---|
| `add-integration-test` | Writing a test that needs real Postgres or Redis |
| `add-e2e-test` | Writing a test that exercises the full stack |
| `debug-failing-test` | A test is failing and the cause is not obvious |
| `refactor-without-breaking` | Restructuring code while keeping behaviour identical |
| `error-handling-review` | Reviewing error paths in a piece of code |
| `check-hexagonal-boundaries` | Verifying domain/application stay pure |
| `add-prometheus-metric` | Adding a new counter, gauge, or histogram |

### `operations/` — Cross-cutting and operational concerns (4 skills)

| Skill | When to use |
|---|---|
| `add-alert-rule` | Adding a new Prometheus alert rule |
| `update-openapi` | Changing the HTTP contract |
| `add-adr` | Recording a non-trivial architectural decision |
| `add-ci-job` | Adding a job, step, or workflow to GitHub Actions |

## How To Use A Skill

1. Identify which skill matches your current task.
2. Read the entire `SKILL.md` file before doing anything.
3. Follow the steps exactly. Each step is there for a reason.
4. If a step does not apply (rare), note it in your task summary to the human.
5. If you think the skill is wrong, **stop and tell the human**. Do not modify the skill yourself.

## How To Skip A Skill

When no skill matches your task — most one-off chores, config tweaks, doc fixes — proceed without one. Skills are for recurring patterns, not for everything.

## Skill File Structure

Every skill file has:

- **Purpose** — one sentence on what this skill does.
- **When to use** — the trigger conditions.
- **Prerequisites** — what must exist before the skill applies.
- **Steps** — numbered, executable steps.
- **Verification** — how to confirm the work is correct.
- **Common mistakes** — failure modes to avoid.
