package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	nethttp "net/http"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// problemContentType is the media type RFC 7807 §3 mandates for the
// problem details body shape.
const problemContentType = "application/problem+json"

// Problem is the on-wire shape of an RFC 7807 Problem Details object
// (CLAUDE.md §3.5). Title and Status are required; Type defaults to
// "about:blank" per RFC 7807 §3.1 when omitted; Detail and Instance
// are optional. CorrelationID is a non-standard but commonly-added
// member (CLAUDE.md §10 example).
type Problem struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Detail        string `json:"detail,omitempty"`
	Instance      string `json:"instance,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
}

// WriteProblem serializes p as application/problem+json and writes it
// to w with the status carried in p. The request is used to fill in
// fields the caller didn't set explicitly: Instance defaults to the
// request URL path, CorrelationID defaults to the value in r.Context()
// (zero when the middleware has not run).
//
// Type defaults to "about:blank" per RFC 7807 §3.1 — a Problem without
// a more specific type still validates against the spec.
func WriteProblem(w nethttp.ResponseWriter, r *nethttp.Request, p Problem) {
	if p.Type == "" {
		p.Type = "about:blank"
	}
	if p.Instance == "" && r != nil && r.URL != nil {
		p.Instance = r.URL.Path
	}
	if p.CorrelationID == "" && r != nil {
		p.CorrelationID = CorrelationIDFromContext(r.Context())
	}

	body, err := json.Marshal(p)
	if err != nil {
		// json.Marshal of a tiny known struct should not fail; if it
		// somehow does, surface a generic 500 and log — we cannot
		// recover into a problem+json response since the marshal failed.
		slog.ErrorContext(r.Context(), "marshaling problem details failed",
			"error", err.Error())
		nethttp.Error(w, "internal error", nethttp.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", problemContentType)
	w.WriteHeader(p.Status)
	if _, werr := w.Write(body); werr != nil {
		slog.WarnContext(r.Context(), "writing problem details failed",
			"error", werr.Error())
	}
}

// WriteError is the single translation function CLAUDE.md §3.5
// mandates: it maps a domain or port error to the appropriate RFC 7807
// problem response. Adding a new domain error means adding one branch
// to errorMappings — the test catalog in problem_test.go is the spec.
//
// Calling WriteError with a nil error is a deliberate no-op so handlers
// that defer a single `if err != nil { WriteError(...) }` check don't
// have to guard the call site too.
func WriteError(w nethttp.ResponseWriter, r *nethttp.Request, err error) {
	if err == nil {
		return
	}

	// Pick the first mapping whose sentinel errors.Is matches. Order
	// of entries does not matter for correctness — sentinels in this
	// codebase don't form chains — but is kept stable for readability.
	for _, m := range errorMappings {
		if errors.Is(err, m.sentinel) {
			p := Problem{
				Type:   m.problemType,
				Title:  m.title,
				Status: m.status,
				Detail: errorDetail(err, m),
			}
			WriteProblem(w, r, p)
			return
		}
	}

	// Unknown error: 500 with a generic detail. Implementation
	// details (the raw error.Error() text) are deliberately NOT
	// echoed to the client — that is a security mistake. The real
	// error is logged for operators.
	slog.ErrorContext(r.Context(), "unmapped error reached http translator",
		"error", err.Error())
	WriteProblem(w, r, Problem{
		Type:   "/probs/internal",
		Title:  "Internal Server Error",
		Status: nethttp.StatusInternalServerError,
		Detail: "An unexpected error occurred. The incident has been logged.",
	})
}

// errorMapping pairs a sentinel error with the RFC 7807 fields that
// should be emitted when it surfaces. Detail is a default — typed
// errors (ValidationError, TransitionError) override it with their
// own contextual phrasing via errorDetail.
type errorMapping struct {
	sentinel      error
	status        int
	problemType   string
	title         string
	defaultDetail string
}

// errorMappings is the full catalog. Every sentinel error declared in
// internal/domain/errors.go and the port-level sentinels in
// internal/ports/ports.go must appear here, otherwise the error
// degrades to a generic 500 — the test catalog in problem_test.go
// keeps the two in lockstep.
var errorMappings = []errorMapping{
	// Port-level sentinels.
	{ports.ErrNotFound, nethttp.StatusNotFound, "/probs/not-found", "Not Found", "The requested resource does not exist."},
	{ports.ErrAlreadyClaimed, nethttp.StatusConflict, "/probs/already-claimed", "Already Claimed", "The notification is being processed by another worker."},
	{ports.ErrConcurrentUpdate, nethttp.StatusConflict, "/probs/concurrent-update", "Concurrent Update", "The resource was updated by another request; retry with the latest version."},

	// Value-object validation.
	{domain.ErrInvalidChannel, nethttp.StatusBadRequest, "/probs/invalid-channel", "Invalid Channel", "Channel must be one of: sms, email, push."},
	{domain.ErrInvalidPriority, nethttp.StatusBadRequest, "/probs/invalid-priority", "Invalid Priority", "Priority must be one of: low, normal, high."},
	{domain.ErrInvalidStatus, nethttp.StatusBadRequest, "/probs/invalid-status", "Invalid Status", "Status is not a known notification state."},
	{domain.ErrInvalidLogEvent, nethttp.StatusBadRequest, "/probs/invalid-log-event", "Invalid Log Event", "Log event is not a known transition event."},

	// Identifier validation.
	{domain.ErrInvalidNotificationID, nethttp.StatusBadRequest, "/probs/invalid-notification-id", "Invalid Notification ID", "Notification id must be a valid UUID."},
	{domain.ErrInvalidCorrelationID, nethttp.StatusBadRequest, "/probs/invalid-correlation-id", "Invalid Correlation ID", "Correlation id is not in the expected ULID or UUID shape."},
	{domain.ErrInvalidBatchID, nethttp.StatusBadRequest, "/probs/invalid-batch-id", "Invalid Batch ID", "Batch id must be a valid UUID."},
	{domain.ErrInvalidTemplateID, nethttp.StatusBadRequest, "/probs/invalid-template-id", "Invalid Template ID", "Template id must be a valid UUID."},
	{domain.ErrInvalidLogID, nethttp.StatusBadRequest, "/probs/invalid-log-id", "Invalid Log ID", "Log id must be a valid UUID."},

	// Notification field validation.
	{domain.ErrInvalidRecipient, nethttp.StatusBadRequest, "/probs/invalid-recipient", "Invalid Recipient", "Recipient address is missing or malformed."},
	{domain.ErrInvalidContent, nethttp.StatusBadRequest, "/probs/invalid-content", "Invalid Content", "Notification content is missing or exceeds the channel limit."},

	// State machine.
	{domain.ErrInvalidTransition, nethttp.StatusConflict, "/probs/invalid-transition", "Invalid Transition", "The requested status transition is not allowed from the current state."},

	// Batch invariants.
	{domain.ErrInvalidBatchSize, nethttp.StatusBadRequest, "/probs/invalid-batch-size", "Invalid Batch Size", "Batch size is outside the allowed range."},
	{domain.ErrBatchInconsistentCorrelation, nethttp.StatusBadRequest, "/probs/batch-inconsistent-correlation", "Batch Inconsistent Correlation", "All notifications in a batch must share the same correlation id."},

	// Template invariants.
	{domain.ErrInvalidTemplateName, nethttp.StatusBadRequest, "/probs/invalid-template-name", "Invalid Template Name", "Template name is missing or malformed."},
	{domain.ErrInvalidTemplateBody, nethttp.StatusBadRequest, "/probs/invalid-template-body", "Invalid Template Body", "Template body is missing or malformed."},
	{domain.ErrTemplateRenderFailed, nethttp.StatusBadRequest, "/probs/template-render-failed", "Template Render Failed", "Rendering the template against the provided variables failed."},
}

// errorDetail prefers the structured detail carried by typed errors
// (ValidationError.Field/Reason, TransitionError.From/To) over the
// mapping's generic phrasing. Typed errors carry caller-specific
// context that a hardcoded default cannot reproduce.
func errorDetail(err error, m errorMapping) string {
	var verr *domain.ValidationError
	if errors.As(err, &verr) {
		switch {
		case verr.Field != "" && verr.Reason != "":
			return fmt.Sprintf("Field %q: %s.", verr.Field, verr.Reason)
		case verr.Reason != "":
			return verr.Reason
		case verr.Field != "":
			return fmt.Sprintf("Field %q is invalid.", verr.Field)
		}
	}

	var terr *domain.TransitionError
	if errors.As(err, &terr) {
		return fmt.Sprintf("Cannot move from %q to %q.", terr.From, terr.To)
	}

	return m.defaultDetail
}
