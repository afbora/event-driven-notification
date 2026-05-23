# Skill: add-prometheus-metric

## Purpose

Add a Prometheus counter, gauge, or histogram so a new piece of system behaviour becomes observable.

## When To Use

- A new code path needs to be measurable (rate, total, latency).
- An alert rule needs a metric that does not exist yet.
- An operator asks "how often does X happen?" and the answer is currently in logs only.

## Prerequisites

- The metric does not already exist (check `internal/infrastructure/metrics/`).
- You can name the metric per the Prometheus naming guide.
- You know the labels (low cardinality only — never IDs, never URLs with parameters, never user input).

## Steps

### 1. Choose the metric type

| Type | When |
|---|---|
| Counter | Monotonically increasing count of events. Examples: notifications sent, errors. |
| Gauge | A value that goes up and down. Examples: queue depth, in-flight requests, circuit breaker state. |
| Histogram | Distribution of values. Examples: request latency, message size. |
| Summary | Almost always prefer histogram. |

### 2. Choose the name

Format: `<namespace>_<subsystem>_<name>_<unit>`.

Our namespace is `notifications`. Subsystems include `api`, `worker`, `queue`, `provider`, `db`, `redis`.

Examples:

- `notifications_worker_attempts_total{channel,outcome}` — counter
- `notifications_queue_depth{queue}` — gauge
- `notifications_provider_call_duration_seconds{channel,provider}` — histogram

Units:

- `_total` suffix for counters.
- `_seconds` for durations (never milliseconds).
- `_bytes` for sizes.
- No suffix for unitless gauges.

### 3. Choose the labels carefully

**Rule: total cardinality across all label combinations must stay bounded.** Targets:

- 3-5 channel values.
- ~10 endpoint paths.
- ~5 status codes.

That is at most a few hundred series per metric. **Never** use as a label:

- Notification ID, recipient, request body — unbounded.
- URL query strings — unbounded.
- User IDs — unbounded.
- Error messages — unbounded.

If you want to know "how many failures had reason X", define a small enum of reasons (`timeout`, `4xx`, `5xx`, `circuit_open`, `rate_limited`) and use that as a label.

### 4. Register the metric

In `internal/infrastructure/metrics/metrics.go`:

```go
var (
    workerAttemptsTotal = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Namespace: "notifications",
            Subsystem: "worker",
            Name:      "attempts_total",
            Help:      "Total notification delivery attempts, labelled by channel and outcome.",
        },
        []string{"channel", "outcome"},
    )
)
```

`promauto.NewCounterVec` registers with the default registry. Use the default for everything; do not create custom registries unless there is a documented reason.

### 5. Emit the metric at the right place

For counters, increment **after** the event has happened, not before:

```go
err := provider.Send(ctx, n)
outcome := "success"
if err != nil {
    outcome = classifyOutcome(err)
}
metrics.WorkerAttemptsTotal.WithLabelValues(string(n.Channel), outcome).Inc()
```

For gauges, set them at the source of truth (queue worker reports its depth, not the API):

```go
metrics.QueueDepth.WithLabelValues("default").Set(float64(depth))
```

For histograms, observe the duration:

```go
start := time.Now()
err := provider.Send(ctx, n)
metrics.ProviderCallDuration.WithLabelValues(string(n.Channel), providerName).Observe(time.Since(start).Seconds())
```

### 6. Add a unit test

Verify the metric exists and increments:

```go
func TestProcessNotification_IncrementsAttempts(t *testing.T) {
    metrics.Reset()
    // ... run use case
    require.Equal(t, 1.0, testutil.ToFloat64(metrics.WorkerAttemptsTotal.WithLabelValues("sms", "success")))
}
```

### 7. Document the metric in `docs/METRICS.md`

A short paragraph: what it measures, units, label values, when it changes.

### 8. If you added a metric to support an alert

Cross-link in `RUNBOOK.md` and in the alert rule's `annotations.summary`.

## Verification

- [ ] Metric appears at `http://localhost:8080/metrics` after `docker compose up`.
- [ ] Label cardinality is bounded.
- [ ] Unit test passes.
- [ ] Documentation in `METRICS.md` is updated.
- [ ] If linked to an alert, the alert rule references the metric correctly.

## Common Mistakes

- Using high-cardinality labels (IDs, URLs, error messages). This breaks Prometheus.
- Suffix mismatch: a counter without `_total`, a duration without `_seconds`. Tooling expects conventions.
- Incrementing a counter on every loop iteration when you meant per request.
- Setting a gauge without source-of-truth ownership (two goroutines fight over the value).
- Emitting metrics inside hot loops without batching, killing throughput.
- Forgetting to register the metric — it just silently does not appear.
- Adding a metric without an alert or dashboard reference. If nobody looks at it, it does not exist.
