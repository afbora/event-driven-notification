// Package metrics owns the Prometheus collector definitions called
// out in CLAUDE.md §12.1. Every binary (api, worker, reconciler)
// shares one *Metrics instance — the registry it registers against
// is also shared so /metrics returns a unified view.
//
// Naming convention (Prometheus best practice + CLAUDE.md):
//
//	notifications_*  — domain-specific counters / gauges / histograms
//	http_*           — request-level signals
//
// Adding a new metric: declare the field on Metrics, construct it
// in New, then expose a typed Inc/Set/Observe method so call sites
// stay short and the label cardinality is grep-able.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// CircuitBreakerState encodes the three states the gauge exposes
// per CLAUDE.md §12.1. Numeric values are fixed by contract so
// Grafana dashboards can map them back to labels.
type CircuitBreakerState float64

// CircuitBreakerState enum values match the integers stamped on the
// gauge so Grafana panels can pivot on numeric thresholds.
const (
	CircuitClosed   CircuitBreakerState = 0
	CircuitOpen     CircuitBreakerState = 1
	CircuitHalfOpen CircuitBreakerState = 2
)

// processingBuckets reflects the histogram boundaries fixed in
// CLAUDE.md §12.1. Anything slower than 30 s lands in +Inf — that
// is intentionally the alerting tail.
var processingBuckets = []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30}

// httpBuckets covers the typical HTTP request budget: under 5 ms
// for cached lookups, up to 5 s for the slowest write paths.
var httpBuckets = []float64{0.005, 0.01, 0.05, 0.1, 0.5, 1, 5}

// Metrics is the collector bundle every binary holds. Each exported
// vector is also reachable directly (NotificationsCreatedTotal())
// for tests that need to assert against a label set; production
// code reaches for the verb methods (NotificationCreated, etc.).
type Metrics struct {
	notificationsCreatedTotal *prometheus.CounterVec
	notificationsDeliveredTot *prometheus.CounterVec
	notificationsFailedTotal  *prometheus.CounterVec
	notificationsAttemptsTot  *prometheus.CounterVec
	httpRequestsTotal         *prometheus.CounterVec
	inboundRateLimitHitsTotal *prometheus.CounterVec
	outboundRateLimitHitsTot  *prometheus.CounterVec

	notificationsQueueDepth *prometheus.GaugeVec
	circuitBreakerState     *prometheus.GaugeVec
	notificationsWSClients  prometheus.Gauge

	notificationsProcessing *prometheus.HistogramVec
	httpRequestDuration     *prometheus.HistogramVec
}

// New constructs every collector and registers it on reg. Tests
// pass prometheus.NewRegistry() for isolation; production passes
// the registry served at /metrics.
func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		notificationsCreatedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "notifications_created_total",
			Help: "Total notifications accepted by the API, by channel and priority.",
		}, []string{"channel", "priority"}),

		notificationsDeliveredTot: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "notifications_delivered_total",
			Help: "Total notifications successfully delivered by the worker, by channel.",
		}, []string{"channel"}),

		notificationsFailedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "notifications_failed_total",
			Help: "Total notifications that reached the failed terminal state.",
		}, []string{"channel", "reason"}),

		notificationsAttemptsTot: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "notifications_attempts_total",
			Help: "Total provider call attempts by channel and outcome (success/transient/permanent).",
		}, []string{"channel", "outcome"}),

		httpRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests by method, path, and status code.",
		}, []string{"method", "path", "status"}),

		inboundRateLimitHitsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "inbound_rate_limit_hits_total",
			Help: "Total inbound requests rejected by the per-IP rate limiter.",
		}, []string{"endpoint"}),

		outboundRateLimitHitsTot: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "outbound_rate_limit_hits_total",
			Help: "Total worker attempts deferred by the per-channel outbound rate limiter.",
		}, []string{"channel"}),

		notificationsQueueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "notifications_queue_depth",
			Help: "Current depth of each asynq priority queue, sampled periodically.",
		}, []string{"queue"}),

		circuitBreakerState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "notifications_circuit_breaker_state",
			Help: "Current state of each provider circuit breaker (0=closed, 1=open, 2=half-open).",
		}, []string{"provider"}),

		notificationsWSClients: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "notifications_websocket_clients",
			Help: "Number of clients currently connected to the WebSocket fan-out hub.",
		}),

		notificationsProcessing: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "notifications_processing_duration_seconds",
			Help:    "Wall time spent processing a notification, from atomic claim to terminal status.",
			Buckets: processingBuckets,
		}, []string{"channel"}),

		httpRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Wall time spent serving an HTTP request, by method and path.",
			Buckets: httpBuckets,
		}, []string{"method", "path"}),
	}

	reg.MustRegister(
		m.notificationsCreatedTotal,
		m.notificationsDeliveredTot,
		m.notificationsFailedTotal,
		m.notificationsAttemptsTot,
		m.httpRequestsTotal,
		m.inboundRateLimitHitsTotal,
		m.outboundRateLimitHitsTot,
		m.notificationsQueueDepth,
		m.circuitBreakerState,
		m.notificationsWSClients,
		m.notificationsProcessing,
		m.httpRequestDuration,
	)
	return m
}

// --- Verb methods — terse call sites, single point to grep --------------

// NotificationCreated increments the created counter for the given
// label set. Called from CreateNotification's success path.
func (m *Metrics) NotificationCreated(channel, priority string) {
	m.notificationsCreatedTotal.WithLabelValues(channel, priority).Inc()
}

// NotificationDelivered increments on every successful provider call.
func (m *Metrics) NotificationDelivered(channel string) {
	m.notificationsDeliveredTot.WithLabelValues(channel).Inc()
}

// NotificationFailed increments on the terminal failed transition.
// reason should be a short, low-cardinality label
// (worker_timeout, permanent, retries_exhausted, ...).
func (m *Metrics) NotificationFailed(channel, reason string) {
	m.notificationsFailedTotal.WithLabelValues(channel, reason).Inc()
}

// NotificationAttempt records one provider call attempt. outcome is
// one of: success, transient, permanent.
func (m *Metrics) NotificationAttempt(channel, outcome string) {
	m.notificationsAttemptsTot.WithLabelValues(channel, outcome).Inc()
}

// HTTPRequest records one served request. status is the HTTP status
// code as a string ("200", "429", ...). The HTTP middleware fires
// this on every handler exit.
func (m *Metrics) HTTPRequest(method, path, status string) {
	m.httpRequestsTotal.WithLabelValues(method, path, status).Inc()
}

// InboundRateLimitHit counts a single 429 emission, by endpoint.
func (m *Metrics) InboundRateLimitHit(endpoint string) {
	m.inboundRateLimitHitsTotal.WithLabelValues(endpoint).Inc()
}

// OutboundRateLimitHit counts a single worker-side deferral.
func (m *Metrics) OutboundRateLimitHit(channel string) {
	m.outboundRateLimitHitsTot.WithLabelValues(channel).Inc()
}

// SetQueueDepth stamps the current depth of the named queue. A
// periodic sampler (cmd/api or cmd/worker) reads asynq's inspector
// and calls this.
func (m *Metrics) SetQueueDepth(queue string, depth int) {
	m.notificationsQueueDepth.WithLabelValues(queue).Set(float64(depth))
}

// SetCircuitBreakerState stamps the current breaker state. The
// circuit package's OnStateChange hook fires this.
func (m *Metrics) SetCircuitBreakerState(provider string, state CircuitBreakerState) {
	m.circuitBreakerState.WithLabelValues(provider).Set(float64(state))
}

// SetWebSocketClients stamps the current count of connected clients.
// The Hub's subscribe/unsubscribe paths fire this.
func (m *Metrics) SetWebSocketClients(count int) {
	m.notificationsWSClients.Set(float64(count))
}

// ObserveProcessing records one end-to-end processing duration.
// Worker fires this after the terminal transition.
func (m *Metrics) ObserveProcessing(channel string, d time.Duration) {
	m.notificationsProcessing.WithLabelValues(channel).Observe(d.Seconds())
}

// ObserveHTTPRequest records one HTTP request duration, paired
// with the HTTPRequest counter.
func (m *Metrics) ObserveHTTPRequest(method, path string, d time.Duration) {
	m.httpRequestDuration.WithLabelValues(method, path).Observe(d.Seconds())
}

// Direct vector accessors below let tests reach for a label set via
// testutil.ToFloat64(m.X().WithLabelValues(...)). Production code
// should prefer the verb methods (NotificationCreated, etc.) so the
// label cardinality stays grep-able at the call site.

// NotificationsCreatedTotal returns the underlying counter vector.
func (m *Metrics) NotificationsCreatedTotal() *prometheus.CounterVec {
	return m.notificationsCreatedTotal
}

// NotificationsDeliveredTotal returns the underlying counter vector.
func (m *Metrics) NotificationsDeliveredTotal() *prometheus.CounterVec {
	return m.notificationsDeliveredTot
}

// NotificationsFailedTotal returns the underlying counter vector.
func (m *Metrics) NotificationsFailedTotal() *prometheus.CounterVec {
	return m.notificationsFailedTotal
}

// NotificationsAttemptsTotal returns the underlying counter vector.
func (m *Metrics) NotificationsAttemptsTotal() *prometheus.CounterVec {
	return m.notificationsAttemptsTot
}

// HTTPRequestsTotal returns the underlying counter vector.
func (m *Metrics) HTTPRequestsTotal() *prometheus.CounterVec { return m.httpRequestsTotal }

// InboundRateLimitHitsTotal returns the underlying counter vector.
func (m *Metrics) InboundRateLimitHitsTotal() *prometheus.CounterVec {
	return m.inboundRateLimitHitsTotal
}

// OutboundRateLimitHitsTotal returns the underlying counter vector.
func (m *Metrics) OutboundRateLimitHitsTotal() *prometheus.CounterVec {
	return m.outboundRateLimitHitsTot
}

// QueueDepth returns the underlying gauge vector.
func (m *Metrics) QueueDepth() *prometheus.GaugeVec { return m.notificationsQueueDepth }

// CircuitBreakerState returns the underlying gauge vector.
func (m *Metrics) CircuitBreakerState() *prometheus.GaugeVec { return m.circuitBreakerState }

// WebSocketClients returns the underlying gauge.
func (m *Metrics) WebSocketClients() prometheus.Gauge { return m.notificationsWSClients }

// NotificationsProcessing returns the underlying histogram vector.
func (m *Metrics) NotificationsProcessing() *prometheus.HistogramVec {
	return m.notificationsProcessing
}

// HTTPRequestDuration returns the underlying histogram vector.
func (m *Metrics) HTTPRequestDuration() *prometheus.HistogramVec { return m.httpRequestDuration }
