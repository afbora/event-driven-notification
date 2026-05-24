// Package websocket holds the WebSocket adapter: a Hub that tracks
// per-notification subscriptions, and (in phase 4) the HTTP handler that
// upgrades incoming connections and feeds them into the Hub.
//
// The Hub is the consumer side of the Redis pub/sub status channel
// (ADR-0006). Workers publish via internal/adapters/redis.StatusBroadcaster;
// every API instance subscribes and calls Hub.Broadcast for each message;
// each Hub fans out to its locally-connected clients.
package websocket

import (
	"sync"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// StatusUpdate is the message shape pushed to each subscribed client. It
// mirrors the JSON payload that internal/adapters/redis.StatusBroadcaster
// publishes — the Redis consumer (phase 3E task 38) decodes and forwards
// it through the Hub.
type StatusUpdate struct {
	NotificationID string `json:"notification_id"`
	Status         string `json:"status"`
}

// Client is the minimal contract the Hub depends on. Production wiring
// (phase 4 HTTP handler) provides a Client that wraps a coder/websocket
// connection; tests pass an in-memory implementation.
type Client interface {
	// ID returns a unique identifier so the Hub can detect duplicate
	// subscriptions and route UnsubscribeAll calls.
	ID() string

	// Send pushes one StatusUpdate to the client. Returning an error is
	// allowed; the Hub logs and unsubscribes the client on failure.
	Send(msg StatusUpdate) error
}

// MetricsRecorder is the slim port the Hub uses to publish the
// active-client gauge. Defined here (not in internal/ports) because
// the Hub owns the contract it consumes — production wires
// *infrastructure/metrics.Metrics, tests pass a stub or nil.
type MetricsRecorder interface {
	SetWebSocketClients(count int)
}

// Hub tracks which clients want which notification ids and fans broadcasts
// out to them. The two maps are kept in lock-step: subscribers is the
// primary lookup for Broadcast; clientSubs is the reverse index used by
// UnsubscribeAll when a client disconnects.
type Hub struct {
	mu sync.RWMutex

	// notification id → (client id → client)
	subscribers map[domain.NotificationID]map[string]Client

	// client id → (notification id → present)
	clientSubs map[string]map[domain.NotificationID]struct{}

	// metrics is optional; nil skips emits.
	metrics MetricsRecorder
}

// NewHub returns an empty hub ready for Subscribe / Broadcast calls.
// No metrics emission — use NewHubWithMetrics to opt in.
func NewHub() *Hub {
	return &Hub{
		subscribers: make(map[domain.NotificationID]map[string]Client),
		clientSubs:  make(map[string]map[domain.NotificationID]struct{}),
	}
}

// NewHubWithMetrics returns a Hub that emits the active-client
// gauge to rec on every Subscribe / UnsubscribeAll transition.
// Pass nil to opt out — equivalent to NewHub().
func NewHubWithMetrics(rec MetricsRecorder) *Hub {
	h := NewHub()
	h.metrics = rec
	return h
}

// Subscribe registers interest in a notification id for the given client.
// Duplicate calls for the same (client, notification) pair are no-ops —
// the client does not get the message twice.
func (h *Hub) Subscribe(client Client, notificationID domain.NotificationID) {
	h.mu.Lock()
	clients, ok := h.subscribers[notificationID]
	if !ok {
		clients = make(map[string]Client)
		h.subscribers[notificationID] = clients
	}
	clients[client.ID()] = client

	subs, ok := h.clientSubs[client.ID()]
	if !ok {
		subs = make(map[domain.NotificationID]struct{})
		h.clientSubs[client.ID()] = subs
	}
	subs[notificationID] = struct{}{}
	count := len(h.clientSubs)
	h.mu.Unlock()

	if h.metrics != nil {
		h.metrics.SetWebSocketClients(count)
	}
}

// Unsubscribe removes one (client, notification) pair. Other subscriptions
// the client holds remain intact.
func (h *Hub) Unsubscribe(client Client, notificationID domain.NotificationID) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.removeLocked(client.ID(), notificationID)
}

// UnsubscribeAll drops every subscription this client holds. Called when
// the underlying WebSocket connection closes; prevents the subscribers
// map from leaking dead client references.
func (h *Hub) UnsubscribeAll(client Client) {
	h.mu.Lock()
	subs, ok := h.clientSubs[client.ID()]
	if !ok {
		h.mu.Unlock()
		return
	}
	for notifID := range subs {
		h.removeLocked(client.ID(), notifID)
	}
	count := len(h.clientSubs)
	h.mu.Unlock()

	if h.metrics != nil {
		h.metrics.SetWebSocketClients(count)
	}
}

// Broadcast pushes a StatusUpdate to every client subscribed to the
// notification id. A Send error from any individual client is treated as
// a disconnect signal: the client is dropped from every subscription it
// held. Broadcast itself never returns an error — the Hub is best-effort
// (the trace endpoint is the authoritative source of truth).
func (h *Hub) Broadcast(notificationID domain.NotificationID, status domain.Status) {
	msg := StatusUpdate{
		NotificationID: string(notificationID),
		Status:         string(status),
	}

	// Snapshot the recipients under the lock so the actual Send happens
	// outside it — a slow client cannot block other broadcasts.
	h.mu.RLock()
	clients := h.subscribers[notificationID]
	recipients := make([]Client, 0, len(clients))
	for _, c := range clients {
		recipients = append(recipients, c)
	}
	h.mu.RUnlock()

	for _, c := range recipients {
		if err := c.Send(msg); err != nil {
			h.UnsubscribeAll(c)
		}
	}
}

// SubscriberCount returns how many distinct clients are currently
// subscribed to the given notification id. Used by tests to confirm
// subscribe/unsubscribe lifecycle without poking at internals; cheap
// enough to keep available in production too (a read under RLock).
func (h *Hub) SubscriberCount(notificationID domain.NotificationID) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers[notificationID])
}

// removeLocked deletes one (clientID, notificationID) pair from both maps.
// The caller must already hold h.mu (the L variant — write lock).
func (h *Hub) removeLocked(clientID string, notificationID domain.NotificationID) {
	if clients, ok := h.subscribers[notificationID]; ok {
		delete(clients, clientID)
		if len(clients) == 0 {
			delete(h.subscribers, notificationID)
		}
	}
	if subs, ok := h.clientSubs[clientID]; ok {
		delete(subs, notificationID)
		if len(subs) == 0 {
			delete(h.clientSubs, clientID)
		}
	}
}
