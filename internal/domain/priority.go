package domain

import (
	"errors"
	"strings"
)

// Priority bucketises notifications for the queue. The three values map to
// asynq queue weights at the adapter layer (high=6, default=3, low=1, per
// ADR-0003), but the domain treats Priority as an opaque label — it doesn't
// expose comparison semantics, because nothing in the business rules needs
// to ask "is this higher than that".
type Priority string

// Valid priority values. The string form is the canonical representation
// persisted to the database and embedded in queue payloads.
const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
)

// ErrInvalidPriority is returned by NewPriority when the input does not name
// a known priority. Detect with errors.Is so the comparison survives future
// wrapping.
var ErrInvalidPriority = errors.New("invalid priority")

// NewPriority parses and validates a priority string. Input is case-insensitive
// and surrounding whitespace is trimmed; the returned Priority is always one
// of the package-level constants. Anything else returns ErrInvalidPriority.
func NewPriority(s string) (Priority, error) {
	normalised := strings.ToLower(strings.TrimSpace(s))
	switch Priority(normalised) {
	case PriorityLow, PriorityNormal, PriorityHigh:
		return Priority(normalised), nil
	default:
		return "", ErrInvalidPriority
	}
}

// String returns the canonical lowercase representation of the priority.
func (p Priority) String() string {
	return string(p)
}

// IsValid reports whether the priority equals one of the package-level
// constants. A Priority cast from an unnormalised string (e.g.
// Priority("HIGH")) returns false here; that is intentional — IsValid is
// a strict predicate, NewPriority is the lenient parser.
func (p Priority) IsValid() bool {
	switch p {
	case PriorityLow, PriorityNormal, PriorityHigh:
		return true
	default:
		return false
	}
}
