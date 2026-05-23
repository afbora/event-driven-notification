# ADR-0005: No Custom Frontend; Operational UI Stack Instead

**Status:** Accepted
**Date:** 2026-05-23
**Deciders:** Ahmet Bora

## Context

The assessment evaluates "frontend skills" as one of its criteria. The natural temptation is to build a custom dashboard — a React or Next.js single-page application that shows notifications, queue depth, success rates, and so on.

Three forces push against that temptation:

1. **Time budget.** The brief allots 12 hours; this project is targeting ~22 hours for quality. A bespoke frontend is a 5–8 hour project by itself if it is going to be presentable, and that time comes directly from the backend quality budget.
2. **Duplication.** Every screen we would build — queue depth, throughput, success rate, DLQ inspection, dashboards — already exists, polished, in tools the production world relies on. Grafana, asynqmon, and Adminer are not toy substitutes for a "real" frontend; they are the actual screens engineers use in production.
3. **Assessment context.** The job is for a **Senior Software Engineer (Golang)**. Backend quality is the primary skill being assessed. A weak frontend would distract from a strong backend.

The brief itself is ambiguous on what "frontend skills" means in this context. We interpret it as "the candidate makes thoughtful UI choices for their users," not "the candidate writes React code."

## Decision

There is **no custom frontend** in this repository. The "frontend skills" criterion is addressed by a curated stack of operational and developer UIs, each suited to a specific audience:

| UI                | Audience           | Purpose                                                                   |
|-------------------|--------------------|---------------------------------------------------------------------------|
| Swagger UI        | API consumers      | Live API documentation, try-it-now request forms, schema browser.         |
| asynqmon          | Operators          | Queue depth, retry inspection, DLQ management, in-flight visibility.      |
| Grafana           | Internal teams     | Business and infrastructure dashboards (delivery rate, latency, throughput, circuit breaker state, DLQ size). |
| Adminer           | Developers / DBAs  | Database inspection, ad-hoc queries, schema browsing.                     |
| Redis Commander   | Developers         | Redis state inspection (idempotency cache, rate-limit buckets, pub/sub topics, asynq internals). |

All five are wired in `docker-compose.yml` and reachable on dedicated ports (see README.md). The README's tables and the comprehensive Phase 7 docs explain when to use which.

## Consequences

**Positive:**

- The backend quality budget is preserved entirely for the backend.
- Each UI in the stack is a production-grade tool, immediately familiar to anyone who has operated a Go service. The reviewer recognises them.
- No bespoke frontend code to maintain, secure, or document.
- Demonstrates judgement: choosing the right tool for the audience is a senior-level skill.

**Negative:**

- The repository lacks a single "wow, look at this dashboard" screenshot. Reviewers expecting a portfolio piece will not find one.
- Five separate UIs is more cognitive overhead than one unified dashboard, for users who would touch all of them. In practice each audience uses one of them, so this overhead is theoretical.
- We rely on each upstream tool staying maintained. asynqmon in particular is single-vendor; if it stopped being maintained we would need a successor. Risk is acceptable for an assessment; production deployments would consider it.

## Alternatives Considered

1. **Custom Next.js dashboard** — rejected. Would duplicate Grafana (charts) and asynqmon (queue inspection) with less polish, take 5–8 hours, and distract from the backend.
2. **Minimal HTML status page** — rejected. Neither professional enough to impress nor useful enough to operate. The worst of both worlds.
3. **Single embedded UI built into the Go binary** — rejected. Same duplication problem as #1, plus it would couple frontend release cadence to backend deploy.
4. **Skip the criterion entirely** — rejected. The brief lists frontend skills; we address them deliberately, just not through code we write.

## Related

- CLAUDE.md §2.4 (Frontend Skills Demonstrated Through Operational UIs)
- README.md "Service endpoints" table
- `docker-compose.yml` (asynqmon, adminer, redis-commander, grafana services)
