// Package ports defines the interfaces (ports, in hexagonal architecture
// vocabulary) that the application layer uses to talk to the outside world.
// Concrete implementations live under internal/adapters/.
//
// Domain and application packages depend only on the interfaces declared
// here, never on a specific adapter. See ADR-0001 for the broader rationale
// and CLAUDE.md §3.3 for the import boundary that this package helps enforce.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// Port-level sentinel errors. Adapter implementations return these (often
// wrapped with adapter-specific context) so use-case code can branch on
// them via errors.Is without knowing which adapter is plugged in.
var (
	ErrNotFound         = errors.New("not found")
	ErrAlreadyClaimed   = errors.New("notification already claimed by another worker")
	ErrConcurrentUpdate = errors.New("concurrent update detected")
)

// --- Persistence ----------------------------------------------------------

// NotificationRepository is the storage port for Notification entities. The
// production implementation lives in internal/adapters/postgres (ADR-0002,
// sqlc-generated queries).
type NotificationRepository interface {
	// Create persists a new notification in the pending state.
	Create(ctx context.Context, n *domain.Notification) error

	// Get retrieves a notification by id. Returns ErrNotFound (possibly
	// wrapped) when the row does not exist; callers detect with errors.Is.
	Get(ctx context.Context, id domain.NotificationID) (*domain.Notification, error)

	// ClaimForProcessing atomically moves a notification from queued or
	// retrying into processing via the SQL pattern defined in CLAUDE.md §3.10
	// and ADR-0009. Returns the claimed notification on success, or
	// ErrAlreadyClaimed when another worker beat us to it.
	ClaimForProcessing(ctx context.Context, id domain.NotificationID, now time.Time) (*domain.Notification, error)

	// UpdateStatus persists a status transition the caller has already
	// validated via the entity's mark methods. The repository checks the
	// expected source status in its WHERE clause for safety; a mismatch
	// returns ErrConcurrentUpdate.
	UpdateStatus(ctx context.Context, n *domain.Notification, expectedSource domain.Status) error

	// List returns notifications matching filter with cursor pagination.
	// Returns the page and the cursor to use for the next page (empty when
	// no more pages).
	List(ctx context.Context, filter NotificationFilter, cursor string, limit int) ([]*domain.Notification, string, error)

	// Reconciler queries — used by cmd/reconciler (CLAUDE.md §3.11). Each
	// uses SELECT ... FOR UPDATE SKIP LOCKED so multiple reconciler
	// instances can run in parallel without conflicting claims.
	FindOrphanedPending(ctx context.Context, olderThan time.Time, limit int) ([]*domain.Notification, error)
	FindStuckProcessing(ctx context.Context, olderThan time.Time, limit int) ([]*domain.Notification, error)
	FindOverdueRetrying(ctx context.Context, before time.Time, limit int) ([]*domain.Notification, error)
}

// NotificationFilter is the parameter bundle for NotificationRepository.List.
// All fields are optional; the empty filter returns every notification.
type NotificationFilter struct {
	Status        *domain.Status
	Channel       *domain.Channel
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	BatchID       *domain.BatchID
}

// BatchRepository is the storage port for Batch entities.
type BatchRepository interface {
	// Create persists a new batch and every notification inside it.
	Create(ctx context.Context, b *domain.Batch) error

	// Get retrieves a batch with all its notifications.
	Get(ctx context.Context, id domain.BatchID) (*domain.Batch, error)
}

// TemplateRepository is the storage port for Template entities. Templates
// are looked up by id (UUID) or by Name when callers refer to a stable
// human-readable handle.
type TemplateRepository interface {
	Create(ctx context.Context, t *domain.Template) error
	Get(ctx context.Context, id domain.TemplateID) (*domain.Template, error)
	GetByName(ctx context.Context, name string) (*domain.Template, error)
	List(ctx context.Context, limit int) ([]*domain.Template, error)
	Update(ctx context.Context, t *domain.Template) error
	Delete(ctx context.Context, id domain.TemplateID) error
}

// NotificationLogRepository is the storage port for the per-notification
// audit trail behind the trace endpoint (CLAUDE.md §12.3).
type NotificationLogRepository interface {
	// Append writes one row.
	Append(ctx context.Context, entry *domain.NotificationLog) error

	// List returns every log row for a notification in chronological order.
	List(ctx context.Context, notificationID domain.NotificationID) ([]*domain.NotificationLog, error)
}

// --- Asynchronous processing ----------------------------------------------

// Queue is the async task dispatcher. Concrete implementation: asynq
// (ADR-0003). The Queue port covers enqueueing tasks; consuming them happens
// inside cmd/worker which wires asynq's Server directly because the
// consumer-side semantics are framework-specific.
type Queue interface {
	// Enqueue schedules a notification for processing on its priority queue.
	// The optional unique key (idempotencyKey) prevents duplicate enqueues
	// for 24 hours when set.
	Enqueue(ctx context.Context, notificationID domain.NotificationID, priority domain.Priority, idempotencyKey string) error

	// EnqueueScheduled defers the notification until at; the worker may not
	// dequeue it before that time.
	EnqueueScheduled(ctx context.Context, notificationID domain.NotificationID, priority domain.Priority, at time.Time) error

	// Cancel removes a notification's pending task from the queue. Best
	// effort: if the task has already been picked up by a worker, Cancel
	// returns nil and the worker is expected to check status before sending.
	Cancel(ctx context.Context, notificationID domain.NotificationID) error
}

// --- External delivery ----------------------------------------------------

// Provider is the strategy interface for sending a message through a
// specific channel (SMS, Email, Push, or future channels). Concrete
// implementations live under internal/adapters/provider/. See ADR-0004 for
// the strategy-pattern rationale.
type Provider interface {
	// Send dispatches the message and returns a DeliveryResult that the
	// worker uses to decide retry vs terminal status.
	Send(ctx context.Context, channel domain.Channel, recipient, content string) domain.DeliveryResult
}

// --- Idempotency, rate limiting, fan-out ----------------------------------

// IdempotencyEntry is the round-trip shape the store persists per key
// (CLAUDE.md §3.9). Body and ContentType replay the original response;
// RequestHash records the SHA-256 of the original request body so a
// later POST with the same key but a divergent payload is rejected as
// 409 Conflict instead of silently replaying — that prevents the
// "same key, two intents" client bug from being masked.
//
// RequestHash MAY be empty for entries written by older deployments
// that did not record it; the middleware treats an empty stored hash
// as a legacy entry and falls back to the pre-fingerprint replay
// behavior (see internal/adapters/http/idempotency.go).
type IdempotencyEntry struct {
	Body        []byte
	ContentType string
	RequestHash []byte
}

// IdempotencyStore caches API responses keyed by the client-provided
// Idempotency-Key header (CLAUDE.md §3.9). Redis-backed in production.
type IdempotencyStore interface {
	// Get returns the cached entry, or found=false when no entry exists.
	Get(ctx context.Context, key string) (entry IdempotencyEntry, found bool, err error)

	// Set stores the entry with a caller-chosen TTL (default 24h).
	Set(ctx context.Context, key string, entry IdempotencyEntry, ttl time.Duration) error
}

// RateLimiter is the two-flavor limiter described in CLAUDE.md §2.6: inbound
// (per client IP, applied at the HTTP middleware) and outbound (per channel,
// applied at the worker). Bucket is the namespacing key — e.g. "ip:1.2.3.4"
// or "channel:sms" — so a single Redis-backed implementation serves both
// use cases without conflating their keyspaces.
type RateLimiter interface {
	// Allow consumes one token and reports whether the request is permitted.
	// retryAfter is meaningful only when allowed == false; it tells the
	// caller how long to wait before retrying.
	Allow(ctx context.Context, bucket string) (allowed bool, retryAfter time.Duration, err error)
}

// StatusBroadcaster publishes notification status updates to the WebSocket
// hub via Redis pub/sub (ADR-0006). Adapter implementations are responsible
// for fan-out to local subscribers; this port only covers the publish side.
type StatusBroadcaster interface {
	Publish(ctx context.Context, notificationID domain.NotificationID, status domain.Status) error
}

// --- Identifiers & time ---------------------------------------------------

// IDGenerator produces unique identifiers for new entities. Production wires
// a UUID v7 implementation (lexicographically sortable, friendly to B-tree
// indexes); tests inject a deterministic stub for stable assertions.
//
// IDGenerator lives in ports because the domain refuses to take a UUID
// dependency (CLAUDE.md §3.3 — stdlib only).
type IDGenerator interface {
	NewNotificationID() domain.NotificationID
	NewBatchID() domain.BatchID
	NewTemplateID() domain.TemplateID
	NewLogID() domain.LogID

	// NewCorrelationID is also generated server-side when the inbound
	// request omits the X-Correlation-ID header.
	NewCorrelationID() string
}

// Clock is injected into application code instead of calling time.Now
// directly (CLAUDE.md §3.6). Production wires the real clock; tests use a
// frozen clock for deterministic timestamps.
type Clock interface {
	Now() time.Time
}
