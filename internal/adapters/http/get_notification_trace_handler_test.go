package http_test

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// buildTraceTestServer wires only the GetNotificationTrace executor.
func buildTraceTestServer(t *testing.T, exec httpadapter.GetNotificationTraceExecutor) chi.Router {
	t.Helper()
	server := httpadapter.NewServer(httpadapter.ServerOptions{
		GetNotificationTrace: exec,
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

// TestGetNotificationTrace_HappyPath: the response carries the
// notification id and every log entry in chronological order. Details
// JSON survives the round-trip — support tooling depends on it.
func TestGetNotificationTrace_HappyPath(t *testing.T) {
	const notifID = "01940000-0000-7000-8000-0000000000d1"
	t0 := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)

	exec := func(_ context.Context, in application.GetNotificationTraceInput) ([]*domain.NotificationLog, error) {
		require.Equal(t, domain.NotificationID(notifID), in.NotificationID)
		return []*domain.NotificationLog{
			{
				ID:             domain.LogID("01940000-0000-7000-8000-0000000000a1"),
				NotificationID: domain.NotificationID(notifID),
				CorrelationID:  "01HXYZTRACECORR000001",
				Event:          domain.LogEventCreated,
				CreatedAt:      t0,
			},
			{
				ID:             domain.LogID("01940000-0000-7000-8000-0000000000a2"),
				NotificationID: domain.NotificationID(notifID),
				CorrelationID:  "01HXYZTRACECORR000001",
				Event:          domain.LogEventQueued,
				CreatedAt:      t0.Add(1 * time.Second),
			},
			{
				ID:             domain.LogID("01940000-0000-7000-8000-0000000000a3"),
				NotificationID: domain.NotificationID(notifID),
				CorrelationID:  "01HXYZTRACECORR000001",
				Event:          domain.LogEventDelivered,
				Details:        map[string]any{"provider": "twilio-mock", "status_code": float64(200)},
				CreatedAt:      t0.Add(2 * time.Second),
			},
		}, nil
	}
	r := buildTraceTestServer(t, exec)

	req := httptest.NewRequest(nethttp.MethodGet,
		"/api/v1/notifications/"+notifID+"/trace", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	var out api.NotificationTrace
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))

	require.Equal(t, notifID, out.NotificationId.String())
	require.Len(t, out.Entries, 3)
	require.Equal(t, api.LogEventCreated, out.Entries[0].Event)
	require.Equal(t, api.LogEventQueued, out.Entries[1].Event)
	require.Equal(t, api.LogEventDelivered, out.Entries[2].Event)
	require.NotNil(t, out.Entries[2].Details)
	require.Equal(t, "twilio-mock", (*out.Entries[2].Details)["provider"])
	require.Nil(t, out.Entries[0].Details, "absent details serialize as omitted, not empty object")
}

// TestGetNotificationTrace_EmptyTrace: a notification with no recorded
// events still returns 200 — an empty trace is a valid edge case
// (extremely short-lived notification, or a freshly-failed test
// fixture).
func TestGetNotificationTrace_EmptyTrace(t *testing.T) {
	exec := func(_ context.Context, _ application.GetNotificationTraceInput) ([]*domain.NotificationLog, error) {
		return nil, nil
	}
	r := buildTraceTestServer(t, exec)
	req := httptest.NewRequest(nethttp.MethodGet,
		"/api/v1/notifications/01940000-0000-7000-8000-0000000000d2/trace", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	var out api.NotificationTrace
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Empty(t, out.Entries, "empty trace serializes to an empty array, not null")
}

// TestGetNotificationTrace_NotFound_404: ports.ErrNotFound from the
// use case becomes a 404 problem.
func TestGetNotificationTrace_NotFound_404(t *testing.T) {
	exec := func(_ context.Context, _ application.GetNotificationTraceInput) ([]*domain.NotificationLog, error) {
		return nil, ports.ErrNotFound
	}
	r := buildTraceTestServer(t, exec)
	req := httptest.NewRequest(nethttp.MethodGet,
		"/api/v1/notifications/01940000-0000-7000-8000-0000000000d3/trace", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNotFound, rr.Code, "body=%s", rr.Body.String())
	var p struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &p))
	require.Equal(t, "/probs/not-found", p.Type)
}

// TestGetNotificationTrace_NilExecutor_NotImplemented: 501 when not
// wired.
func TestGetNotificationTrace_NilExecutor_NotImplemented(t *testing.T) {
	r := buildTraceTestServer(t, nil)
	req := httptest.NewRequest(nethttp.MethodGet,
		"/api/v1/notifications/01940000-0000-7000-8000-0000000000d4/trace", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNotImplemented, rr.Code, "body=%s", rr.Body.String())
}
