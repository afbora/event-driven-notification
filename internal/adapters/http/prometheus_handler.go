package http

import (
	"bytes"
	"context"
	"fmt"

	"github.com/prometheus/common/expfmt"

	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
)

// GetPrometheusMetrics overrides the embedded stub and renders the
// registered metric set in the Prometheus exposition format. The
// handler is wired through the strict-server (not promhttp.Handler
// mounted on a sibling route) so the same chain — request id, log,
// metrics middleware — wraps it as every other endpoint.
//
// Encoding cost: every scrape rebuilds the response body. Production
// scrape intervals are 10-60s; the gather + encode for a few hundred
// metrics is dominated by network IO, so the simplicity is the right
// trade.
func (s *Server) GetPrometheusMetrics(_ context.Context, _ api.GetPrometheusMetricsRequestObject) (api.GetPrometheusMetricsResponseObject, error) {
	if s.prometheusGatherer == nil {
		return nil, ErrNotImplemented
	}

	mfs, err := s.prometheusGatherer.Gather()
	if err != nil {
		return nil, fmt.Errorf("prometheus gather: %w", err)
	}

	var buf bytes.Buffer
	enc := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfs {
		if encErr := enc.Encode(mf); encErr != nil {
			return nil, fmt.Errorf("prometheus encode %s: %w", mf.GetName(), encErr)
		}
	}

	return api.GetPrometheusMetrics200TextResponse(buf.String()), nil
}
