package domain

import (
	"errors"
	"strings"
)

// Status represents the lifecycle stage of a notification. The set is fixed
// (see the package-level constants) and transitions follow a finite-state
// machine:
//
//	pending → queued → processing → delivered | failed | retrying
//	retrying → processing
//	pending | queued | retrying → cancelled
//
// The terminal states (delivered, failed, cancelled) reject every outgoing
// transition. CLAUDE.md §3.10 and ADR-0009 rely on this invariant: the
// atomic claim in the worker is written assuming terminal states are truly
// terminal.
type Status string

// All defined status values. The string form is the canonical representation
// persisted to the database and emitted in API responses.
const (
	StatusPending    Status = "pending"
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusDelivered  Status = "delivered"
	StatusFailed     Status = "failed"
	StatusRetrying   Status = "retrying"
	StatusCancelled  Status = "cancelled"
)

// ErrInvalidStatus is returned by NewStatus when the input does not name a
// known status. Detect with errors.Is so the comparison survives wrapping.
var ErrInvalidStatus = errors.New("invalid status")

// validTransitions encodes the FSM as an adjacency list. Statuses missing
// from this map (delivered, failed, cancelled) are terminal: no outgoing
// edges. Adding a new status requires updating both this map and the IsValid
// / IsTerminal predicates.
var validTransitions = map[Status][]Status{
	StatusPending:    {StatusQueued, StatusCancelled},
	StatusQueued:     {StatusProcessing, StatusCancelled},
	StatusProcessing: {StatusDelivered, StatusFailed, StatusRetrying},
	StatusRetrying:   {StatusProcessing, StatusCancelled},
}

// NewStatus parses and validates a status string. Input is case-insensitive
// and surrounding whitespace is trimmed; the returned Status is always one
// of the package-level constants. Anything else returns ErrInvalidStatus.
func NewStatus(s string) (Status, error) {
	normalised := strings.ToLower(strings.TrimSpace(s))
	candidate := Status(normalised)
	if candidate.IsValid() {
		return candidate, nil
	}
	return "", ErrInvalidStatus
}

// String returns the canonical lowercase representation of the status.
func (s Status) String() string {
	return string(s)
}

// IsValid reports whether the status equals one of the package-level
// constants. A Status cast from an unnormalised string returns false here;
// IsValid is the strict predicate, NewStatus is the lenient parser.
func (s Status) IsValid() bool {
	switch s {
	case StatusPending, StatusQueued, StatusProcessing,
		StatusDelivered, StatusFailed, StatusRetrying, StatusCancelled:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether the status has no outgoing transitions. Once a
// notification reaches a terminal state it never moves again.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusDelivered, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// CanTransitionTo reports whether moving from s to next is permitted by the
// state machine. Same-state "transitions" return false (callers wanting a
// no-op should compare for equality themselves); unknown next values return
// false silently.
func (s Status) CanTransitionTo(next Status) bool {
	allowed, ok := validTransitions[s]
	if !ok {
		// Terminal or unknown source status — no outgoing edges.
		return false
	}
	if !next.IsValid() {
		return false
	}
	for _, candidate := range allowed {
		if candidate == next {
			return true
		}
	}
	return false
}
