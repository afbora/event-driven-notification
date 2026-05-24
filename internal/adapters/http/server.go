package http

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// CreateNotificationExecutor is the slim function-type contract the
// CreateNotification handler depends on. Mirrors the pattern used by
// the asynq processor: the production wiring (cmd/api) passes
// (*application.CreateNotification).Execute, tests pass a closure that
// records inputs and returns a controlled outcome.
type CreateNotificationExecutor func(ctx context.Context, in application.CreateNotificationInput) (*domain.Notification, error)

// CreateBatchExecutor is the slim contract for POST
// /api/v1/notifications/batch. Production wires
// (*application.CreateBatch).Execute; tests pass a closure.
type CreateBatchExecutor func(ctx context.Context, in application.CreateBatchInput) (*domain.Batch, error)

// GetNotificationExecutor is the slim contract for GET
// /api/v1/notifications/{id}. Production wires
// (*application.GetNotification).Execute; tests pass a closure.
type GetNotificationExecutor func(ctx context.Context, in application.GetNotificationInput) (*domain.Notification, error)

// ListNotificationsExecutor is the slim contract for GET
// /api/v1/notifications. Production wires
// (*application.ListNotifications).Execute; tests pass a closure.
type ListNotificationsExecutor func(ctx context.Context, in application.ListNotificationsInput) (application.ListNotificationsOutput, error)

// CancelNotificationExecutor is the slim contract for PATCH
// /api/v1/notifications/{id}/cancel. Production wires
// (*application.CancelNotification).Execute; tests pass a closure.
type CancelNotificationExecutor func(ctx context.Context, in application.CancelNotificationInput) (*domain.Notification, error)

// GetNotificationTraceExecutor is the slim contract for GET
// /api/v1/notifications/{id}/trace. Production wires
// (*application.GetNotificationTrace).Execute; tests pass a closure.
type GetNotificationTraceExecutor func(ctx context.Context, in application.GetNotificationTraceInput) ([]*domain.NotificationLog, error)

// GetBatchExecutor is the slim contract for GET
// /api/v1/notifications/batch/{id}. Production wires
// (*application.GetBatch).Execute; tests pass a closure.
type GetBatchExecutor func(ctx context.Context, in application.GetBatchInput) (*domain.Batch, error)

// CreateTemplateExecutor is the slim contract for POST /api/v1/templates.
type CreateTemplateExecutor func(ctx context.Context, in application.CreateTemplateInput) (*domain.Template, error)

// GetTemplateExecutor is the slim contract for GET /api/v1/templates/{id}.
type GetTemplateExecutor func(ctx context.Context, in application.GetTemplateInput) (*domain.Template, error)

// ListTemplatesExecutor is the slim contract for GET /api/v1/templates.
type ListTemplatesExecutor func(ctx context.Context, in application.ListTemplatesInput) (application.ListTemplatesOutput, error)

// ReplaceTemplateExecutor is the slim contract for PUT /api/v1/templates/{id}.
type ReplaceTemplateExecutor func(ctx context.Context, in application.ReplaceTemplateInput) (*domain.Template, error)

// DeleteTemplateExecutor is the slim contract for DELETE /api/v1/templates/{id}.
type DeleteTemplateExecutor func(ctx context.Context, in application.DeleteTemplateInput) error

// ReadinessCheck verifies that one downstream dependency is reachable.
// /healthz/ready invokes every configured check in turn; any error
// flips the response to 503. Production wires one check per critical
// dependency (Postgres ping, Redis ping); tests inject closures.
type ReadinessCheck func(ctx context.Context) error

// JSONMetricsSnapshot is the small, JSON-friendly subset of the
// Prometheus exposition data that /api/v1/metrics returns. Used by
// clients that cannot consume Prometheus text format (dashboards,
// ad-hoc scripts). SuccessRate is a pointer so the provider can
// explicitly signal "no data this window" instead of an ambiguous 0.
type JSONMetricsSnapshot struct {
	CreatedPerMinute   int
	DeliveredPerMinute int
	FailedPerMinute    int
	QueueDepth         int
	SuccessRate        *float64
}

// JSONMetricsProvider returns one snapshot per scrape. Production wires
// a Prometheus-querying provider (or an inline counter aggregation);
// tests inject a closure.
type JSONMetricsProvider func(ctx context.Context) (JSONMetricsSnapshot, error)

// ServerOptions bundles the per-operation executors the Server needs.
// Each operation has its own slot so partial wiring is legal — every
// handler method on Server begins with a nil-check on its executor
// and returns ErrNotImplemented (→ 501) when absent. Production
// composes the full set; tests inject only what they exercise.
type ServerOptions struct {
	CreateNotification   CreateNotificationExecutor
	CreateBatch          CreateBatchExecutor
	GetNotification      GetNotificationExecutor
	ListNotifications    ListNotificationsExecutor
	CancelNotification   CancelNotificationExecutor
	GetNotificationTrace GetNotificationTraceExecutor
	GetBatch             GetBatchExecutor

	CreateTemplate  CreateTemplateExecutor
	GetTemplate     GetTemplateExecutor
	ListTemplates   ListTemplatesExecutor
	ReplaceTemplate ReplaceTemplateExecutor
	DeleteTemplate  DeleteTemplateExecutor

	// ReadinessChecks runs against /healthz/ready. Empty means the
	// API has no downstream dependencies to verify and reports ready
	// the moment it can accept HTTP.
	ReadinessChecks []ReadinessCheck

	// PrometheusGatherer is the source for /metrics. Production wires
	// prometheus.DefaultGatherer (or a custom registry); leave nil and
	// the endpoint falls through to 501 via the embedded stub.
	PrometheusGatherer prometheus.Gatherer

	// JSONMetrics returns the snapshot rendered by /api/v1/metrics.
	JSONMetrics JSONMetricsProvider
}

// Server is the adapter that implements api.StrictServerInterface by
// dispatching each operation to its application use case. Every handler
// method begins with a nil-check on its executor and returns
// ErrNotImplemented (→ 501) when absent — tests wire only the slots
// they exercise.
//
// Concurrency: Server holds only constructor-injected references; it
// is safe to share across goroutines once NewServer returns.
type Server struct {
	createNotification   CreateNotificationExecutor
	createBatch          CreateBatchExecutor
	getNotification      GetNotificationExecutor
	listNotifications    ListNotificationsExecutor
	cancelNotification   CancelNotificationExecutor
	getNotificationTrace GetNotificationTraceExecutor
	getBatch             GetBatchExecutor

	createTemplate  CreateTemplateExecutor
	getTemplate     GetTemplateExecutor
	listTemplates   ListTemplatesExecutor
	replaceTemplate ReplaceTemplateExecutor
	deleteTemplate  DeleteTemplateExecutor

	readinessChecks    []ReadinessCheck
	prometheusGatherer prometheus.Gatherer
	jsonMetrics        JSONMetricsProvider
}

// NewServer wires the executors carried by opts into a Server. Any
// operation whose executor is nil falls through to a 501 response —
// the nil-check lives in each handler method.
func NewServer(opts ServerOptions) *Server {
	return &Server{
		createNotification:   opts.CreateNotification,
		createBatch:          opts.CreateBatch,
		getNotification:      opts.GetNotification,
		listNotifications:    opts.ListNotifications,
		cancelNotification:   opts.CancelNotification,
		getNotificationTrace: opts.GetNotificationTrace,
		getBatch:             opts.GetBatch,
		createTemplate:       opts.CreateTemplate,
		getTemplate:          opts.GetTemplate,
		listTemplates:        opts.ListTemplates,
		replaceTemplate:      opts.ReplaceTemplate,
		deleteTemplate:       opts.DeleteTemplate,
		readinessChecks:      opts.ReadinessChecks,
		prometheusGatherer:   opts.PrometheusGatherer,
		jsonMetrics:          opts.JSONMetrics,
	}
}
