package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	nethttp "net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	wsadapter "github.com/afbora/event-driven-notification/internal/adapters/websocket"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// subscribeAction names the action verbs the client sends. The
// protocol is deliberately minimal — anything more elaborate (auth,
// presence, channel filtering) belongs in a follow-up that ships with
// its own ADR.
const (
	subscribeAction   = "subscribe"
	unsubscribeAction = "unsubscribe"
)

// clientMessage is the on-wire shape clients send to subscribe to or
// unsubscribe from a notification id (CLAUDE.md §2.5). Unknown
// actions are logged and ignored — survivability over strictness.
type clientMessage struct {
	Action         string `json:"action"`
	NotificationID string `json:"notification_id"`
}

// WebSocketHandler upgrades incoming HTTP requests to WebSocket
// connections and runs the subscribe/unsubscribe protocol against the
// shared Hub. Production wiring (cmd/api) mounts this handler at
// `GET /api/v1/ws/notifications`; the openapi spec deliberately does
// NOT model this endpoint because OpenAPI 3.0 has no native WebSocket
// description.
type WebSocketHandler struct {
	hub *wsadapter.Hub
}

// NewWebSocketHandler wires the handler to the shared Hub.
func NewWebSocketHandler(hub *wsadapter.Hub) *WebSocketHandler {
	return &WebSocketHandler{hub: hub}
}

// ServeHTTP performs the upgrade and runs the client's read loop until
// the connection closes. UnsubscribeAll is deferred so the Hub never
// leaks references to dead connections.
//
// AcceptOptions: OriginPatterns="*" — CORS validation belongs in a
// dedicated middleware layer at the edge, not in the WebSocket
// handshake itself (mixing the two has bitten other projects).
func (h *WebSocketHandler) ServeHTTP(w nethttp.ResponseWriter, r *nethttp.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		slog.WarnContext(r.Context(), "websocket upgrade failed",
			"error", err.Error())
		return
	}

	client := newConnClient(conn)
	defer func() {
		h.hub.UnsubscribeAll(client)
		_ = conn.CloseNow()
	}()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_, payload, readErr := conn.Read(ctx)
		if readErr != nil {
			// Normal close, going away, or context cancellation —
			// these are expected control-flow paths, not failures.
			var closeErr websocket.CloseError
			if errors.As(readErr, &closeErr) || errors.Is(readErr, context.Canceled) {
				return
			}
			slog.WarnContext(ctx, "websocket read error",
				"client_id", client.ID(),
				"error", readErr.Error())
			return
		}

		var msg clientMessage
		if jerr := json.Unmarshal(payload, &msg); jerr != nil {
			// Malformed message: log, ignore, keep the connection
			// alive. A single bad frame must not knock the client
			// out of the broadcast stream.
			slog.DebugContext(ctx, "websocket client sent malformed message",
				"client_id", client.ID(),
				"error", jerr.Error())
			continue
		}
		h.dispatch(ctx, client, msg)
	}
}

// dispatch routes a parsed client message to the Hub. Unknown actions
// are logged and dropped so the protocol can grow forward-compatibly.
func (h *WebSocketHandler) dispatch(ctx context.Context, client wsadapter.Client, msg clientMessage) {
	if msg.NotificationID == "" {
		slog.DebugContext(ctx, "websocket subscribe missing notification id",
			"client_id", client.ID(), "action", msg.Action)
		return
	}
	notifID := domain.NotificationID(msg.NotificationID)

	switch msg.Action {
	case subscribeAction:
		h.hub.Subscribe(client, notifID)
	case unsubscribeAction:
		h.hub.Unsubscribe(client, notifID)
	default:
		slog.DebugContext(ctx, "websocket unknown action ignored",
			"client_id", client.ID(), "action", msg.Action)
	}
}

// connClient wraps a *websocket.Conn so it satisfies the
// wsadapter.Client contract. The id is generated locally because the
// WebSocket protocol does not carry one — we only need uniqueness
// within this process's Hub.
type connClient struct {
	id   string
	conn *websocket.Conn

	closed atomic.Bool
	mu     sync.Mutex // serializes writes; conn.Write is not safe for concurrent use
}

func newConnClient(conn *websocket.Conn) *connClient {
	return &connClient{
		id:   "ws-" + uuid.New().String(),
		conn: conn,
	}
}

func (c *connClient) ID() string { return c.id }

// Send marshals msg as JSON and writes one text frame. Returns an
// error if the connection has been closed or the write fails — the
// Hub treats either as a disconnect and drops the client.
func (c *connClient) Send(msg wsadapter.StatusUpdate) error {
	if c.closed.Load() {
		return errors.New("websocket client already closed")
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal status update: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// 5s write deadline is policy: a stuck client must not park a
	// broadcast goroutine forever. context.Background is intentional
	// — the request context is bound to the read goroutine and will
	// outlive a single Send call when the broadcast comes from
	// another goroutine.
	ctx, cancel := context.WithTimeout(context.Background(), defaultWSWriteTimeout)
	defer cancel()

	if err := c.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		c.closed.Store(true)
		return fmt.Errorf("ws write: %w", err)
	}
	return nil
}

// defaultWSWriteTimeout caps every individual Send so one slow client
// cannot stall broadcasts to its peers. Five seconds is generous; a
// healthy client acks within milliseconds.
const defaultWSWriteTimeout = 5 * time.Second
