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

// buildCancelTestServer wires only the CancelNotification executor.
func buildCancelTestServer(t *testing.T, exec httpadapter.CancelNotificationExecutor) chi.Router {
	t.Helper()
	server := httpadapter.NewServer(httpadapter.ServerOptions{
		CancelNotification: exec,
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

// TestCancelNotification_HappyPath: a successful cancel returns 200 and
// the cancelled notification body — the same shape as GET, just in
// the terminal cancelled state.
func TestCancelNotification_HappyPath(t *testing.T) {
	const idStr = "01940000-0000-7000-8000-0000000000c1"

	var seenID domain.NotificationID
	exec := func(_ context.Context, in application.CancelNotificationInput) (*domain.Notification, error) {
		seenID = in.ID
		n := sampleNotification()
		n.ID = domain.NotificationID(idStr)
		n.Status = domain.StatusCancelled
		return n, nil
	}
	r := buildCancelTestServer(t, exec)

	req := httptest.NewRequest(nethttp.MethodPatch, "/api/v1/notifications/"+idStr+"/cancel", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	require.Equal(t, domain.NotificationID(idStr), seenID, "path id reaches the use case")

	var out api.Notification
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Equal(t, idStr, out.Id.String())
	require.Equal(t, api.StatusCancelled, out.Status, "response carries the cancelled status")
}

// TestCancelNotification_NotFound_404: canceling a non-existent id
// surfaces ports.ErrNotFound via the translator.
func TestCancelNotification_NotFound_404(t *testing.T) {
	exec := func(_ context.Context, _ application.CancelNotificationInput) (*domain.Notification, error) {
		return nil, ports.ErrNotFound
	}
	r := buildCancelTestServer(t, exec)
	req := httptest.NewRequest(nethttp.MethodPatch,
		"/api/v1/notifications/01940000-0000-7000-8000-0000000000c2/cancel", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNotFound, rr.Code, "body=%s", rr.Body.String())
	var p struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &p))
	require.Equal(t, "/probs/not-found", p.Type)
}

// TestCancelNotification_AlreadyTerminal_409: canceling a notification
// that has already reached a terminal state (delivered, failed,
// cancelled) yields a TransitionError → 409 conflict. The handler
// surfaces the from/to detail unchanged.
func TestCancelNotification_AlreadyTerminal_409(t *testing.T) {
	exec := func(_ context.Context, _ application.CancelNotificationInput) (*domain.Notification, error) {
		return nil, &domain.TransitionError{
			From: domain.StatusDelivered,
			To:   domain.StatusCancelled,
		}
	}
	r := buildCancelTestServer(t, exec)
	req := httptest.NewRequest(nethttp.MethodPatch,
		"/api/v1/notifications/01940000-0000-7000-8000-0000000000c3/cancel", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusConflict, rr.Code, "body=%s", rr.Body.String())
	var p struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &p))
	require.Equal(t, "/probs/invalid-transition", p.Type)
	require.Contains(t, p.Detail, string(domain.StatusDelivered))
	require.Contains(t, p.Detail, string(domain.StatusCancelled))
}

// TestCancelNotification_NilExecutor_NotImplemented: 501 when no
// executor is wired.
func TestCancelNotification_NilExecutor_NotImplemented(t *testing.T) {
	r := buildCancelTestServer(t, nil)
	req := httptest.NewRequest(nethttp.MethodPatch,
		"/api/v1/notifications/01940000-0000-7000-8000-0000000000c4/cancel", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNotImplemented, rr.Code, "body=%s", rr.Body.String())
}
