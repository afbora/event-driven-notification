package http_test

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// stubIDGen is a deterministic ports.IDGenerator implementation. Only
// NewCorrelationID is exercised here; the other methods return zero
// values so the type still satisfies the full port contract.
type stubIDGen struct {
	correlationID string
}

func (s *stubIDGen) NewNotificationID() domain.NotificationID { return "" }
func (s *stubIDGen) NewBatchID() domain.BatchID               { return "" }
func (s *stubIDGen) NewTemplateID() domain.TemplateID         { return "" }
func (s *stubIDGen) NewLogID() domain.LogID                   { return "" }
func (s *stubIDGen) NewCorrelationID() string                 { return s.correlationID }

// TestCorrelationID_HeaderPresent_Reused: when the inbound request
// carries X-Correlation-ID, the middleware adopts that value — no new
// ID is generated. The handler sees it via the context helper, and the
// response echoes it back.
func TestCorrelationID_HeaderPresent_Reused(t *testing.T) {
	const inbound = "01HXYZSAMPLECORRELATION0001"

	idGen := &stubIDGen{correlationID: "should-not-be-used"}
	mw := httpadapter.CorrelationIDMiddleware(idGen)

	var seenInHandler string
	handler := mw(nethttp.HandlerFunc(func(_ nethttp.ResponseWriter, r *nethttp.Request) {
		seenInHandler = httpadapter.CorrelationIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(nethttp.MethodGet, "/x", nil)
	req.Header.Set("X-Correlation-ID", inbound)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	require.Equal(t, inbound, seenInHandler, "handler must see the inbound id")
	require.Equal(t, inbound, rr.Header().Get("X-Correlation-ID"), "response must echo inbound id")
}

// TestCorrelationID_HeaderMissing_Generated: when the request omits
// X-Correlation-ID, the middleware asks the injected IDGenerator for a
// new one. The same id reaches the handler context and the response
// header.
func TestCorrelationID_HeaderMissing_Generated(t *testing.T) {
	const generated = "01HXYZGENERATED1234567890ID"

	idGen := &stubIDGen{correlationID: generated}
	mw := httpadapter.CorrelationIDMiddleware(idGen)

	var seenInHandler string
	handler := mw(nethttp.HandlerFunc(func(_ nethttp.ResponseWriter, r *nethttp.Request) {
		seenInHandler = httpadapter.CorrelationIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(nethttp.MethodGet, "/x", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	require.Equal(t, generated, seenInHandler, "handler must see generated id")
	require.Equal(t, generated, rr.Header().Get("X-Correlation-ID"), "response must echo generated id")
}

// TestCorrelationID_HeaderBlank_Generated: an empty X-Correlation-ID is
// treated the same as a missing one — clients sometimes send the header
// with no value and we must not propagate "" downstream as a correlation
// id.
func TestCorrelationID_HeaderBlank_Generated(t *testing.T) {
	const generated = "01HXYZGENERATEDONBLANKHDR01"

	idGen := &stubIDGen{correlationID: generated}
	mw := httpadapter.CorrelationIDMiddleware(idGen)

	var seenInHandler string
	handler := mw(nethttp.HandlerFunc(func(_ nethttp.ResponseWriter, r *nethttp.Request) {
		seenInHandler = httpadapter.CorrelationIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(nethttp.MethodGet, "/x", nil)
	req.Header.Set("X-Correlation-ID", "")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	require.Equal(t, generated, seenInHandler, "blank header must trigger generation")
	require.Equal(t, generated, rr.Header().Get("X-Correlation-ID"))
}

// TestCorrelationIDFromContext_Missing_ReturnsEmpty: the context helper
// is total — callers can use it without a presence check and get a
// safe empty string when no middleware ran (e.g. in unit tests of
// downstream handlers).
func TestCorrelationIDFromContext_Missing_ReturnsEmpty(t *testing.T) {
	got := httpadapter.CorrelationIDFromContext(context.Background())
	require.Equal(t, "", got)
}
