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
	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
	wsadapter "github.com/afbora/event-driven-notification/internal/adapters/websocket"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// buildWSTestServer returns an httptest.Server that mounts the
// WebSocket handler at /api/v1/ws/notifications. The shared *Hub is
// returned so the test can drive broadcasts directly without going
// through Redis pub/sub.
func buildWSTestServer(t *testing.T) (*httptest.Server, *wsadapter.Hub) {
	t.Helper()
	hub := wsadapter.NewHub()
	handler := httpadapter.NewWebSocketHandler(hub)

	mux := nethttp.NewServeMux()
	mux.Handle("/api/v1/ws/notifications", handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, hub
}

// wsURL converts the httptest.NewServer URL (http://…) into the ws://
// scheme required by the WebSocket client. The coder/websocket library
// also accepts http:// directly but ws:// is what real clients send,
// so we mirror that here.
func wsURL(srvURL string) string {
	return "ws" + strings.TrimPrefix(srvURL, "http") + "/api/v1/ws/notifications"
}

// TestWebSocket_SubscribeAndReceive: the canonical happy path. A
// client connects, subscribes to a notification id, the Hub
// broadcasts a status update, and the client receives the encoded
// StatusUpdate payload.
func TestWebSocket_SubscribeAndReceive(t *testing.T) {
	srv, hub := buildWSTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL(srv.URL), nil)
	require.NoError(t, err, "ws dial")
	defer func() { _ = conn.CloseNow() }()

	notifID := domain.NotificationID("01940000-0000-7000-8000-0000000000ws")
	require.NoError(t, wsjson.Write(ctx, conn, map[string]string{
		"action":          "subscribe",
		"notification_id": string(notifID),
	}))

	// Give the handler a moment to register the subscription before
	// broadcasting. The handler reads the subscribe message in its
	// goroutine; without yielding, the broadcast could race past it.
	require.Eventually(t, func() bool {
		return hub.SubscriberCount(notifID) > 0
	}, 1*time.Second, 5*time.Millisecond, "subscription never registered")

	hub.Broadcast(notifID, domain.StatusDelivered)

	var got wsadapter.StatusUpdate
	require.NoError(t, wsjson.Read(ctx, conn, &got))
	require.Equal(t, string(notifID), got.NotificationID)
	require.Equal(t, string(domain.StatusDelivered), got.Status)
}

// TestWebSocket_UnsubscribeStopsDelivery: after the client sends an
// unsubscribe message, broadcasts to the same id no longer reach it.
func TestWebSocket_UnsubscribeStopsDelivery(t *testing.T) {
	srv, hub := buildWSTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL(srv.URL), nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	notifID := domain.NotificationID("01940000-0000-7000-8000-0000000000wb")
	require.NoError(t, wsjson.Write(ctx, conn, map[string]string{
		"action": "subscribe", "notification_id": string(notifID),
	}))
	require.Eventually(t, func() bool { return hub.SubscriberCount(notifID) > 0 },
		1*time.Second, 5*time.Millisecond)

	require.NoError(t, wsjson.Write(ctx, conn, map[string]string{
		"action": "unsubscribe", "notification_id": string(notifID),
	}))
	require.Eventually(t, func() bool { return hub.SubscriberCount(notifID) == 0 },
		1*time.Second, 5*time.Millisecond, "unsubscribe never took effect")
}

// TestWebSocket_DisconnectCleansUpAllSubscriptions: when the client
// closes the connection (or simply walks away) every subscription it
// held is dropped from the Hub. This is the leak-prevention contract.
func TestWebSocket_DisconnectCleansUpAllSubscriptions(t *testing.T) {
	srv, hub := buildWSTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL(srv.URL), nil)
	require.NoError(t, err)

	id1 := domain.NotificationID("01940000-0000-7000-8000-000000000c01")
	id2 := domain.NotificationID("01940000-0000-7000-8000-000000000c02")
	require.NoError(t, wsjson.Write(ctx, conn, map[string]string{
		"action": "subscribe", "notification_id": string(id1),
	}))
	require.NoError(t, wsjson.Write(ctx, conn, map[string]string{
		"action": "subscribe", "notification_id": string(id2),
	}))
	require.Eventually(t, func() bool {
		return hub.SubscriberCount(id1) == 1 && hub.SubscriberCount(id2) == 1
	}, 1*time.Second, 5*time.Millisecond)

	require.NoError(t, conn.Close(websocket.StatusNormalClosure, "bye"))

	require.Eventually(t, func() bool {
		return hub.SubscriberCount(id1) == 0 && hub.SubscriberCount(id2) == 0
	}, 1*time.Second, 5*time.Millisecond, "subscriptions leaked after client disconnect")
}

// TestWebSocket_IgnoresMalformedMessage: a non-JSON payload from the
// client must NOT bring down the handler. The connection stays open,
// the next valid message is processed.
func TestWebSocket_IgnoresMalformedMessage(t *testing.T) {
	srv, hub := buildWSTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL(srv.URL), nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	// Garbage payload — handler should swallow and keep reading.
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte("not json")))

	notifID := domain.NotificationID("01940000-0000-7000-8000-0000000000bd")
	require.NoError(t, wsjson.Write(ctx, conn, map[string]string{
		"action": "subscribe", "notification_id": string(notifID),
	}))
	require.Eventually(t, func() bool { return hub.SubscriberCount(notifID) > 0 },
		1*time.Second, 5*time.Millisecond,
		"handler must survive garbage and still process the next valid message")
}
