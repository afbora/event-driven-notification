// Package application holds the use cases that orchestrate domain entities
// via the ports interfaces. Each use case is a small struct: dependencies
// injected through the constructor, one Execute method that performs the
// orchestration. Use cases never reach for time.Now or for a concrete
// adapter — both are dependencies (CLAUDE.md §3.6, §3.3).
package application

import (
	"context"
	"fmt"
	"time"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// CreateNotificationInput is the payload accepted by CreateNotification.
// String forms of Channel and Priority are kept on the input side so HTTP
// handlers can pass the raw client value; parsing happens inside Execute.
//
// Template semantics: when TemplateID is non-nil and points at a known
// template, the use case renders the template body with TemplateVariables
// and the result REPLACES the inline Content field. Callers that supply
// both still get the rendered output — the template is the authoritative
// source when both are present.
type CreateNotificationInput struct {
	Channel           string
	Priority          string
	Recipient         string
	Content           string
	IdempotencyKey    string
	CorrelationID     string // optional — generated server-side when empty
	ScheduledAt       *time.Time
	TemplateID        *string
	TemplateVariables map[string]any
}

// CreateNotification persists a new notification, writes the initial
// audit-log row, and enqueues it for processing. On success it returns the
// notification in its initial pending state with all server-generated fields
// (ID, CorrelationID, CreatedAt) populated.
type CreateNotification struct {
	repo     ports.NotificationRepository
	logRepo  ports.NotificationLogRepository
	tmplRepo ports.TemplateRepository
	queue    ports.Queue
	idGen    ports.IDGenerator
	clock    ports.Clock
	metrics  MetricsRecorder // optional; nil skips emit
}

// NewCreateNotification wires the dependencies. Every port is
// required except metricsRec — tests that do not care about
// observability pass nil.
func NewCreateNotification(
	repo ports.NotificationRepository,
	logRepo ports.NotificationLogRepository,
	tmplRepo ports.TemplateRepository,
	queue ports.Queue,
	idGen ports.IDGenerator,
	clock ports.Clock,
	metricsRec MetricsRecorder,
) *CreateNotification {
	return &CreateNotification{
		repo:     repo,
		logRepo:  logRepo,
		tmplRepo: tmplRepo,
		queue:    queue,
		idGen:    idGen,
		clock:    clock,
		metrics:  metricsRec,
	}
}

// Execute runs the use case. Failures from input parsing or domain
// construction return before any side effect, so the repository, log, and
// queue stay untouched on validation errors (the test enforces this).
//
// If repo.Create succeeds but log.Append or queue.Enqueue fails, Execute
// returns the error. The notification remains persisted in the pending
// state; the reconciler (ADR-0011) sweeps it back into circulation after
// the orphan threshold elapses. This trades latency on the rare failure
// path for simpler synchronous semantics here.
func (uc *CreateNotification) Execute(ctx context.Context, in CreateNotificationInput) (*domain.Notification, error) {
	channel, err := domain.NewChannel(in.Channel)
	if err != nil {
		return nil, err
	}
	priority, err := domain.NewPriority(in.Priority)
	if err != nil {
		return nil, err
	}

	correlationID := in.CorrelationID
	if correlationID == "" {
		correlationID = uc.idGen.NewCorrelationID()
	}

	now := uc.clock.Now()

	// When a template id is supplied, render its body with the
	// caller-supplied variables and use the result as content. The
	// inline Content field is overridden — see CreateNotificationInput
	// docs for the precedence rule.
	content := in.Content
	if in.TemplateID != nil && *in.TemplateID != "" {
		rendered, err := uc.renderTemplate(ctx, *in.TemplateID, in.TemplateVariables)
		if err != nil {
			return nil, err
		}
		content = rendered
	}

	n, err := domain.NewNotification(domain.NewNotificationInput{
		ID:             uc.idGen.NewNotificationID(),
		CorrelationID:  correlationID,
		Channel:        channel,
		Priority:       priority,
		Recipient:      in.Recipient,
		Content:        content,
		IdempotencyKey: in.IdempotencyKey,
		ScheduledAt:    in.ScheduledAt,
		TemplateID:     in.TemplateID,
	}, now)
	if err != nil {
		return nil, err
	}

	if err := uc.repo.Create(ctx, n); err != nil {
		return nil, fmt.Errorf("create notification: %w", err)
	}

	logEntry, err := domain.NewNotificationLog(domain.NewNotificationLogInput{
		ID:             uc.idGen.NewLogID(),
		NotificationID: n.ID,
		CorrelationID:  n.CorrelationID,
		Event:          domain.LogEventCreated,
	}, now)
	if err != nil {
		return nil, fmt.Errorf("build log entry: %w", err)
	}
	if err := uc.logRepo.Append(ctx, logEntry); err != nil {
		return nil, fmt.Errorf("append log entry: %w", err)
	}

	if err := uc.enqueue(ctx, n); err != nil {
		return nil, fmt.Errorf("enqueue notification: %w", err)
	}

	if err := uc.markQueued(ctx, n, now); err != nil {
		return nil, err
	}

	if uc.metrics != nil {
		uc.metrics.NotificationCreated(string(n.Channel), string(n.Priority))
	}

	return n, nil
}

// markQueued advances pending → queued and writes the audit-log row
// so the worker's atomic claim (queued|retrying → processing) accepts
// the notification. On failure the notification remains in pending;
// the reconciler's orphaned-pending sweep (ADR-0011) puts it back
// into circulation. Trades a small extra latency on the rare failure
// path for a strictly safer transition order.
func (uc *CreateNotification) markQueued(ctx context.Context, n *domain.Notification, now time.Time) error {
	if err := n.MarkQueued(now); err != nil {
		return fmt.Errorf("mark queued: %w", err)
	}
	if err := uc.repo.UpdateStatus(ctx, n, domain.StatusPending); err != nil {
		return fmt.Errorf("persist queued status: %w", err)
	}

	entry, err := domain.NewNotificationLog(domain.NewNotificationLogInput{
		ID:             uc.idGen.NewLogID(),
		NotificationID: n.ID,
		CorrelationID:  n.CorrelationID,
		Event:          domain.LogEventQueued,
	}, now)
	if err != nil {
		return fmt.Errorf("build queued log entry: %w", err)
	}
	if err := uc.logRepo.Append(ctx, entry); err != nil {
		return fmt.Errorf("append queued log entry: %w", err)
	}
	return nil
}

// enqueue selects between immediate and scheduled delivery based on the
// notification's ScheduledAt. Pulled out so Execute reads top-to-bottom.
func (uc *CreateNotification) enqueue(ctx context.Context, n *domain.Notification) error {
	if n.ScheduledAt != nil {
		return uc.queue.EnqueueScheduled(ctx, n.ID, n.Priority, *n.ScheduledAt)
	}
	return uc.queue.Enqueue(ctx, n.ID, n.Priority, n.IdempotencyKey)
}

// renderTemplate fetches the named template and substitutes the
// supplied variables into its body. ports.ErrNotFound and
// domain.ErrTemplateRenderFailed propagate so the HTTP translator
// maps them to the expected RFC 7807 responses.
func (uc *CreateNotification) renderTemplate(ctx context.Context, id string, vars map[string]any) (string, error) {
	tmpl, err := uc.tmplRepo.Get(ctx, domain.TemplateID(id))
	if err != nil {
		return "", fmt.Errorf("get template %s: %w", id, err)
	}
	rendered, err := tmpl.Render(vars)
	if err != nil {
		return "", fmt.Errorf("render template %s: %w", id, err)
	}
	return rendered, nil
}
