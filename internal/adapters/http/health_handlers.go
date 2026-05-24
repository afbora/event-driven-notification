package http

import (
	"context"
	"log/slog"
	nethttp "net/http"

	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
)

// GetHealthzLive is the liveness probe. It always returns 200 — its
// only job is to confirm the process is running and the HTTP server
// is accepting requests. Kubernetes uses this to decide whether to
// restart the pod; including downstream checks would cause a Redis
// hiccup to restart every API replica simultaneously.
func (s *Server) GetHealthzLive(_ context.Context, _ api.GetHealthzLiveRequestObject) (api.GetHealthzLiveResponseObject, error) {
	return api.GetHealthzLive200JSONResponse{
		Status: api.Ok,
	}, nil
}

// GetHealthzReady runs every configured ReadinessCheck and reports
// ready only when all of them succeed. A single failure flips the
// response to 503 — Kubernetes stops routing traffic but the pod
// stays alive so the dependency can recover.
//
// The check loop returns on the first failure (no need to keep
// probing — the verdict is already 503). The failed check's error is
// logged with structured fields so operators can correlate the 503
// with the offending dependency.
func (s *Server) GetHealthzReady(ctx context.Context, _ api.GetHealthzReadyRequestObject) (api.GetHealthzReadyResponseObject, error) {
	for i, check := range s.readinessChecks {
		if err := check(ctx); err != nil {
			slog.WarnContext(ctx, "readiness check failed",
				"check_index", i,
				"error", err.Error(),
			)
			return api.GetHealthzReady503ApplicationProblemPlusJSONResponse{
				Type:   strPtr("/probs/dependency-unavailable"),
				Title:  "Dependency Unavailable",
				Status: nethttp.StatusServiceUnavailable,
				Detail: strPtr("One or more downstream dependencies are not responding. The pod will stop receiving traffic until they recover."),
			}, nil
		}
	}

	return api.GetHealthzReady200JSONResponse{
		Status: api.Ok,
	}, nil
}

// strPtr returns a pointer to the given string. The generated Problem
// struct uses pointer fields for optional members; this helper keeps
// the call sites readable.
func strPtr(s string) *string { return &s }
