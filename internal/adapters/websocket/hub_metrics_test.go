package websocket_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/adapters/websocket"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// stubMetricsRecorder is a minimal websocket.MetricsRecorder
// implementation that records every gauge value the Hub stamps.
type stubMetricsRecorder struct {
	values []int
}

func (s *stubMetricsRecorder) SetWebSocketClients(count int) {
	s.values = append(s.values, count)
}

// TestHub_SubscribeStampsClientGauge: every distinct client that
// subscribes bumps the gauge to the current unique count. Repeat
// subscriptions from the same client must NOT inflate the count.
func TestHub_SubscribeStampsClientGauge(t *testing.T) {
	rec := &stubMetricsRecorder{}
	hub := websocket.NewHubWithMetrics(rec)

	c1 := newFakeClient("c1")
	c2 := newFakeClient("c2")
	notif := domain.NotificationID("01940000-0000-7000-8000-000000000001")

	hub.Subscribe(c1, notif)
	hub.Subscribe(c2, notif)
	hub.Subscribe(c1, notif) // duplicate — same client, same id

	require.Equal(t, []int{1, 2, 2}, rec.values,
		"gauge must reflect unique client count: 1 after c1, 2 after c2, still 2 after c1's duplicate subscribe")
}

// TestHub_UnsubscribeAllStampsClientGauge: when a client drops every
// subscription it held, the gauge falls by one. Other clients are
// unaffected.
func TestHub_UnsubscribeAllStampsClientGauge(t *testing.T) {
	rec := &stubMetricsRecorder{}
	hub := websocket.NewHubWithMetrics(rec)

	c1 := newFakeClient("c1")
	c2 := newFakeClient("c2")
	a := domain.NotificationID("01940000-0000-7000-8000-000000000010")
	b := domain.NotificationID("01940000-0000-7000-8000-000000000011")

	hub.Subscribe(c1, a)
	hub.Subscribe(c1, b)
	hub.Subscribe(c2, a)
	// gauge values so far: 1, 1, 2

	hub.UnsubscribeAll(c1)
	// expect: gauge → 1

	require.Equal(t, 1, rec.values[len(rec.values)-1],
		"after c1 drops all subs the unique client count is 1; got %v", rec.values)
}

// TestHub_UnsubscribePartialDoesNotChangeClientCount: a single
// Unsubscribe (one of several the client holds) leaves the client
// connected — the gauge does NOT decrement.
func TestHub_UnsubscribePartialDoesNotChangeClientCount(t *testing.T) {
	rec := &stubMetricsRecorder{}
	hub := websocket.NewHubWithMetrics(rec)

	c1 := newFakeClient("c1")
	a := domain.NotificationID("01940000-0000-7000-8000-000000000020")
	b := domain.NotificationID("01940000-0000-7000-8000-000000000021")

	hub.Subscribe(c1, a)
	hub.Subscribe(c1, b)
	require.Equal(t, []int{1, 1}, rec.values)

	hub.Unsubscribe(c1, a)
	// c1 still subscribed to b — gauge stays at 1.
	require.Equal(t, 1, rec.values[len(rec.values)-1])
}

// TestHub_NewHubWithoutMetrics_NilSafe: the existing NewHub
// constructor must keep working — passing no recorder is the
// legitimate test/development path.
func TestHub_NewHubWithoutMetrics_NilSafe(t *testing.T) {
	hub := websocket.NewHub()
	c1 := newFakeClient("c1")
	notif := domain.NotificationID("01940000-0000-7000-8000-000000000030")

	require.NotPanics(t, func() {
		hub.Subscribe(c1, notif)
		hub.UnsubscribeAll(c1)
	})
}
