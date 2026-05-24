package http

import (
	"context"

	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
)

// GetJSONMetrics renders /api/v1/metrics. The endpoint exists for
// clients that cannot speak the Prometheus text exposition format
// (dashboards, ad-hoc scripts, smoke tests) — the data is the same
// data /metrics exposes, just JSON-shaped and trimmed to the most
// useful counters.
//
// SuccessRate is intentionally a pointer in the snapshot: explicit
// `nil` means "no traffic in the window, no rate to report" so the
// API does not lie about the success of zero deliveries.
func (s *Server) GetJSONMetrics(ctx context.Context, _ api.GetJSONMetricsRequestObject) (api.GetJSONMetricsResponseObject, error) {
	if s.jsonMetrics == nil {
		return nil, ErrNotImplemented
	}

	snap, err := s.jsonMetrics(ctx)
	if err != nil {
		return nil, err
	}

	created := snap.CreatedPerMinute
	delivered := snap.DeliveredPerMinute
	failed := snap.FailedPerMinute
	queue := snap.QueueDepth

	return api.GetJSONMetrics200JSONResponse{
		CreatedPerMinute:   &created,
		DeliveredPerMinute: &delivered,
		FailedPerMinute:    &failed,
		QueueDepth:         &queue,
		SuccessRate:        snap.SuccessRate,
	}, nil
}
