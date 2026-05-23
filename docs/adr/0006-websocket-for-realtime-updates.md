# ADR-0006: WebSocket For Real-Time Status Updates

**Status:** Accepted
**Date:** 2026-05-23
**Deciders:** Ahmet Bora

## Context

The brief's bonus features section includes:

> **WebSocket Updates:** Real-time status updates via WebSocket

The wording is literal — "WebSocket" is named explicitly. The implementation question is twofold:

1. **What library?** The Go ecosystem has `gorilla/websocket` (popular, mature, in maintenance mode as of 2022), `coder/websocket` (formerly `nhooyr/websocket`, modern, context-native, simpler API), and a handful of less-used alternatives.
2. **How is fan-out coordinated?** A single API instance is easy: keep a map of subscriptions. A horizontally scaled deployment needs some kind of backplane.

The forces:

- The brief uses literal language; we should too. "WebSocket" means WebSocket, not Server-Sent Events.
- We already have Redis in the stack for the queue (ADR-0003), idempotency cache, and rate limiting. A pub/sub backplane is "free."
- We want clients to subscribe to specific notification IDs, not a firehose of every change in the system.

## Decision

Real-time status updates are delivered via **WebSocket** at the endpoint `GET /api/v1/ws/notifications`, using the **`coder/websocket`** library.

- Clients connect, then send `{"action": "subscribe", "notification_id": "<id>"}` per notification they care about.
- Each API instance maintains a local `Hub` mapping `notification_id → []*WebsocketClient`.
- Status changes published by the worker are written to a Redis pub/sub topic (`notification.status`).
- Every API instance subscribes to that topic; when a message arrives, it fans the update out to any local clients subscribed to the matching `notification_id`.

This means the WebSocket layer scales horizontally: any client can connect to any API instance, and any worker can publish updates; Redis carries the messages between them.

## Consequences

**Positive:**

- Satisfies the brief literally — "WebSocket Updates" → WebSocket.
- Bi-directional channel allows the subscribe/unsubscribe protocol without needing a separate REST endpoint.
- Horizontal scaling works out of the box because of the Redis pub/sub backplane.
- `coder/websocket` has a clean, context-native API and active maintenance.

**Negative:**

- WebSocket is heavier than Server-Sent Events for unidirectional pushes. We carry the cost (full-duplex framing, ping/pong keep-alive) even though the client mostly listens.
- Authentication is not implemented in the initial release — connections are open to any client. Production deployments would require a token-based handshake. This is documented as a future-work item in the README; the brief does not require auth.
- Redis pub/sub is fire-and-forget. If a notification status change happens while an API instance is between subscription cycles, that instance misses the message. We mitigate via the `notification_logs` table: the trace endpoint always shows the complete history; the WebSocket is a best-effort live feed, not the source of truth.

## Alternatives Considered

1. **Server-Sent Events (SSE)** — lighter, simpler, perfectly suited to one-way push. Rejected because the brief literally says "WebSocket." Choosing SSE would require justifying a deviation from the brief on a bonus feature; the cost of going with WebSocket is minimal.
2. **Long polling** — rejected as obsolete; client experience is poor, server resource usage is worse.
3. **Webhook callbacks to API consumers** — different use case. Webhooks satisfy "let me know when this notification's status changes" for backend consumers. WebSockets satisfy "show me a live status in the UI." We may add webhooks later (future-work in README); they do not replace WebSockets.
4. **`gorilla/websocket`** — solid library, but in maintenance mode and the API predates `context.Context` ergonomics. `coder/websocket` is the newer-generation choice.

## Related

- CLAUDE.md §2.5 (WebSocket interpretation), §6 (technology stack)
- ADR-0003 (Redis + asynq) — Redis is shared infrastructure
- `internal/adapters/websocket/` (Hub, pub/sub consumer)
- `internal/adapters/redis/status_broadcaster.go` (publisher side)
