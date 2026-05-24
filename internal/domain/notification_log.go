package domain

import (
	"strings"
	"time"
)

// LogID is the unique identifier for one row in the notification_logs table.
// The string form is the UUID v7 representation, parallel to the other ID
// types in this package.
type LogID string

// LogEvent names a single point in a notification's lifecycle. The set is
// fixed; six events coincide with Status names (queued / processing /
// delivered / retrying / failed / cancelled) plus the special `created`
// event written when the row is first persisted, before any status
// transition has happened.
type LogEvent string

// All defined log events. The string form is the canonical representation
// persisted to notification_logs.event and emitted in the trace endpoint.
const (
	LogEventCreated    LogEvent = "created"
	LogEventQueued     LogEvent = "queued"
	LogEventProcessing LogEvent = "processing"
	LogEventDelivered  LogEvent = "delivered"
	LogEventRetrying   LogEvent = "retrying"
	LogEventFailed     LogEvent = "failed"
	LogEventCancelled  LogEvent = "cancelled"
)

// NewLogEvent parses and validates a log event string. Input is
// case-insensitive and surrounding whitespace is trimmed; the returned
// LogEvent is always one of the package-level constants. Anything else
// returns ErrInvalidLogEvent (declared in errors.go).
func NewLogEvent(s string) (LogEvent, error) {
	normalised := strings.ToLower(strings.TrimSpace(s))
	candidate := LogEvent(normalised)
	if candidate.IsValid() {
		return candidate, nil
	}
	return "", ErrInvalidLogEvent
}

// String returns the canonical lowercase representation of the log event.
func (e LogEvent) String() string {
	return string(e)
}

// IsValid reports whether the log event equals one of the package-level
// constants. Strict predicate; NewLogEvent is the lenient parser.
func (e LogEvent) IsValid() bool {
	switch e {
	case LogEventCreated, LogEventQueued, LogEventProcessing,
		LogEventDelivered, LogEventRetrying, LogEventFailed, LogEventCancelled:
		return true
	default:
		return false
	}
}

// NotificationLog is one row of the per-notification audit trail. Each status
// transition (and the initial creation) writes one row; the trace endpoint
// (GET /api/v1/notifications/{id}/trace) reads them back in chronological
// order. The shape mirrors notification_logs in the database, per
// CLAUDE.md §12.3.
type NotificationLog struct {
	ID             LogID
	NotificationID NotificationID
	CorrelationID  string
	Event          LogEvent
	Details        map[string]any
	CreatedAt      time.Time
}

// NewNotificationLogInput bundles parameters for NewNotificationLog.
type NewNotificationLogInput struct {
	ID             LogID
	NotificationID NotificationID
	CorrelationID  string
	Event          LogEvent
	Details        map[string]any // optional; nil and empty are both accepted
}

// NewNotificationLog constructs a fully validated NotificationLog. Details
// are stored as-is; the repository layer is responsible for JSON marshaling
// and any size limits. The `now` parameter sets CreatedAt — callers inject
// a clock (CLAUDE.md §3.6).
func NewNotificationLog(in NewNotificationLogInput, now time.Time) (*NotificationLog, error) {
	if in.ID == "" {
		return nil, ErrInvalidLogID
	}
	if in.NotificationID == "" {
		return nil, ErrInvalidNotificationID
	}
	if in.CorrelationID == "" {
		return nil, ErrInvalidCorrelationID
	}
	if !in.Event.IsValid() {
		return nil, ErrInvalidLogEvent
	}

	return &NotificationLog{
		ID:             in.ID,
		NotificationID: in.NotificationID,
		CorrelationID:  in.CorrelationID,
		Event:          in.Event,
		Details:        in.Details,
		CreatedAt:      now,
	}, nil
}
