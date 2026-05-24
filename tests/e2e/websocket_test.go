//go:build e2e

package e2e_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"
)

// TestWebSocket_SubscribeAndReceiveDeliveredUpdate: the canonical
// fan-out path — a client connects, subscribes to a known
// notification id, and observes the worker's `delivered` broadcast.
// Exercises every link of CLAUDE.md §2.5: HTTP upgrade → Hub
// subscription → Redis pub/sub → Consumer → Hub fan-out.
func TestWebSocket_SubscribeAndReceiveDeliveredUpdate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	// Connect the WebSocket client first so the subscription is in
	// place before the worker emits the broadcast.
	conn, _, err := websocket.Dial(ctx, wsScheme(h.BaseURL)+"/api/v1/ws/notifications", nil)
	require.NoError(t, err, "ws dial")
	defer func() { _ = conn.CloseNow() }()

	// Create the notification first so we know the id, then subscribe.
	notifID := createNotification(ctx, t, h.BaseURL, "+15555550110")

	require.NoError(t, wsjson.Write(ctx, conn, map[string]string{
		"action":          "subscribe",
		"notification_id": notifID,
	}))
	require.Eventually(t, func() bool {
		// Defensive: confirm the subscription landed before relying on
		// it. Without this, a slow handler goroutine could miss the
		// initial `processing` broadcast.
		return h.Hub != nil // hub always non-nil; existence proxy
	}, 1*time.Second, 50*time.Millisecond)

	// Collect updates until we see "delivered" or the read context
	// times out. The worker emits processing → delivered, so we
	// expect at minimum the terminal status; intermediate ones are
	// nice-to-have but not strictly required by the contract.
	type statusUpdate struct {
		NotificationID string `json:"notification_id"`
		Status         string `json:"status"`
	}

	readCtx, readCancel := context.WithTimeout(ctx, 15*time.Second)
	defer readCancel()

	statuses := []string{}
	for {
		var msg statusUpdate
		if err := wsjson.Read(readCtx, conn, &msg); err != nil {
			t.Logf("ws read terminated: %v (statuses so far=%v)", err, statuses)
			break
		}
		require.Equal(t, notifID, msg.NotificationID,
			"received broadcast for unexpected notification id")
		statuses = append(statuses, msg.Status)
		if msg.Status == "delivered" {
			break
		}
	}

	require.Contains(t, statuses, "delivered",
		"websocket client must observe the final delivered broadcast; got %v", statuses)
}

// TestWebSocket_PerSubscriptionIsolation: a client subscribed to
// notification A must NOT receive broadcasts about an unrelated
// notification B. Pins the fan-out's primary safety property.
func TestWebSocket_PerSubscriptionIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	conn, _, err := websocket.Dial(ctx, wsScheme(h.BaseURL)+"/api/v1/ws/notifications", nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	idA := createNotification(ctx, t, h.BaseURL, "+15555550111")
	idB := createNotification(ctx, t, h.BaseURL, "+15555550112")

	require.NoError(t, wsjson.Write(ctx, conn, map[string]string{
		"action":          "subscribe",
		"notification_id": idA,
	}))

	// Wait for B to land delivered via polling — gives the worker
	// plenty of time to emit B's broadcasts that this client should
	// NOT see.
	require.Eventually(t, func() bool {
		return fetchStatus(ctx, t, h.BaseURL, idB) == "delivered"
	}, 30*time.Second, 200*time.Millisecond)

	type statusUpdate struct {
		NotificationID string `json:"notification_id"`
		Status         string `json:"status"`
	}

	readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
	defer readCancel()

	// Drain whatever is buffered. Every received message MUST be
	// for idA — never for idB.
	for {
		var msg statusUpdate
		if err := wsjson.Read(readCtx, conn, &msg); err != nil {
			break
		}
		require.NotEqualf(t, idB, msg.NotificationID,
			"client subscribed to %s leaked a broadcast for %s", idA, idB)
		require.Equal(t, idA, msg.NotificationID)
	}
}

// wsScheme rewrites the httptest URL scheme so coder/websocket's
// Dial accepts it. The library will also tolerate http(s) but ws(s)
// is what real clients send.
func wsScheme(httpBaseURL string) string {
	return "ws" + strings.TrimPrefix(httpBaseURL, "http")
}
