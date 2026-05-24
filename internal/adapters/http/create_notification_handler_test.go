package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// fixedTime is the deterministic timestamp the test injects into every
// fake-built notification so assertions stay stable.
var fixedTime = time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)

// buildTestServer wires a Server with the supplied CreateNotification
// executor and returns a chi.Router ready to serve. The other 13
// operations are left as 501-returning stubs; tests only exercise the
// operation under test.
func buildTestServer(t *testing.T, exec httpadapter.CreateNotificationExecutor) chi.Router {
	t.Helper()
	server := httpadapter.NewServer(httpadapter.ServerOptions{
		CreateNotification: exec,
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

// sampleNotification is the domain notification returned by a happy-path
// fake. Used as the "what the use case produced" stand-in.
func sampleNotification() *domain.Notification {
	id := domain.NotificationID("01940000-0000-7000-8000-000000000abc")
	return &domain.Notification{
		ID:            id,
		CorrelationID: "01HXYZSAMPLECORR000001",
		Channel:       domain.ChannelSMS,
		Priority:      domain.PriorityNormal,
		Status:        domain.StatusPending,
		Recipient:     "+15555550001",
		Content:       "hello",
		Attempts:      0,
		CreatedAt:     fixedTime,
		UpdatedAt:     fixedTime,
	}
}

// TestCreateNotification_HappyPath: a well-formed POST body produces a
// 202, a JSON Notification body, a Location header pointing at the new
// resource, and an echoed X-Correlation-ID.
func TestCreateNotification_HappyPath(t *testing.T) {
	captured := struct {
		called bool
		input  application.CreateNotificationInput
	}{}

	exec := func(_ context.Context, in application.CreateNotificationInput) (*domain.Notification, error) {
		captured.called = true
		captured.input = in
		return sampleNotification(), nil
	}

	r := buildTestServer(t, exec)

	body, err := json.Marshal(map[string]any{
		"channel":   "sms",
		"recipient": "+15555550001",
		"content":   "hello",
		"priority":  "normal",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/notifications", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "k-1")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusAccepted, rr.Code, "body=%s", rr.Body.String())
	require.True(t, captured.called, "use case must be invoked on a valid request")
	require.Equal(t, "sms", captured.input.Channel)
	require.Equal(t, "+15555550001", captured.input.Recipient)
	require.Equal(t, "hello", captured.input.Content)
	require.Equal(t, "normal", captured.input.Priority)
	require.Equal(t, "k-1", captured.input.IdempotencyKey)

	var out api.Notification
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Equal(t, "01940000-0000-7000-8000-000000000abc", out.Id.String())
	require.Equal(t, api.Sms, out.Channel)
	require.Equal(t, api.StatusPending, out.Status)

	require.Equal(t, "/api/v1/notifications/01940000-0000-7000-8000-000000000abc",
		rr.Header().Get("Location"))
	require.Equal(t, "01HXYZSAMPLECORR000001", rr.Header().Get("X-Correlation-ID"))
}

// TestCreateNotification_PropagatesInboundCorrelationID: when the
// caller supplies X-Correlation-ID, the use case input carries the
// same value so logs and queue payloads stay aligned end-to-end.
func TestCreateNotification_PropagatesInboundCorrelationID(t *testing.T) {
	const inbound = "01HXYZINBOUNDCORR0000A"

	var seen string
	exec := func(_ context.Context, in application.CreateNotificationInput) (*domain.Notification, error) {
		seen = in.CorrelationID
		n := sampleNotification()
		n.CorrelationID = inbound
		return n, nil
	}

	r := buildTestServer(t, exec)
	body, _ := json.Marshal(map[string]any{
		"channel": "sms", "recipient": "+15555550001", "content": "hello",
	})
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/notifications", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Correlation-ID", inbound)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusAccepted, rr.Code, "body=%s", rr.Body.String())
	require.Equal(t, inbound, seen, "handler must forward inbound X-Correlation-ID to the use case")
}

// TestCreateNotification_DomainValidationError_400: a domain
// ValidationError (e.g. invalid channel) reaches the error handler and
// is translated to a 400 with the RFC 7807 problem body.
func TestCreateNotification_DomainValidationError_400(t *testing.T) {
	exec := func(_ context.Context, _ application.CreateNotificationInput) (*domain.Notification, error) {
		return nil, domain.ErrInvalidChannel
	}
	r := buildTestServer(t, exec)
	body, _ := json.Marshal(map[string]any{
		"channel": "fax", "recipient": "+15555550001", "content": "hello",
	})
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/notifications", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusBadRequest, rr.Code)
	require.Equal(t, "application/problem+json", rr.Header().Get("Content-Type"))
	var p struct {
		Type   string `json:"type"`
		Status int    `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &p))
	require.Equal(t, "/probs/invalid-channel", p.Type)
	require.Equal(t, nethttp.StatusBadRequest, p.Status)
}

// TestCreateNotification_UseCaseFailure_500: an unmapped error from
// the use case (e.g. infra outage wrapped error) becomes a generic 500
// — implementation details must NOT leak into the response body.
func TestCreateNotification_UseCaseFailure_500(t *testing.T) {
	exec := func(_ context.Context, _ application.CreateNotificationInput) (*domain.Notification, error) {
		return nil, errors.New("postgres: connection refused")
	}
	r := buildTestServer(t, exec)
	body, _ := json.Marshal(map[string]any{
		"channel": "sms", "recipient": "+15555550001", "content": "hello",
	})
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/notifications", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusInternalServerError, rr.Code)
	require.NotContains(t, rr.Body.String(), "connection refused",
		"raw error must not leak to the client for unmapped errors")
}

// TestCreateNotification_MalformedJSON_400: oapi-codegen catches body
// decoding failures and surfaces them as an error to the error
// handler, which writes a 400 problem response.
func TestCreateNotification_MalformedJSON_400(t *testing.T) {
	exec := func(_ context.Context, _ application.CreateNotificationInput) (*domain.Notification, error) {
		t.Fatalf("executor must not be called for malformed input")
		return nil, nil
	}
	r := buildTestServer(t, exec)

	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/notifications",
		bytes.NewBufferString(`{"channel": "sms", "recipient":`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusBadRequest, rr.Code,
		"malformed body must produce 400; got body=%s", rr.Body.String())
}

// TestCreateNotification_UnimplementedSibling_501: the embedded
// unimplemented base ensures that operations the Server has not
// overridden return 501. Probe via GetTemplate (no executor wired)
// without falling over.
func TestCreateNotification_UnimplementedSibling_501(t *testing.T) {
	r := buildTestServer(t, func(_ context.Context, _ application.CreateNotificationInput) (*domain.Notification, error) {
		return sampleNotification(), nil
	})

	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/templates/01940000-0000-7000-8000-000000000001", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusNotImplemented, rr.Code,
		"unwired operations must return 501; body=%s", rr.Body.String())
}
