package http_test

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// buildGetBatchTestServer wires only the GetBatch executor.
func buildGetBatchTestServer(t *testing.T, exec httpadapter.GetBatchExecutor) chi.Router {
	t.Helper()
	server := httpadapter.NewServer(httpadapter.ServerOptions{
		GetBatch: exec,
	})
	r := chi.NewRouter()
	api.HandlerFromMux(api.NewStrictHandlerWithOptions(
		server, nil,
		api.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  httpadapter.RespondWithError,
			ResponseErrorHandlerFunc: httpadapter.RespondWithError,
		},
	), r)
	return r
}

// TestGetBatch_HappyPath_NotificationsInlined: unlike POST 202 (which
// omits members to keep the payload small), GET inlines every member
// notification so a single round-trip yields the full picture.
func TestGetBatch_HappyPath_NotificationsInlined(t *testing.T) {
	const batchID = "01940000-0000-7000-8000-00000000bbbb"

	exec := func(_ context.Context, in application.GetBatchInput) (*domain.Batch, error) {
		require.Equal(t, domain.BatchID(batchID), in.ID, "path id reaches the use case")
		bID := domain.BatchID(batchID)
		mk := func(idStr string) *domain.Notification {
			n := sampleNotification()
			n.ID = domain.NotificationID(idStr)
			n.BatchID = &bID
			return n
		}
		return &domain.Batch{
			ID:            bID,
			CorrelationID: "01HXYZBATCHGETCORR0001",
			Notifications: []*domain.Notification{
				mk("01940000-0000-7000-8000-000000000a01"),
				mk("01940000-0000-7000-8000-000000000a02"),
				mk("01940000-0000-7000-8000-000000000a03"),
			},
			CreatedAt: fixedTime,
		}, nil
	}
	r := buildGetBatchTestServer(t, exec)

	req := httptest.NewRequest(nethttp.MethodGet,
		"/api/v1/notifications/batch/"+batchID, nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	var out api.Batch
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))

	require.Equal(t, batchID, out.Id.String())
	require.Equal(t, 3, out.Size)
	require.NotNil(t, out.CorrelationId)
	require.Equal(t, "01HXYZBATCHGETCORR0001", *out.CorrelationId)
	require.NotNil(t, out.Notifications, "GET response must include the member notifications")
	require.Len(t, *out.Notifications, 3)
	require.Equal(t, "01940000-0000-7000-8000-000000000a01", (*out.Notifications)[0].Id.String())
}

// TestGetBatch_NotFound_404: an unknown id propagates as
// ports.ErrNotFound and the translator maps it to a 404 problem.
func TestGetBatch_NotFound_404(t *testing.T) {
	exec := func(_ context.Context, _ application.GetBatchInput) (*domain.Batch, error) {
		return nil, ports.ErrNotFound
	}
	r := buildGetBatchTestServer(t, exec)
	req := httptest.NewRequest(nethttp.MethodGet,
		"/api/v1/notifications/batch/01940000-0000-7000-8000-00000000bbcc", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNotFound, rr.Code, "body=%s", rr.Body.String())
	var p struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &p))
	require.Equal(t, "/probs/not-found", p.Type)
}

// TestGetBatch_NilExecutor_NotImplemented: 501 when not wired.
func TestGetBatch_NilExecutor_NotImplemented(t *testing.T) {
	r := buildGetBatchTestServer(t, nil)
	req := httptest.NewRequest(nethttp.MethodGet,
		"/api/v1/notifications/batch/01940000-0000-7000-8000-00000000bbdd", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNotImplemented, rr.Code, "body=%s", rr.Body.String())
}
