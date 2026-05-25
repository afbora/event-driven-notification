package http

import (
	// embed is imported for its side effect of enabling the //go:embed
	// directive on openapiSpecYAML below. Required by Go's compiler to
	// recognize the //go:embed comment as a directive rather than a
	// regular comment.
	_ "embed"
	nethttp "net/http"

	"github.com/go-chi/chi/v5"
)

// openapiSpecYAML embeds the openapi.yaml at build time so the
// running binary serves the same spec the codegen consumed. No file
// IO at startup, no chance of the served spec drifting from the
// generated code.
//
//go:embed openapi.yaml.embed
var openapiSpecYAML []byte

// swaggerUIPage is a minimal HTML shell that loads the Swagger UI
// bundle from a CDN and points it at the embedded spec endpoint.
// Pinned to a specific version (5.17.14) so the rendered docs do not
// drift between deploys when upstream ships a UI redesign.
const swaggerUIPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Notification System API · Swagger UI</title>
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui.css">
</head>
<body>
<div id="swagger-ui"></div>
<script src="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui-bundle.js" crossorigin></script>
<script>
window.addEventListener("load", function () {
  window.ui = SwaggerUIBundle({
    url: "/docs/openapi.yaml",
    dom_id: "#swagger-ui",
    deepLinking: true,
  });
});
</script>
</body>
</html>
`

// MountDocs registers the documentation handlers on the supplied
// router:
//
//   - GET /docs and /docs/ → Swagger UI HTML shell.
//   - GET /docs/openapi.yaml → the embedded spec as text/yaml.
//
// Mounted outside the strict-server because OpenAPI 3.0 does not
// describe its own documentation surface — the page is a static asset
// in spirit, even though one byte of it is the spec embedded at build.
func MountDocs(r chi.Router) {
	r.Get("/docs", serveSwaggerUI)
	r.Get("/docs/", serveSwaggerUI)
	r.Get("/docs/openapi.yaml", serveSpecYAML)
}

func serveSwaggerUI(w nethttp.ResponseWriter, _ *nethttp.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(nethttp.StatusOK)
	_, _ = w.Write([]byte(swaggerUIPage))
}

func serveSpecYAML(w nethttp.ResponseWriter, _ *nethttp.Request) {
	// application/yaml is the IANA-registered media type
	// (RFC 9512 §4.1); Swagger UI accepts both yaml and text/plain.
	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(nethttp.StatusOK)
	_, _ = w.Write(openapiSpecYAML)
}
