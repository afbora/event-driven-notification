package http_test

import (
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
)

// buildDocsTestRouter mounts the documentation handlers under /docs.
// The shape mirrors how cmd/api will wire them in production: a chi
// route group, no strict-server, no middleware chain — pure static
// serving.
func buildDocsTestRouter(t *testing.T) chi.Router {
	t.Helper()
	r := chi.NewRouter()
	httpadapter.MountDocs(r)
	return r
}

// TestDocs_SpecYAMLEndpoint: Swagger UI fetches the spec by URL, so
// the spec must be reachable at a stable path with a YAML
// content-type. The body contains the openapi version line and at
// least one of the operationIds we know is in the spec.
func TestDocs_SpecYAMLEndpoint(t *testing.T) {
	r := buildDocsTestRouter(t)

	req := httptest.NewRequest(nethttp.MethodGet, "/docs/openapi.yaml", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())

	ctype := rr.Header().Get("Content-Type")
	require.True(t, strings.Contains(ctype, "yaml") || strings.Contains(ctype, "text"),
		"yaml endpoint should set a yaml-ish content type, got %q", ctype)

	body := rr.Body.String()
	require.Contains(t, body, "openapi: 3.0.3",
		"served yaml must be the project openapi spec")
	require.Contains(t, body, "createNotification",
		"served yaml must include known operations (createNotification)")
}

// TestDocs_SwaggerUIPage: GET /docs returns an HTML page that loads
// Swagger UI and points it at the spec endpoint. We do not need to
// load real Swagger UI assets in the test — the markers below are
// enough to confirm the page is a working Swagger UI shell.
func TestDocs_SwaggerUIPage(t *testing.T) {
	r := buildDocsTestRouter(t)

	req := httptest.NewRequest(nethttp.MethodGet, "/docs", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code, "body=%s", rr.Body.String())
	require.Contains(t, rr.Header().Get("Content-Type"), "text/html")

	body := rr.Body.String()
	require.Contains(t, body, "swagger-ui",
		"docs page must mention swagger-ui so the bundle initializes")
	require.Contains(t, body, "/docs/openapi.yaml",
		"docs page must point swagger ui at the served spec")
}

// TestDocs_TrailingSlashServesSamePage: humans type `/docs/`
// reflexively; serving both `/docs` and `/docs/` keeps the UI from
// returning a 404 surprise.
func TestDocs_TrailingSlashServesSamePage(t *testing.T) {
	r := buildDocsTestRouter(t)

	req := httptest.NewRequest(nethttp.MethodGet, "/docs/", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, nethttp.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "swagger-ui")
}
