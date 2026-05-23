# Skill: add-alert-rule

## Purpose

Add a new Prometheus alert rule that fires when a system behaviour crosses a meaningful threshold, with a corresponding runbook entry so the on-call engineer knows what to do.

## When To Use

- You introduced a new metric and want to be notified when it goes wrong.
- A retrospective revealed a failure mode that was not alerted on.
- A new component (worker pool, provider, dependency) needs operational coverage.

## Prerequisites

- The underlying metric exists and is being scraped (see `add-prometheus-metric`).
- You have decided the severity (critical, warning, info).
- You can articulate what a human should do when the alert fires.

## Steps

### 1. Open `deploy/prometheus/alerts.yml`

Rules are grouped by component. Find the right group or create one:

```yaml
groups:
  - name: worker
    rules:
      - alert: WorkerProcessingStalled
        # ...
```

### 2. Write the alert

```yaml
- alert: WorkerProcessingStalled
  expr: rate(notifications_worker_attempts_total[5m]) == 0
  for: 5m
  labels:
    severity: critical
    component: worker
  annotations:
    summary: "Worker has not processed any notification in 5 minutes"
    description: |
      No worker attempts recorded for 5 minutes despite queue depth > 0.
      This indicates the worker pool is stuck or crashed.
    runbook_url: https://github.com/<repo>/notification-system/blob/main/docs/RUNBOOK.md#workerprocessingstalled
```

**Required fields:**

- `alert`: PascalCase name, no spaces. Used as identifier.
- `expr`: PromQL expression. Should evaluate to a non-empty vector when the condition is met.
- `for`: How long the condition must persist before firing. Prevents flaps on momentary spikes. Typical: 5m for warnings, 1-5m for critical.
- `labels.severity`: `critical`, `warning`, or `info`. AlertManager routes on this.
- `labels.component`: which subsystem (api, worker, queue, db, redis, provider).
- `annotations.summary`: one-line summary for the alert notification.
- `annotations.description`: longer multi-line text with context.
- `annotations.runbook_url`: link to the RUNBOOK.md section.

### 3. Choose thresholds carefully

Too sensitive → alert fatigue, ignored alerts.
Too lax → real problems missed.

Rules of thumb:

- **Critical:** something is broken in a way that hurts users right now. Page someone.
  - Examples: queue stalled, success rate < 50%, DB unreachable.
- **Warning:** something is wrong, will hurt users if not addressed in business hours.
  - Examples: circuit breaker open, high latency, retry rate elevated.
- **Info:** unusual but not necessarily wrong. Investigate at leisure.
  - Examples: traffic spike, rate limit frequently hit.

### 4. Write the runbook entry

In `docs/RUNBOOK.md`, add a section with the same anchor:

```markdown
## WorkerProcessingStalled

**What it means:** The worker pool has not processed any notification in 5 minutes despite queue depth > 0. The worker is stuck, crashed, or unable to reach Redis.

**What to check first:**

1. Are worker pods/processes running? Check `docker compose ps worker`.
2. Are worker logs flowing? `docker compose logs --tail=100 worker`.
3. Is Redis reachable from the worker? Check the `notifications_redis_up` metric.
4. Is the queue actually non-empty? Check `notifications_queue_depth` gauge.

**Common causes:**

- Worker process crashed and was not restarted.
- Redis connection lost; reconnect not happening.
- Deadlock in the worker code path.
- Worker is processing one very slow task (check current attempt logs).

**Remediation:**

1. If worker process is down: `docker compose restart worker`.
2. If Redis is unreachable: investigate Redis health; fix that first.
3. If a single task is stuck: identify the task in asynqmon UI (http://localhost:8081), cancel it, file a bug.
4. If unknown: enable debug logging, restart worker, observe.

**Escalation:**

If queue depth grows past 100k while the worker is stalled, escalate to backend on-call immediately — user-visible delivery is being delayed.

**Related dashboards:**

- Worker & Queue Health: http://localhost:3001/d/worker-queue
- Notification System Overview: http://localhost:3001/d/notifications-overview
```

### 5. Verify the rule syntax

```sh
make alerts-lint
```

This runs `promtool check rules deploy/prometheus/alerts.yml` and catches syntax errors.

### 6. Test the alert manually

Bring up the stack:

```sh
docker compose up -d
```

Open Prometheus at `http://localhost:9090/alerts`. The new rule should be listed as `inactive`.

Cause the condition (kill the worker, fill the queue, etc.). Wait for the `for:` duration. The alert should transition to `pending`, then `firing`.

Check AlertManager at `http://localhost:9093`. The alert should appear in the active list.

### 7. Commit

```
feat(deploy/prometheus): add WorkerProcessingStalled alert rule
docs(runbook): add WorkerProcessingStalled remediation steps
```

(Two commits; the rule and runbook can be reviewed independently.)

## Verification

- [ ] `make alerts-lint` passes.
- [ ] Rule appears in Prometheus UI.
- [ ] Alert fires on the intended condition (verified manually or with test traffic).
- [ ] AlertManager receives the alert.
- [ ] Runbook entry exists and is linked from the rule.

## Common Mistakes

- Alerting without a runbook. On-call wakes up at 3am and has no idea what to do.
- Thresholds tuned in isolation. Run the metric in normal traffic for a few minutes first to know what "normal" looks like.
- `for: 0s`. Alerts fire on every transient blip, get ignored, lose meaning.
- Wrong severity. A "warning" for something that requires immediate action will be ignored.
- PromQL that returns the wrong shape — `vector(0)` vs `vector(empty)` matters. Use `rate(...) == 0`, not `rate(...) < 1`.
- Alert names that are vague (`QueueIssue`). Be specific: `WorkerProcessingStalled`, `HighQueueDepth`, `CircuitBreakerOpenSMS`.
- Forgetting to link the runbook URL in the alert annotation.
