package http_test

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
	wsadapter "github.com/afbora/event-driven-notification/internal/adapters/websocket"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/infrastructure/metrics"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// buildWSTestServerWithMiddleware mirrors buildWSTestServer in
// websocket_handler_test.go but wraps the WebSocket handler in the
// SAME middleware chain the production cmd/api wiring uses
// (MetricsMiddleware + IdempotencyMiddleware). This is the chain that
// was silently breaking WebSocket upgrades in `docker compose up`
// before the Hijack/Flush fix landed on the wrapper types — bypassing
// the middleware (as the original WebSocket tests did) hid the bug
// from CI.
//
// Idempotency requires a ports.IdempotencyStore; the WebSocket request
// never carries an Idempotency-Key header so the middleware's first
// branch short-circuits and never touches the store. A no-op store
// keeps the test infra trivial.
func buildWSTestServerWithMiddleware(t *testing.T) (*httptest.Server, *wsadapter.Hub) {
	t.Helper()

	hub := wsadapter.NewHub()
	wsHandler := httpadapter.NewWebSocketHandler(hub)

	// Production-shaped middleware chain. Order mirrors cmd/api/main.go:
	// MetricsMiddleware wraps first (outermost) so it sees the final
	// status code; IdempotencyMiddleware wraps inner so it can short-
	// circuit on cache hit before metrics observation completes.
	m := metrics.New(prometheus.NewRegistry())
	wrapped := httpadapter.MetricsMiddleware(m)(
		httpadapter.IdempotencyMiddleware(noopIdempotencyStore{})(wsHandler),
	)

	mux := nethttp.NewServeMux()
	mux.Handle("/api/v1/ws/notifications", wrapped)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, hub
}

// noopIdempotencyStore satisfies ports.IdempotencyStore but never
// returns a cache hit and never errors. Sufficient for this test
// because the WebSocket request has no Idempotency-Key header — the
// middleware short-circuits before touching the store.
type noopIdempotencyStore struct{}

func (noopIdempotencyStore) Get(_ context.Context, _ string) (ports.IdempotencyEntry, bool, error) {
	return ports.IdempotencyEntry{}, false, nil
}
func (noopIdempotencyStore) Set(_ context.Context, _ string, _ ports.IdempotencyEntry, _ time.Duration) error {
	return nil
}

// TestWebSocket_UpgradeSucceedsThroughMiddlewareStack is the
// regression test for the bug that triggered the Hijack/Flush fix.
// Without Hijack on the MetricsMiddleware's statusTracker (or
// IdempotencyMiddleware's capturingWriter) the upgrade fails with
// "http.ResponseWriter does not implement http.Hijacker" — the test
// would fail with a dial error before any subscribe round-trip.
//
// The subscribe + broadcast round-trip downstream is included so the
// test exercises real bidirectional traffic through the wrapped
// writers, not just the upgrade handshake.
func TestWebSocket_UpgradeSucceedsThroughMiddlewareStack(t *testing.T) {
	srv, hub := buildWSTestServerWithMiddleware(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws/notifications"

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err, "websocket upgrade through full middleware stack")
	defer func() { _ = conn.CloseNow() }()

	notifID := domain.NotificationID("01940000-0000-7000-8000-0000000000mw")
	require.NoError(t, wsjson.Write(ctx, conn, map[string]string{
		"action":          "subscribe",
		"notification_id": string(notifID),
	}))

	require.Eventually(t, func() bool {
		return hub.SubscriberCount(notifID) > 0
	}, 1*time.Second, 5*time.Millisecond, "subscription never registered through middleware stack")

	hub.Broadcast(notifID, domain.StatusDelivered)

	var got wsadapter.StatusUpdate
	require.NoError(t, wsjson.Read(ctx, conn, &got))
	require.Equal(t, string(notifID), got.NotificationID)
	require.Equal(t, string(domain.StatusDelivered), got.Status)
}
