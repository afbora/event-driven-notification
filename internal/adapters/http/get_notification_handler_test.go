package http_test

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// buildGetNotificationTestServer is the analog of buildTestServer for
// the GET handler — wires only the GetNotification executor.
func buildGetNotificationTestServer(t *testing.T, exec httpadapter.GetNotificationExecutor) chi.Router {
	t.Helper()
	server := httpadapter.NewServer(httpadapter.ServerOptions{
		GetNotification: exec,
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

// TestGetNotification_HappyPath: a known id yields 200 plus the full
// JSON notification payload; the executor receives the parsed
// domain.NotificationID untouched.
func TestGetNotification_HappyPath(t *testing.T) {
	const idStr = "01940000-0000-7000-8000-0000000000aa"

	var seenID domain.NotificationID
	exec := func(_ context.Context, in application.GetNotificationInput) (*domain.Notification, error) {
		seenID = in.ID
		n := sampleNotification()
		n.ID = domain.NotificationID(idStr)
		return n, nil
	}
	r := buildGetNotificationTestServer(t, exec)

	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/notifications/"+idStr, nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	require.Equal(t, domain.NotificationID(idStr), seenID,
		"path param must reach the use case verbatim")

	var out api.Notification
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Equal(t, idStr, out.Id.String())
	require.Equal(t, api.StatusPending, out.Status)
}

// TestGetNotification_NotFound_404: the use case returns ports.ErrNotFound
// (possibly wrapped) and the translator turns it into a 404 problem.
func TestGetNotification_NotFound_404(t *testing.T) {
	exec := func(_ context.Context, _ application.GetNotificationInput) (*domain.Notification, error) {
		return nil, ports.ErrNotFound
	}
	r := buildGetNotificationTestServer(t, exec)

	missing := uuid.New().String()
	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/notifications/"+missing, nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNotFound, rr.Code, "body=%s", rr.Body.String())
	require.Equal(t, "application/problem+json", rr.Header().Get("Content-Type"))
	var p struct {
		Type   string `json:"type"`
		Status int    `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &p))
	require.Equal(t, "/probs/not-found", p.Type)
	require.Equal(t, nethttp.StatusNotFound, p.Status)
}

// TestGetNotification_CorrelationIDEchoed: when the stored notification
// has a correlation id, the X-Correlation-ID response header carries
// it so the caller can join the read with the request that originally
// created the notification.
func TestGetNotification_CorrelationIDEchoed(t *testing.T) {
	const correlation = "01HXYZRETURNCORR000001"
	exec := func(_ context.Context, _ application.GetNotificationInput) (*domain.Notification, error) {
		n := sampleNotification()
		n.CorrelationID = correlation
		return n, nil
	}
	r := buildGetNotificationTestServer(t, exec)

	req := httptest.NewRequest(nethttp.MethodGet,
		"/api/v1/notifications/01940000-0000-7000-8000-0000000000ab", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	require.Equal(t, correlation, rr.Header().Get("X-Correlation-ID"))
}

// TestGetNotification_NilExecutor_NotImplemented: when no executor is
// wired the embedded stub returns ErrNotImplemented → 501.
func TestGetNotification_NilExecutor_NotImplemented(t *testing.T) {
	r := buildGetNotificationTestServer(t, nil)
	req := httptest.NewRequest(nethttp.MethodGet,
		"/api/v1/notifications/01940000-0000-7000-8000-0000000000ac", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNotImplemented, rr.Code, "body=%s", rr.Body.String())
}
