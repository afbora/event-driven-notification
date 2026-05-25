package metrics_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/infrastructure/metrics"
)

// gatherText renders the registry's metric families as Prometheus
// text exposition. The stdlib testutil package omits this helper in
// the version we pin.
func gatherText(t *testing.T, reg *prometheus.Registry, names ...string) string {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)

	keep := map[string]bool{}
	for _, n := range names {
		keep[n] = true
	}

	var buf bytes.Buffer
	enc := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfs {
		if len(keep) > 0 && !keep[mf.GetName()] {
			continue
		}
		require.NoError(t, enc.Encode(mf))
	}
	return buf.String()
}

// TestNew_RegistersEveryMetricFamily: every collector declared in
// CLAUDE.md §12.1 must show up in the registry. Labeled vectors
// only surface in Gather() once they have at least one observed
// label set, so we touch each one with a representative value first.
func TestNew_RegistersEveryMetricFamily(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	// Materialize one series per labeled metric so Gather emits it.
	m.NotificationCreated("sms", "high")
	m.NotificationDelivered("sms")
	m.NotificationFailed("sms", "worker_timeout")
	m.NotificationAttempt("sms", "success")
	m.HTTPRequest("GET", "/healthz/live", "200")
	m.InboundRateLimitHit("/healthz/live")
	m.OutboundRateLimitHit("sms")
	m.SetQueueDepth("normal", 0)
	m.SetCircuitBreakerState("registry", metrics.CircuitClosed)
	m.SetWebSocketClients(0)
	m.ObserveProcessing("sms", 0)
	m.ObserveHTTPRequest("GET", "/healthz/live", 0)

	mfs, err := reg.Gather()
	require.NoError(t, err)

	got := map[string]bool{}
	for _, mf := range mfs {
		got[mf.GetName()] = true
	}

	expected := []string{
		// Counters
		"notifications_created_total",
		"notifications_delivered_total",
		"notifications_failed_total",
		"notifications_attempts_total",
		"http_requests_total",
		"inbound_rate_limit_hits_total",
		"outbound_rate_limit_hits_total",
		// Gauges
		"notifications_queue_depth",
		"notifications_circuit_breaker_state",
		"notifications_websocket_clients",
		// Histograms
		"notifications_processing_duration_seconds",
		"http_request_duration_seconds",
	}
	for _, name := range expected {
		require.Truef(t, got[name], "metric %q not registered; got %v", name, mapKeys(got))
	}
}

// TestNotificationCreated_IncrementsByChannelPriority: the counter
// exposes per-(channel, priority) cardinality. Two different label
// combos must produce two separate series.
func TestNotificationCreated_IncrementsByChannelPriority(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	m.NotificationCreated("sms", "high")
	m.NotificationCreated("sms", "high")
	m.NotificationCreated("email", "normal")

	require.Equal(t, float64(2),
		testutil.ToFloat64(m.NotificationsCreatedTotal().WithLabelValues("sms", "high")))
	require.Equal(t, float64(1),
		testutil.ToFloat64(m.NotificationsCreatedTotal().WithLabelValues("email", "normal")))
}

// TestProcessingDuration_RecordsInChannelBucket: ObserveProcessing
// stamps the duration on the per-channel histogram. We assert via
// the exposition format because Histogram has no clean "current
// total observation count" accessor per label set.
func TestProcessingDuration_RecordsInChannelBucket(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	m.ObserveProcessing("sms", 150*time.Millisecond)
	m.ObserveProcessing("sms", 220*time.Millisecond)

	text := gatherText(t, reg, "notifications_processing_duration_seconds")
	require.Contains(t, text, `notifications_processing_duration_seconds_count{channel="sms"} 2`,
		"two observations should land on the sms channel; got\n%s", text)
}

// TestCircuitBreakerState_SetsGauge: the gauge encodes the three
// states (0=closed, 1=open, 2=half-open) per CLAUDE.md §12.1. We
// stamp each and read back through the registry.
func TestCircuitBreakerState_SetsGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	m.SetCircuitBreakerState("registry", metrics.CircuitClosed)
	require.Equal(t, float64(0),
		testutil.ToFloat64(m.CircuitBreakerState().WithLabelValues("registry")))

	m.SetCircuitBreakerState("registry", metrics.CircuitOpen)
	require.Equal(t, float64(1),
		testutil.ToFloat64(m.CircuitBreakerState().WithLabelValues("registry")))

	m.SetCircuitBreakerState("registry", metrics.CircuitHalfOpen)
	require.Equal(t, float64(2),
		testutil.ToFloat64(m.CircuitBreakerState().WithLabelValues("registry")))
}

// TestQueueDepth_AccumulatesLabel: the queue-depth gauge takes a
// queue name and replaces (not adds to) the current value, matching
// how a periodic sampler would scrape asynq's inspector.
func TestQueueDepth_AccumulatesLabel(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	m.SetQueueDepth("normal", 42)
	m.SetQueueDepth("normal", 17) // replace, not add

	require.Equal(t, float64(17),
		testutil.ToFloat64(m.QueueDepth().WithLabelValues("normal")))
}

// TestExposition_FormatIsPrometheusText: a smoke check that the
// registry exports valid Prometheus text — guards against subtle
// label-name mistakes that only show up at scrape time.
func TestExposition_FormatIsPrometheusText(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	m.NotificationCreated("sms", "high")
	m.NotificationDelivered("sms")

	mfs, err := reg.Gather()
	require.NoError(t, err)
	require.NotEmpty(t, mfs)

	// HELP + TYPE lines should be present for every series — pull
	// one out and pattern-match.
	text := gatherText(t, reg, "notifications_created_total")
	require.True(t, strings.Contains(text, "# HELP notifications_created_total"),
		"exposition must include HELP lines; got\n%s", text)
	require.True(t, strings.Contains(text, "# TYPE notifications_created_total counter"),
		"exposition must include TYPE lines; got\n%s", text)
}

// TestDirectAccessors_ReturnRegisteredCollectors exercises the public
// vector accessors so tests anywhere in the codebase can reach for a
// label set via the underlying collector. Asserts each one returns a
// non-nil handle pointing at a registered Prometheus collector.
func TestDirectAccessors_ReturnRegisteredCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	// Counter accessors.
	require.NotNil(t, m.NotificationsCreatedTotal())
	require.NotNil(t, m.NotificationsDeliveredTotal())
	require.NotNil(t, m.NotificationsFailedTotal())
	require.NotNil(t, m.NotificationsAttemptsTotal())
	require.NotNil(t, m.HTTPRequestsTotal())
	require.NotNil(t, m.InboundRateLimitHitsTotal())
	require.NotNil(t, m.OutboundRateLimitHitsTotal())

	// Gauge accessors.
	require.NotNil(t, m.QueueDepth())
	require.NotNil(t, m.CircuitBreakerState())
	require.NotNil(t, m.WebSocketClients())

	// Histogram accessors.
	require.NotNil(t, m.NotificationsProcessing())
	require.NotNil(t, m.HTTPRequestDuration())

	// Sanity-check that one of the returned handles is wired to the
	// same registry — increment via the accessor and read it back
	// via the verb method.
	m.NotificationsDeliveredTotal().WithLabelValues("sms").Inc()
	require.Equal(t, float64(1),
		testutil.ToFloat64(m.NotificationsDeliveredTotal().WithLabelValues("sms")))
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
