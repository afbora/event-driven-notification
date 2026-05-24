package domain

import (
	"errors"
	"fmt"
)

// Every public domain error is declared in this file. Two flavors exist:
//
//   - Sentinels (errors.New) for catch-all detection via errors.Is. The HTTP
//     error translator (CLAUDE.md §3.5) maps these to RFC 7807 Problem
//     Details responses without inspecting error messages.
//   - Typed errors (struct types) for callers that need structured context
//     (the offending field, the from/to status). They wrap the appropriate
//     sentinel so errors.Is keeps matching the broad case while errors.As
//     extracts the detail.
//
// New domain errors must be added here, not inline next to the type that
// raised them. Centralizing them keeps the HTTP translator's switch table
// in lockstep with the domain.

// --- Sentinels -------------------------------------------------------------

// Value object validation.
var (
	ErrInvalidChannel  = errors.New("invalid channel")
	ErrInvalidPriority = errors.New("invalid priority")
	ErrInvalidStatus   = errors.New("invalid status")
	ErrInvalidLogEvent = errors.New("invalid log event")
)

// Identifier validation.
var (
	ErrInvalidNotificationID = errors.New("invalid notification id")
	ErrInvalidCorrelationID  = errors.New("invalid correlation id")
	ErrInvalidBatchID        = errors.New("invalid batch id")
	ErrInvalidTemplateID     = errors.New("invalid template id")
	ErrInvalidLogID          = errors.New("invalid log id")
)

// Notification field validation.
var (
	ErrInvalidRecipient = errors.New("invalid recipient")
	ErrInvalidContent   = errors.New("invalid content")
)

// State machine.
var (
	ErrInvalidTransition = errors.New("invalid status transition")
)

// Batch invariants.
var (
	ErrInvalidBatchSize             = errors.New("invalid batch size")
	ErrBatchInconsistentCorrelation = errors.New("batch notifications have inconsistent correlation id")
)

// Template invariants.
var (
	ErrInvalidTemplateName  = errors.New("invalid template name")
	ErrInvalidTemplateBody  = errors.New("invalid template body")
	ErrTemplateRenderFailed = errors.New("template render failed")
)

// --- Typed errors ----------------------------------------------------------

// ValidationError carries the offending field name and a human-readable
// reason. It wraps a sentinel so the HTTP translator can fall back on the
// broad errors.Is match while a more specific handler uses errors.As to
// pull out Field and Reason for an RFC 7807 response body.
type ValidationError struct {
	Field  string
	Reason string
	Err    error // wrapped sentinel; may be nil for ad-hoc validation
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	switch {
	case e.Field == "" && e.Reason == "":
		return "validation error"
	case e.Field == "":
		return "validation error: " + e.Reason
	case e.Reason == "":
		return fmt.Sprintf("validation error: field=%s", e.Field)
	default:
		return fmt.Sprintf("validation error: field=%s: %s", e.Field, e.Reason)
	}
}

// Unwrap returns the wrapped sentinel so errors.Is traverses the chain.
func (e *ValidationError) Unwrap() error {
	return e.Err
}

// TransitionError captures the from/to statuses of an illegal status
// transition. Always considered equivalent to ErrInvalidTransition under
// errors.Is.
type TransitionError struct {
	From Status
	To   Status
}

// Error implements the error interface.
func (e *TransitionError) Error() string {
	return fmt.Sprintf("invalid status transition: %s → %s", e.From, e.To)
}

// Is reports whether target equals ErrInvalidTransition. Allows
// errors.Is(transitionErr, ErrInvalidTransition) without an explicit
// Unwrap chain.
func (e *TransitionError) Is(target error) bool {
	return target == ErrInvalidTransition
}
