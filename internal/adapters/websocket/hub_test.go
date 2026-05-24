package websocket_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/adapters/websocket"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// fakeClient is a minimal websocket.Client implementation for tests. It
// records every Send call in order so assertions can introspect the
// fan-out.
type fakeClient struct {
	id  string
	mu  sync.Mutex
	got []websocket.StatusUpdate
}

func newFakeClient(id string) *fakeClient {
	return &fakeClient{id: id}
}

func (c *fakeClient) ID() string { return c.id }

func (c *fakeClient) Send(msg websocket.StatusUpdate) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, msg)
	return nil
}

func (c *fakeClient) Received() []websocket.StatusUpdate {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]websocket.StatusUpdate, len(c.got))
	copy(out, c.got)
	return out
}

// TestHub_SubscribeReceives: a client that subscribes to a notification id
// receives the matching Broadcast message.
func TestHub_SubscribeReceives(t *testing.T) {
	hub := websocket.NewHub()
	client := newFakeClient("c1")

	notifID := domain.NotificationID("01940000-0000-7000-8000-000000000001")
	hub.Subscribe(client, notifID)

	hub.Broadcast(notifID, domain.StatusDelivered)

	got := client.Received()
	require.Len(t, got, 1)
	require.Equal(t, string(notifID), got[0].NotificationID)
	require.Equal(t, string(domain.StatusDelivered), got[0].Status)
}

// TestHub_UnsubscribeStopsDelivery: after Unsubscribe the client no longer
// receives broadcasts.
func TestHub_UnsubscribeStopsDelivery(t *testing.T) {
	hub := websocket.NewHub()
	client := newFakeClient("c1")

	notifID := domain.NotificationID("01940000-0000-7000-8000-000000000002")
	hub.Subscribe(client, notifID)
	hub.Unsubscribe(client, notifID)

	hub.Broadcast(notifID, domain.StatusDelivered)

	require.Empty(t, client.Received())
}

// TestHub_MultipleSubscribersFanOut: every subscribed client receives every
// broadcast — the WebSocket Hub's whole reason for being.
func TestHub_MultipleSubscribersFanOut(t *testing.T) {
	hub := websocket.NewHub()

	c1 := newFakeClient("c1")
	c2 := newFakeClient("c2")
	c3 := newFakeClient("c3")

	notifID := domain.NotificationID("01940000-0000-7000-8000-000000000003")
	hub.Subscribe(c1, notifID)
	hub.Subscribe(c2, notifID)
	hub.Subscribe(c3, notifID)

	hub.Broadcast(notifID, domain.StatusProcessing)

	require.Len(t, c1.Received(), 1)
	require.Len(t, c2.Received(), 1)
	require.Len(t, c3.Received(), 1)
}

// TestHub_PerNotificationIsolation: clients only receive broadcasts for
// the notifications they subscribed to.
func TestHub_PerNotificationIsolation(t *testing.T) {
	hub := websocket.NewHub()

	c1 := newFakeClient("c1")
	c2 := newFakeClient("c2")

	notif1 := domain.NotificationID("01940000-0000-7000-8000-000000000010")
	notif2 := domain.NotificationID("01940000-0000-7000-8000-000000000011")

	hub.Subscribe(c1, notif1)
	hub.Subscribe(c2, notif2)

	hub.Broadcast(notif1, domain.StatusDelivered)

	require.Len(t, c1.Received(), 1)
	require.Empty(t, c2.Received(), "c2 must not receive notif1's broadcast")
}

// TestHub_UnsubscribeAllOnDisconnect: when a client disconnects the Hub
// drops every subscription it held. Cheap proof that there is no leak in
// the subscribers map.
func TestHub_UnsubscribeAllOnDisconnect(t *testing.T) {
	hub := websocket.NewHub()

	c1 := newFakeClient("c1")

	notif1 := domain.NotificationID("01940000-0000-7000-8000-000000000020")
	notif2 := domain.NotificationID("01940000-0000-7000-8000-000000000021")
	hub.Subscribe(c1, notif1)
	hub.Subscribe(c1, notif2)

	hub.UnsubscribeAll(c1)

	hub.Broadcast(notif1, domain.StatusDelivered)
	hub.Broadcast(notif2, domain.StatusFailed)

	require.Empty(t, c1.Received(), "fully unsubscribed client must not receive any broadcasts")
}

// TestHub_BroadcastWithNoSubscribers: broadcasting to a notification with
// zero subscribers is a no-op — no panic, no error.
func TestHub_BroadcastWithNoSubscribers(t *testing.T) {
	hub := websocket.NewHub()

	require.NotPanics(t, func() {
		hub.Broadcast(domain.NotificationID("01940000-0000-7000-8000-000000000099"), domain.StatusDelivered)
	})
}

// TestHub_DuplicateSubscribeIdempotent: subscribing the same client to the
// same notification twice does not produce duplicate messages.
func TestHub_DuplicateSubscribeIdempotent(t *testing.T) {
	hub := websocket.NewHub()
	client := newFakeClient("c1")

	notifID := domain.NotificationID("01940000-0000-7000-8000-000000000030")
	hub.Subscribe(client, notifID)
	hub.Subscribe(client, notifID) // second subscription is a no-op

	hub.Broadcast(notifID, domain.StatusDelivered)

	require.Len(t, client.Received(), 1, "duplicate subscribe must not duplicate delivery")
}
