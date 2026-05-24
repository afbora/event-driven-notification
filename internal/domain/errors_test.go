package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// TestSentinels confirms every public sentinel error is exported and
// reachable. This is a regression guard against accidental renames or
// silent removals — the HTTP error translator (CLAUDE.md §3.5) hangs off
// these identifiers, so they must stay stable.
func TestSentinels(t *testing.T) {
	sentinels := map[string]error{
		"ErrInvalidChannel":               domain.ErrInvalidChannel,
		"ErrInvalidPriority":              domain.ErrInvalidPriority,
		"ErrInvalidStatus":                domain.ErrInvalidStatus,
		"ErrInvalidNotificationID":        domain.ErrInvalidNotificationID,
		"ErrInvalidCorrelationID":         domain.ErrInvalidCorrelationID,
		"ErrInvalidRecipient":             domain.ErrInvalidRecipient,
		"ErrInvalidContent":               domain.ErrInvalidContent,
		"ErrInvalidTransition":            domain.ErrInvalidTransition,
		"ErrInvalidBatchID":               domain.ErrInvalidBatchID,
		"ErrInvalidBatchSize":             domain.ErrInvalidBatchSize,
		"ErrBatchInconsistentCorrelation": domain.ErrBatchInconsistentCorrelation,
		"ErrInvalidTemplateID":            domain.ErrInvalidTemplateID,
		"ErrInvalidTemplateName":          domain.ErrInvalidTemplateName,
		"ErrInvalidTemplateBody":          domain.ErrInvalidTemplateBody,
		"ErrTemplateRenderFailed":         domain.ErrTemplateRenderFailed,
		"ErrInvalidLogID":                 domain.ErrInvalidLogID,
		"ErrInvalidLogEvent":              domain.ErrInvalidLogEvent,
	}

	for name, s := range sentinels {
		t.Run(name, func(t *testing.T) {
			if s == nil {
				t.Error("sentinel is nil")
				return
			}
			if s.Error() == "" {
				t.Error("sentinel has empty message")
			}
			// Self-Is: every sentinel matches itself via errors.Is.
			if !errors.Is(s, s) {
				t.Error("errors.Is(s, s) = false")
			}
		})
	}
}

// TestValidationError covers the typed error variant. Callers can extract
// Field and Reason via errors.As; the wrapped sentinel remains discoverable
// via errors.Is so the catch-all HTTP translator still maps the response.
func TestValidationError(t *testing.T) {
	err := &domain.ValidationError{
		Field:  "recipient",
		Reason: "not an E.164 phone number",
		Err:    domain.ErrInvalidRecipient,
	}

	// errors.Is matches the wrapped sentinel.
	if !errors.Is(err, domain.ErrInvalidRecipient) {
		t.Error("errors.Is(ValidationError, ErrInvalidRecipient) = false, want true")
	}

	// errors.As extracts the typed struct.
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatal("errors.As(*ValidationError) = false, want true")
	}
	if ve.Field != "recipient" {
		t.Errorf("Field = %q, want %q", ve.Field, "recipient")
	}
	if ve.Reason != "not an E.164 phone number" {
		t.Errorf("Reason = %q, want %q", ve.Reason, "not an E.164 phone number")
	}

	// Error() message is informative — at minimum mentions field and reason
	// so it's useful in a log line without manual unwrapping.
	msg := err.Error()
	if !strings.Contains(msg, "recipient") {
		t.Errorf("Error() = %q, want to mention field name", msg)
	}
	if !strings.Contains(msg, "E.164") {
		t.Errorf("Error() = %q, want to mention reason", msg)
	}
}

// TestValidationError_NilWrapped covers the case where a caller constructs a
// ValidationError without an underlying sentinel. errors.Is must still work
// (returning false because nothing was wrapped), and Error() must not panic.
func TestValidationError_NilWrapped(t *testing.T) {
	err := &domain.ValidationError{
		Field:  "name",
		Reason: "too short",
	}

	if errors.Is(err, domain.ErrInvalidRecipient) {
		t.Error("errors.Is matched unrelated sentinel, want false")
	}
	if err.Error() == "" {
		t.Error("Error() = empty string, want non-empty")
	}
}

// TestTransitionError covers the typed variant for invalid status transitions.
// HTTP handlers can pluck out From/To via errors.As to surface a specific
// 409 Conflict response.
func TestTransitionError(t *testing.T) {
	err := &domain.TransitionError{
		From: domain.StatusDelivered,
		To:   domain.StatusQueued,
	}

	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Error("errors.Is(TransitionError, ErrInvalidTransition) = false, want true")
	}

	var te *domain.TransitionError
	if !errors.As(err, &te) {
		t.Fatal("errors.As(*TransitionError) = false, want true")
	}
	if te.From != domain.StatusDelivered {
		t.Errorf("From = %q, want delivered", te.From)
	}
	if te.To != domain.StatusQueued {
		t.Errorf("To = %q, want queued", te.To)
	}

	msg := err.Error()
	if !strings.Contains(msg, "delivered") || !strings.Contains(msg, "queued") {
		t.Errorf("Error() = %q, want to mention both states", msg)
	}
}
