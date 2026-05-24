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
)

// buildListNotificationsTestServer wires only the ListNotifications
// executor so the suite stays focused on a single operation.
func buildListNotificationsTestServer(t *testing.T, exec httpadapter.ListNotificationsExecutor) chi.Router {
	t.Helper()
	server := httpadapter.NewServer(httpadapter.ServerOptions{
		ListNotifications: exec,
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

// TestListNotifications_HappyPath_AllFiltersPropagate: every query
// parameter — status, channel, batch_id, created_after, created_before,
// cursor, limit — reaches the use case input one-to-one. The
// response carries the items and the next_cursor returned by the use
// case.
func TestListNotifications_HappyPath_AllFiltersPropagate(t *testing.T) {
	const (
		batchID    = "01940000-0000-7000-8000-00000000bbbb"
		nextCursor = "next-page-cursor-token"
	)
	createdAfter := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	createdBefore := time.Date(2026, 5, 24, 23, 59, 59, 0, time.UTC)

	var seen application.ListNotificationsInput
	exec := func(_ context.Context, in application.ListNotificationsInput) (application.ListNotificationsOutput, error) {
		seen = in
		n1 := sampleNotification()
		n1.ID = domain.NotificationID("01940000-0000-7000-8000-000000000001")
		n2 := sampleNotification()
		n2.ID = domain.NotificationID("01940000-0000-7000-8000-000000000002")
		return application.ListNotificationsOutput{
			Notifications: []*domain.Notification{n1, n2},
			NextCursor:    nextCursor,
		}, nil
	}

	r := buildListNotificationsTestServer(t, exec)

	url := "/api/v1/notifications" +
		"?status=delivered" +
		"&channel=sms" +
		"&batch_id=" + batchID +
		"&created_after=" + createdAfter.Format(time.RFC3339) +
		"&created_before=" + createdBefore.Format(time.RFC3339) +
		"&cursor=opaque-prev-token" +
		"&limit=25"
	req := httptest.NewRequest(nethttp.MethodGet, url, nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	require.Equal(t, "delivered", seen.Status)
	require.Equal(t, "sms", seen.Channel)
	require.NotNil(t, seen.BatchID)
	require.Equal(t, domain.BatchID(batchID), *seen.BatchID)
	require.NotNil(t, seen.CreatedAfter)
	require.True(t, seen.CreatedAfter.Equal(createdAfter), "got %v want %v", *seen.CreatedAfter, createdAfter)
	require.NotNil(t, seen.CreatedBefore)
	require.True(t, seen.CreatedBefore.Equal(createdBefore), "got %v want %v", *seen.CreatedBefore, createdBefore)
	require.Equal(t, "opaque-prev-token", seen.Cursor)
	require.Equal(t, 25, seen.Limit)

	var out api.NotificationPage
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Len(t, out.Items, 2)
	require.Equal(t, "01940000-0000-7000-8000-000000000001", out.Items[0].Id.String())
	require.Equal(t, "01940000-0000-7000-8000-000000000002", out.Items[1].Id.String())
	require.NotNil(t, out.NextCursor)
	require.Equal(t, nextCursor, *out.NextCursor)
}

// TestListNotifications_NoFilters_EmptyInput: every filter is optional;
// a bare GET reaches the use case with an empty input struct (zero
// values everywhere). This is the "give me the first page of
// everything" scenario.
func TestListNotifications_NoFilters_EmptyInput(t *testing.T) {
	var seen application.ListNotificationsInput
	exec := func(_ context.Context, in application.ListNotificationsInput) (application.ListNotificationsOutput, error) {
		seen = in
		return application.ListNotificationsOutput{
			Notifications: nil,
			NextCursor:    "",
		}, nil
	}

	r := buildListNotificationsTestServer(t, exec)
	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/notifications", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	require.Equal(t, "", seen.Status)
	require.Equal(t, "", seen.Channel)
	require.Nil(t, seen.BatchID)
	require.Nil(t, seen.CreatedAfter)
	require.Nil(t, seen.CreatedBefore)
	require.Equal(t, "", seen.Cursor)
	require.Equal(t, 0, seen.Limit)

	var out api.NotificationPage
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Empty(t, out.Items, "empty result must serialize to an empty array, not null")
	require.Nil(t, out.NextCursor, "no next cursor when the use case did not return one")
}

// TestListNotifications_NextCursorOmittedWhenEmpty: when the use case
// returns an empty NextCursor (last page), the response must omit the
// `next_cursor` field entirely — clients use the absence as the
// "end of pages" signal.
func TestListNotifications_NextCursorOmittedWhenEmpty(t *testing.T) {
	exec := func(_ context.Context, _ application.ListNotificationsInput) (application.ListNotificationsOutput, error) {
		return application.ListNotificationsOutput{
			Notifications: []*domain.Notification{sampleNotification()},
			NextCursor:    "",
		}, nil
	}
	r := buildListNotificationsTestServer(t, exec)
	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/notifications", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	// Inspect the raw body to confirm the field is absent — a nil
	// pointer in the Go struct could still serialize as `"next_cursor":null`
	// if omitempty were misconfigured.
	require.NotContains(t, rr.Body.String(), "next_cursor",
		"empty next cursor must not be emitted")
}

// TestListNotifications_InvalidStatus_400: the use case rejects an
// unknown status string with domain.ErrInvalidStatus; the translator
// turns that into a 400 problem.
func TestListNotifications_InvalidStatus_400(t *testing.T) {
	exec := func(_ context.Context, _ application.ListNotificationsInput) (application.ListNotificationsOutput, error) {
		return application.ListNotificationsOutput{}, domain.ErrInvalidStatus
	}
	r := buildListNotificationsTestServer(t, exec)
	req := httptest.NewRequest(nethttp.MethodGet,
		"/api/v1/notifications?status=garbage", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusBadRequest, rr.Code, "body=%s", rr.Body.String())
	var p struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &p))
	require.Equal(t, "/probs/invalid-status", p.Type)
}

// TestListNotifications_NilExecutor_NotImplemented: an unwired
// executor still returns 501 via the embedded stub.
func TestListNotifications_NilExecutor_NotImplemented(t *testing.T) {
	r := buildListNotificationsTestServer(t, nil)
	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/notifications", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNotImplemented, rr.Code, "body=%s", rr.Body.String())
}
