// Package domain holds the core entities, value objects, and invariants of the
// notification system. It depends only on the Go standard library so that the
// business rules stay portable, testable, and independent of any specific
// HTTP / database / queue technology. See ADR-0001 for the hexagonal-boundary
// rationale.
package domain

import (
	"errors"
	"strings"
)

// Channel identifies the medium through which a notification is delivered.
// The zero value is invalid; callers obtain valid Channels by calling
// NewChannel or by using the package-level constants (ChannelSMS,
// ChannelEmail, ChannelPush).
type Channel string

// Valid channel values. The string form is the canonical representation
// persisted to the database, embedded in queue payloads, and emitted in
// API responses.
const (
	ChannelSMS   Channel = "sms"
	ChannelEmail Channel = "email"
	ChannelPush  Channel = "push"
)

// ErrInvalidChannel is returned by NewChannel when the input does not name a
// known channel. Callers detect it with errors.Is so the comparison survives
// future error wrapping.
var ErrInvalidChannel = errors.New("invalid channel")

// NewChannel parses and validates a channel string. Input is case-insensitive
// and surrounding whitespace is trimmed; the returned Channel is always one of
// the package-level constants. Anything else returns ErrInvalidChannel.
func NewChannel(s string) (Channel, error) {
	normalised := strings.ToLower(strings.TrimSpace(s))
	switch Channel(normalised) {
	case ChannelSMS, ChannelEmail, ChannelPush:
		return Channel(normalised), nil
	default:
		return "", ErrInvalidChannel
	}
}

// String returns the canonical lowercase representation of the channel.
func (c Channel) String() string {
	return string(c)
}

// IsValid reports whether the channel equals one of the package-level
// constants. A Channel cast from an unnormalised string (e.g.
// Channel("SMS")) returns false here; that is intentional — IsValid is
// a strict predicate, NewChannel is the lenient parser.
func (c Channel) IsValid() bool {
	switch c {
	case ChannelSMS, ChannelEmail, ChannelPush:
		return true
	default:
		return false
	}
}
