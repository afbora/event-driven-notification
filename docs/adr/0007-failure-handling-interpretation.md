# ADR-0007: "Failure Handling" Interpreted As End-To-End Failure Response

**Status:** Accepted
**Date:** 2026-05-23
**Deciders:** Ahmet Bora

## Context

The brief lists bonus features. Most have one-line descriptions:

> **Scheduled Notifications:** Allow scheduling notifications for future delivery
> **Template System:** Support message templates with variable substitution
> **WebSocket Updates:** Real-time status updates via WebSocket
> **Distributed Tracing**
> **GitHub Actions CI/CD:** Automated testing and linting pipeline

But one is named without elaboration:

> **Failure Handling**

The candidate is expected to define what "failure handling" means in this context. A narrow reading would scope it to retry behaviour. A broader reading would scope it to **the entire system's response to failure** — automated recovery, human notification, and observability.

The brief also says:

> "provide visibility into delivery status for both internal teams and API consumers"

Internal teams do not watch dashboards continuously. They learn about failures because someone or something tells them. That "something" is alerting — which is a piece of failure handling, even though the brief doesn't say the word.

## Decision

We interpret "Failure Handling" as **the system's end-to-end response to failure**, comprising three layers:

### Layer 1 — Passive Failure Handling (the system recovers on its own)

- **Exponential backoff retry** with jitter, configured in asynq (ADR-0003).
- **Error classification:** permanent (4xx → no retry) vs transient (5xx, timeout, network) → retry with backoff. The `DeliveryResult` DTO carries the retryable flag.
- **Circuit breaker** (`sony/gobreaker`) around provider calls, so a flapping provider does not exhaust worker concurrency.
- **Dead-letter queue** for tasks that exhaust their retry budget; inspectable in asynqmon.
- **Reconciliation safety net** (ADR-0011) for stuck or orphaned notifications.
- **Atomic status claim** (ADR-0009) so worker concurrency, asynq redelivery, and the reconciler cannot cause double-sends.

### Layer 2 — Active Failure Handling (humans are informed and can act)

- **Structured logging** with correlation IDs, propagated end-to-end (CLAUDE.md §2.3).
- **Prometheus alert rules** (`deploy/prometheus/alerts.yml`) covering queue depth, failure rate, processing latency, circuit breaker state, DLQ growth, database/Redis reachability — 12 rules in total at the time of writing.
- **AlertManager** routes alerts; receivers are log-based in dev with documented production swap-in points (Slack, PagerDuty, email).
- **Runbook entries** in `docs/RUNBOOK.md` for every alert, with remediation steps.

### Layer 3 — Observational Failure Handling (failure patterns are visible)

- **Prometheus metrics** for every operational dimension (CLAUDE.md §12.1).
- **Grafana dashboards** (3 of them, scope-tailored: Business Overview, HTTP API Performance, Worker & Queue Health).
- **asynqmon** for DLQ inspection and retry control.
- **Notification trace endpoint** (`GET /api/v1/notifications/{id}/trace`) returning the ordered transition log for support and debugging.

All three layers ship together. Layer 2 in particular is the load-bearing part of this interpretation: alerting + runbooks are what convert a metric into action.

## Consequences

**Positive:**

- The bonus item is addressed substantively, not just acknowledged.
- The same architecture that handles failure also serves the brief's "visibility for internal teams" requirement — alerting is the natural mechanism for that visibility.
- Reviewer sees a complete operational story, not just "we have retries."

**Negative:**

- More containers in `docker-compose.yml` (Prometheus, AlertManager, Grafana). Justified by the alerting + dashboarding need.
- Larger documentation footprint (RUNBOOK.md grows to one entry per alert; ADR-0007 itself; this prose).
- Time investment is non-trivial — Phase 6 of `PLAN.md` is dedicated to it.

## Alternatives Considered

1. **Narrow reading: retry only** — implement exponential backoff and call it done. Rejected because (a) it leaves the "visibility for internal teams" requirement under-served and (b) it doesn't actually demonstrate failure-handling thinking; it just demonstrates that retries exist.
2. **Skip the bonus** — possible, but the bonus is named in the brief and not implementing it would feel like a missed opportunity to demonstrate operational thinking.
3. **Alerting without dashboards** — rejected. Dashboards turn metrics into a story; without them, the alert is naked.
4. **Dashboards without alerts** — rejected. Dashboards alone require humans to watch them. Production failure handling must be push-based.

## Related

- CLAUDE.md §2.1 (Failure Handling interpretation), §3.8 (Observability), §12 (Conventions)
- ADR-0009 (Atomic status claim)
- ADR-0011 (Reconciliation safety net)
- `deploy/prometheus/alerts.yml`
- `docs/RUNBOOK.md`
