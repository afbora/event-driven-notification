package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// problemBody is the decoded shape we read back from the response —
// kept private to this test file so the production code can evolve the
// struct without dragging tests along.
type problemBody struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Detail        string `json:"detail"`
	Instance      string `json:"instance"`
	CorrelationID string `json:"correlation_id"`
}

// requestWithCorrelation builds a fresh *http.Request whose context
// carries a known correlation id — so the test can verify the
// translator copies it into the problem body.
func requestWithCorrelation(t *testing.T, path string, id string) *nethttp.Request {
	t.Helper()
	req := httptest.NewRequest(nethttp.MethodPost, path, nil)
	if id != "" {
		ctx := httpadapter.ContextWithCorrelationID(context.Background(), id)
		req = req.WithContext(ctx)
	}
	return req
}

func decodeProblem(t *testing.T, rr *httptest.ResponseRecorder) problemBody {
	t.Helper()
	require.Equal(t, "application/problem+json", rr.Header().Get("Content-Type"),
		"RFC 7807 mandates application/problem+json")
	var p problemBody
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &p))
	return p
}

// TestWriteProblem_BasicShape: the low-level writer emits every RFC
// 7807 member and uses Request.URL.Path as the instance member.
func TestWriteProblem_BasicShape(t *testing.T) {
	req := requestWithCorrelation(t, "/api/v1/notifications", "01HXYZCORR0001")
	rr := httptest.NewRecorder()

	httpadapter.WriteProblem(rr, req, httpadapter.Problem{
		Type:   "/probs/invalid-channel",
		Title:  "Invalid channel",
		Status: nethttp.StatusBadRequest,
		Detail: "Channel must be one of: sms, email, push",
	})

	require.Equal(t, nethttp.StatusBadRequest, rr.Code)
	p := decodeProblem(t, rr)
	require.Equal(t, "/probs/invalid-channel", p.Type)
	require.Equal(t, "Invalid channel", p.Title)
	require.Equal(t, nethttp.StatusBadRequest, p.Status)
	require.Equal(t, "Channel must be one of: sms, email, push", p.Detail)
	require.Equal(t, "/api/v1/notifications", p.Instance, "instance is the request path")
	require.Equal(t, "01HXYZCORR0001", p.CorrelationID)
}

// TestWriteProblem_DefaultsTypeToAboutBlank: per RFC 7807 §3.1 a Problem
// with no explicit type defaults to "about:blank".
func TestWriteProblem_DefaultsTypeToAboutBlank(t *testing.T) {
	req := requestWithCorrelation(t, "/x", "")
	rr := httptest.NewRecorder()

	httpadapter.WriteProblem(rr, req, httpadapter.Problem{
		Title:  "Whatever",
		Status: nethttp.StatusBadRequest,
	})

	p := decodeProblem(t, rr)
	require.Equal(t, "about:blank", p.Type)
}

// TestWriteError_MapsKnownErrors: the high-level translator dispatches
// every known domain / port sentinel to the right status and a
// stable, machine-readable type slug. New domain errors must extend
// this table — the test is the spec.
func TestWriteError_MapsKnownErrors(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantType   string
	}{
		{"not found", ports.ErrNotFound, nethttp.StatusNotFound, "/probs/not-found"},
		{"concurrent update", ports.ErrConcurrentUpdate, nethttp.StatusConflict, "/probs/concurrent-update"},
		{"already claimed", ports.ErrAlreadyClaimed, nethttp.StatusConflict, "/probs/already-claimed"},

		{"invalid channel", domain.ErrInvalidChannel, nethttp.StatusBadRequest, "/probs/invalid-channel"},
		{"invalid priority", domain.ErrInvalidPriority, nethttp.StatusBadRequest, "/probs/invalid-priority"},
		{"invalid status", domain.ErrInvalidStatus, nethttp.StatusBadRequest, "/probs/invalid-status"},
		{"invalid log event", domain.ErrInvalidLogEvent, nethttp.StatusBadRequest, "/probs/invalid-log-event"},

		{"invalid notification id", domain.ErrInvalidNotificationID, nethttp.StatusBadRequest, "/probs/invalid-notification-id"},
		{"invalid correlation id", domain.ErrInvalidCorrelationID, nethttp.StatusBadRequest, "/probs/invalid-correlation-id"},
		{"invalid batch id", domain.ErrInvalidBatchID, nethttp.StatusBadRequest, "/probs/invalid-batch-id"},
		{"invalid template id", domain.ErrInvalidTemplateID, nethttp.StatusBadRequest, "/probs/invalid-template-id"},
		{"invalid log id", domain.ErrInvalidLogID, nethttp.StatusBadRequest, "/probs/invalid-log-id"},

		{"invalid recipient", domain.ErrInvalidRecipient, nethttp.StatusBadRequest, "/probs/invalid-recipient"},
		{"invalid content", domain.ErrInvalidContent, nethttp.StatusBadRequest, "/probs/invalid-content"},

		{"invalid transition", domain.ErrInvalidTransition, nethttp.StatusConflict, "/probs/invalid-transition"},

		{"invalid batch size", domain.ErrInvalidBatchSize, nethttp.StatusBadRequest, "/probs/invalid-batch-size"},
		{"batch inconsistent correlation", domain.ErrBatchInconsistentCorrelation, nethttp.StatusBadRequest, "/probs/batch-inconsistent-correlation"},

		{"invalid template name", domain.ErrInvalidTemplateName, nethttp.StatusBadRequest, "/probs/invalid-template-name"},
		{"invalid template body", domain.ErrInvalidTemplateBody, nethttp.StatusBadRequest, "/probs/invalid-template-body"},
		{"template render failed", domain.ErrTemplateRenderFailed, nethttp.StatusBadRequest, "/probs/template-render-failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := requestWithCorrelation(t, "/api/v1/x", "id-"+tc.name)
			rr := httptest.NewRecorder()
			httpadapter.WriteError(rr, req, tc.err)

			require.Equal(t, tc.wantStatus, rr.Code)
			p := decodeProblem(t, rr)
			require.Equal(t, tc.wantType, p.Type)
			require.NotEmpty(t, p.Title, "every mapped error must have a Title")
			require.Equal(t, tc.wantStatus, p.Status)
			require.Equal(t, "id-"+tc.name, p.CorrelationID)
		})
	}
}

// TestWriteError_WrappedSentinelStillMatches: adapter errors are
// wrapped with context (fmt.Errorf("...%w", sentinel)). The translator
// must still recognize the wrapped sentinel via errors.Is — otherwise
// every adapter would have to unwrap manually.
func TestWriteError_WrappedSentinelStillMatches(t *testing.T) {
	wrapped := fmt.Errorf("postgres: get notification: %w", ports.ErrNotFound)

	req := requestWithCorrelation(t, "/api/v1/notifications/abc", "")
	rr := httptest.NewRecorder()
	httpadapter.WriteError(rr, req, wrapped)

	require.Equal(t, nethttp.StatusNotFound, rr.Code)
	p := decodeProblem(t, rr)
	require.Equal(t, "/probs/not-found", p.Type)
}

// TestWriteError_ValidationError_FieldAndReason: a typed
// ValidationError surfaces its Field and Reason in the problem detail
// — the caller does not have to format the detail manually.
func TestWriteError_ValidationError_FieldAndReason(t *testing.T) {
	verr := &domain.ValidationError{
		Field:  "recipient",
		Reason: "must be e164",
		Err:    domain.ErrInvalidRecipient,
	}

	req := requestWithCorrelation(t, "/x", "")
	rr := httptest.NewRecorder()
	httpadapter.WriteError(rr, req, verr)

	require.Equal(t, nethttp.StatusBadRequest, rr.Code)
	p := decodeProblem(t, rr)
	require.Equal(t, "/probs/invalid-recipient", p.Type,
		"validation error still maps via its wrapped sentinel")
	require.Contains(t, p.Detail, "recipient")
	require.Contains(t, p.Detail, "must be e164")
}

// TestWriteError_TransitionError_FromAndTo: the typed TransitionError
// surfaces both states in the problem detail.
func TestWriteError_TransitionError_FromAndTo(t *testing.T) {
	terr := &domain.TransitionError{
		From: domain.StatusDelivered,
		To:   domain.StatusProcessing,
	}

	req := requestWithCorrelation(t, "/x", "")
	rr := httptest.NewRecorder()
	httpadapter.WriteError(rr, req, terr)

	require.Equal(t, nethttp.StatusConflict, rr.Code)
	p := decodeProblem(t, rr)
	require.Equal(t, "/probs/invalid-transition", p.Type)
	require.Contains(t, p.Detail, string(domain.StatusDelivered))
	require.Contains(t, p.Detail, string(domain.StatusProcessing))
}

// TestWriteError_UnknownError_Defaults500: any unrecognized error is
// reported as 500 Internal so the API never leaks an unmapped
// exception. The detail member is generic — implementation details
// must not reach the client.
func TestWriteError_UnknownError_Defaults500(t *testing.T) {
	req := requestWithCorrelation(t, "/x", "")
	rr := httptest.NewRecorder()
	httpadapter.WriteError(rr, req, errors.New("totally unexpected explosion"))

	require.Equal(t, nethttp.StatusInternalServerError, rr.Code)
	p := decodeProblem(t, rr)
	require.Equal(t, "/probs/internal", p.Type)
	require.NotContains(t, p.Detail, "explosion",
		"raw error text must never leak to the client for unknown errors")
}

// TestWriteError_NilError_Noop: defensive guard — WriteError must not
// panic when the caller forgets to check for nil before delegating.
func TestWriteError_NilError_Noop(t *testing.T) {
	req := requestWithCorrelation(t, "/x", "")
	rr := httptest.NewRecorder()

	require.NotPanics(t, func() {
		httpadapter.WriteError(rr, req, nil)
	})
	require.Equal(t, nethttp.StatusOK, rr.Code,
		"nil error: nothing written; default 200 sticks because the test recorder defaults to 200")
}
