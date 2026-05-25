package domain

import (
	"fmt"
	"regexp"
	"time"
)

// NotificationID is the unique identifier for a notification. The string form
// is the UUID v7 representation (lexicographically sortable). The domain
// itself does not generate IDs — they arrive via a ports.IDGenerator adapter
// (CLAUDE.md §3.3: no third-party dependencies in domain).
type NotificationID string

// Sentinel errors used by this file (ErrInvalidNotificationID, ErrInvalidCorrelationID,
// ErrInvalidRecipient, ErrInvalidContent, ErrInvalidTransition) are declared
// in errors.go alongside the typed error variants.

// contentLimits is the channel-specific maximum content length (CLAUDE.md §11,
// matches the reference implementation).
var contentLimits = map[Channel]int{
	ChannelSMS:   160,
	ChannelEmail: 10000,
	ChannelPush:  500,
}

// e164Pattern matches an E.164 phone number: a `+` followed by 2-15 digits
// (the first digit is non-zero per the spec).
var e164Pattern = regexp.MustCompile(`^\+[1-9]\d{1,14}$`)

// emailPattern enforces the minimal shape `local@domain.tld`. Full RFC 5322
// is intentionally not implemented — overly strict patterns reject legitimate
// addresses, and the email provider is the ultimate authority.
var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// Notification is the central domain entity. A Notification moves through the
// Status state machine (channel-specific delivery), gets retried on transient
// errors, and eventually lands in one of three terminal states (delivered,
// failed, cancelled). The Mark* methods are the only legal way to mutate
// Status; callers must respect the FSM defined in status.go.
type Notification struct {
	ID             NotificationID
	BatchID        *BatchID // pointer = optional; set by Batch.NewBatch
	IdempotencyKey string
	CorrelationID  string

	Channel   Channel
	Priority  Priority
	Recipient string
	Content   string

	Status      Status
	Attempts    int
	LastError   string
	NextRetryAt *time.Time

	ScheduledAt *time.Time
	TemplateID  *string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewNotificationInput bundles parameters for NewNotification. Using a struct
// keeps the constructor signature stable as fields are added (e.g. when the
// template feature ships).
type NewNotificationInput struct {
	ID             NotificationID
	BatchID        *BatchID
	IdempotencyKey string
	CorrelationID  string

	Channel   Channel
	Priority  Priority
	Recipient string
	Content   string

	ScheduledAt *time.Time
	TemplateID  *string
}

// NewNotification constructs a fully validated Notification in the initial
// pending state. The `now` parameter sets CreatedAt and UpdatedAt — callers
// inject a clock (CLAUDE.md §3.6 anti-pattern: time.Now in business logic).
func NewNotification(in NewNotificationInput, now time.Time) (*Notification, error) {
	if in.ID == "" {
		return nil, ErrInvalidNotificationID
	}
	if in.CorrelationID == "" {
		return nil, ErrInvalidCorrelationID
	}
	if !in.Channel.IsValid() {
		return nil, ErrInvalidChannel
	}
	if !in.Priority.IsValid() {
		return nil, ErrInvalidPriority
	}
	if err := validateRecipient(in.Channel, in.Recipient); err != nil {
		return nil, err
	}
	if err := validateContent(in.Channel, in.Content); err != nil {
		return nil, err
	}

	return &Notification{
		ID:             in.ID,
		BatchID:        in.BatchID,
		IdempotencyKey: in.IdempotencyKey,
		CorrelationID:  in.CorrelationID,
		Channel:        in.Channel,
		Priority:       in.Priority,
		Recipient:      in.Recipient,
		Content:        in.Content,
		Status:         StatusPending,
		ScheduledAt:    in.ScheduledAt,
		TemplateID:     in.TemplateID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

func validateRecipient(channel Channel, recipient string) error {
	if recipient == "" {
		return ErrInvalidRecipient
	}
	switch channel {
	case ChannelSMS:
		if !e164Pattern.MatchString(recipient) {
			return fmt.Errorf("%w: not an E.164 phone number", ErrInvalidRecipient)
		}
	case ChannelEmail:
		if !emailPattern.MatchString(recipient) {
			return fmt.Errorf("%w: not a valid email address", ErrInvalidRecipient)
		}
	case ChannelPush:
		// Push tokens (FCM / APNs) have provider-specific formats; we accept
		// any non-empty opaque string here and let the provider validate.
	}
	return nil
}

func validateContent(channel Channel, content string) error {
	if content == "" {
		return fmt.Errorf("%w: empty", ErrInvalidContent)
	}
	limit, ok := contentLimits[channel]
	if !ok {
		// Defensive: NewNotification validates Channel before calling this,
		// but in case the helper is reused elsewhere we surface the error.
		return ErrInvalidChannel
	}
	if len(content) > limit {
		return fmt.Errorf("%w: %d exceeds limit %d for channel %s",
			ErrInvalidContent, len(content), limit, channel)
	}
	return nil
}

// MarkQueued advances pending → queued. Returns ErrInvalidTransition if the
// current Status does not permit it; the entity is left untouched on failure.
func (n *Notification) MarkQueued(now time.Time) error {
	return n.transition(StatusQueued, now, func() {
		// Intentional no-op: pending → queued only flips the status
		// and bumps UpdatedAt, both handled by transition itself.
	})
}

// MarkProcessing advances the notification into processing. The Attempts
// counter is incremented — this is the only place where it grows.
func (n *Notification) MarkProcessing(now time.Time) error {
	return n.transition(StatusProcessing, now, func() {
		n.Attempts++
	})
}

// MarkDelivered marks the notification as successfully delivered (terminal).
// Clears any previous LastError so the trace endpoint accurately reflects
// the final outcome.
func (n *Notification) MarkDelivered(now time.Time) error {
	return n.transition(StatusDelivered, now, func() {
		n.LastError = ""
	})
}

// MarkFailed marks the notification as failed (terminal). The reason is
// stored in LastError so the trace endpoint can explain the failure to
// operators and API consumers.
func (n *Notification) MarkFailed(now time.Time, reason string) error {
	return n.transition(StatusFailed, now, func() {
		n.LastError = reason
	})
}

// MarkRetrying flips the notification into retrying with a scheduled next
// attempt; the worker stays out of the way until then. The reason is
// recorded for trace visibility, and NextRetryAt is what the reconciler
// inspects for overdue retries (CLAUDE.md §3.11).
func (n *Notification) MarkRetrying(now time.Time, reason string, nextRetryAt time.Time) error {
	return n.transition(StatusRetrying, now, func() {
		n.LastError = reason
		t := nextRetryAt
		n.NextRetryAt = &t
	})
}

// Cancel moves the notification into cancelled (terminal). Legal from
// pending / queued / retrying — processing is explicitly forbidden because
// we cannot un-send a notification once the provider call is in flight.
func (n *Notification) Cancel(now time.Time) error {
	return n.transition(StatusCancelled, now, func() {
		// Intentional no-op: cancellation does not touch LastError or
		// counters. The reason a user cancels is not stored — the
		// trace log row captures intent at the event-log layer.
	})
}

// transition is the internal helper every Mark* method uses. It enforces the
// state machine, records the new timestamp, and runs the side-effect callback
// — but only if the transition is legal. On failure the entity is left
// untouched, which the tests assert.
func (n *Notification) transition(target Status, now time.Time, sideEffect func()) error {
	if !n.Status.CanTransitionTo(target) {
		return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, n.Status, target)
	}
	n.Status = target
	n.UpdatedAt = now
	sideEffect()
	return nil
}
