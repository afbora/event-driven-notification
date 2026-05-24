package http

import (
	"encoding/json"
	"errors"
	"io"
	nethttp "net/http"
)

// ErrNotImplemented is the sentinel an operation returns when no
// concrete handler has been wired to it yet. The strict-server error
// handler maps it to a 501 RFC 7807 problem response. Phase 4 tasks
// override one operation each in the Server struct; until then the
// embedded unimplementedServer returns this error.
var ErrNotImplemented = errors.New("not implemented")

// RespondWithError is the unified strict-server error handler — wire
// it into BOTH StrictHTTPServerOptions.RequestErrorHandlerFunc (body
// decode failures) and ResponseErrorHandlerFunc (handler errors). It
// bridges the strict-server world to the project's RFC 7807 translator
// (problem.go) so every operation gets uniform error semantics —
// adding a new domain error never requires touching this function,
// only the errorMappings table in problem.go.
//
// Special cases handled inline rather than via errorMappings:
//
//   - ErrNotImplemented → 501. Purely an http-adapter concern (the
//     domain has no concept of "operation not wired yet"); keeping it
//     here preserves the layering.
//   - JSON decode errors (*json.SyntaxError, *json.UnmarshalTypeError,
//     io.ErrUnexpectedEOF) → 400. The strict-server wraps these and
//     passes them through RequestErrorHandlerFunc; a 400 with a
//     generic Detail is the correct semantic response.
func RespondWithError(w nethttp.ResponseWriter, r *nethttp.Request, err error) {
	if errors.Is(err, ErrNotImplemented) {
		WriteProblem(w, r, Problem{
			Type:   "/probs/not-implemented",
			Title:  "Not Implemented",
			Status: nethttp.StatusNotImplemented,
			Detail: "This operation has not been wired to an executor yet.",
		})
		return
	}

	if isJSONDecodeError(err) {
		WriteProblem(w, r, Problem{
			Type:   "/probs/malformed-body",
			Title:  "Malformed Request Body",
			Status: nethttp.StatusBadRequest,
			Detail: "Request body could not be parsed as JSON. Check syntax and content-type.",
		})
		return
	}

	WriteError(w, r, err)
}

// isJSONDecodeError reports whether err is one of the canonical
// stdlib json decode failure types or io.ErrUnexpectedEOF. The
// strict-server wraps these via fmt.Errorf("can't decode JSON body:
// %w", ...), so errors.As / errors.Is traverse the chain to the
// underlying cause.
func isJSONDecodeError(err error) bool {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		return true
	case errors.As(err, &typ):
		return true
	case errors.Is(err, io.ErrUnexpectedEOF):
		return true
	}
	return false
}
