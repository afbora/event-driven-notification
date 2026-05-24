package provider_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/adapters/provider"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// webhookHandler is a small dispatch helper for tests that want a single
// server with selectable behavior per request.
func newWebhookServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

// TestWebhookProvider_Success: 202 + canonical response body → delivered
// result carrying the provider-side message id.
func TestWebhookProvider_Success(t *testing.T) {
	var capturedBody string
	var capturedMethod string
	srv := newWebhookServer(t, func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		capturedBody = string(b)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"messageId": "provider-msg-001",
			"status":    "accepted",
			"timestamp": "2026-05-24T12:00:00Z",
		})
	})
	defer srv.Close()

	p := provider.NewWebhookProvider(srv.URL, 2*time.Second)

	got := p.Send(context.Background(), domain.ChannelSMS, "+905551234567", "Hello there")

	require.True(t, got.Success)
	require.Equal(t, "provider-msg-001", got.MessageID)
	require.False(t, got.Retryable)
	require.Greater(t, got.Latency, time.Duration(0))

	// Request shape — POST + JSON body per the brief.
	require.Equal(t, http.MethodPost, capturedMethod)
	var req map[string]string
	require.NoError(t, json.Unmarshal([]byte(capturedBody), &req))
	require.Equal(t, "+905551234567", req["to"])
	require.Equal(t, "sms", req["channel"])
	require.Equal(t, "Hello there", req["content"])
}

// TestWebhookProvider_PermanentFailure_4xx: 4xx status → non-retryable
// failure result, ProviderCode reflects the response status.
func TestWebhookProvider_PermanentFailure_4xx(t *testing.T) {
	srv := newWebhookServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"recipient blacklisted"}`))
	})
	defer srv.Close()

	p := provider.NewWebhookProvider(srv.URL, 2*time.Second)

	got := p.Send(context.Background(), domain.ChannelSMS, "+9", "x")
	require.False(t, got.Success)
	require.False(t, got.Retryable, "4xx must not be retryable")
	require.Equal(t, 400, got.ProviderCode)
	require.NotEmpty(t, got.Reason)
}

// TestWebhookProvider_TransientFailure_5xx: 5xx status → retryable failure
// with the response code preserved.
func TestWebhookProvider_TransientFailure_5xx(t *testing.T) {
	srv := newWebhookServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	defer srv.Close()

	p := provider.NewWebhookProvider(srv.URL, 2*time.Second)

	got := p.Send(context.Background(), domain.ChannelSMS, "+9", "x")
	require.False(t, got.Success)
	require.True(t, got.Retryable, "5xx must be retryable")
	require.Equal(t, 503, got.ProviderCode)
}

// TestWebhookProvider_Timeout: a server that never responds within the
// configured timeout yields a transient failure with ProviderCode=0.
func TestWebhookProvider_Timeout(t *testing.T) {
	srv := newWebhookServer(t, func(_ http.ResponseWriter, r *http.Request) {
		// Block until the client gives up.
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	defer srv.Close()

	p := provider.NewWebhookProvider(srv.URL, 100*time.Millisecond)

	start := time.Now()
	got := p.Send(context.Background(), domain.ChannelSMS, "+9", "x")
	elapsed := time.Since(start)

	require.False(t, got.Success)
	require.True(t, got.Retryable, "timeout is a transient failure")
	require.Equal(t, 0, got.ProviderCode, "no HTTP response → code 0")
	require.Less(t, elapsed, 2*time.Second, "must give up within the configured timeout")
}

// TestWebhookProvider_NetworkError: an unreachable URL yields a transient
// failure (no response, no provider code).
func TestWebhookProvider_NetworkError(t *testing.T) {
	// Reserved-for-documentation IP that should never resolve to a real host.
	p := provider.NewWebhookProvider("http://127.0.0.1:1/never-listens", 100*time.Millisecond)

	got := p.Send(context.Background(), domain.ChannelSMS, "+9", "x")
	require.False(t, got.Success)
	require.True(t, got.Retryable, "connection refused/timeout is transient")
	require.Equal(t, 0, got.ProviderCode)
}

// TestWebhookProvider_MalformedResponseBody: 2xx but unparseable JSON →
// still treated as success (the status code is the authoritative signal),
// but the MessageID falls back to empty.
func TestWebhookProvider_MalformedResponseBody(t *testing.T) {
	srv := newWebhookServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("this is not json"))
	})
	defer srv.Close()

	p := provider.NewWebhookProvider(srv.URL, 2*time.Second)

	got := p.Send(context.Background(), domain.ChannelSMS, "+9", "x")
	require.True(t, got.Success, "2xx is success regardless of body shape")
	require.Empty(t, got.MessageID, "no parseable message id → empty")
}
