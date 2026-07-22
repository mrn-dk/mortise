package server

import (
	_ "embed"
	"net/http"
)

// openapiSpec is the API description served at /openapi.yaml and rendered by
// the Swagger UI page at /docs.
//
//go:embed openapi.yaml
var openapiSpec []byte

// swaggerUI is a self-contained Swagger UI page that loads the embedded spec
// from /openapi.yaml. The UI assets are pulled from a CDN at view time.
const swaggerUI = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1"/>
  <title>mortise API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css"/>
  <style>body{margin:0}</style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js" crossorigin></script>
  <script>
    window.onload = () => {
      window.ui = SwaggerUIBundle({
        url: 'openapi.yaml',
        dom_id: '#swagger-ui',
        deepLinking: true,
        presets: [SwaggerUIBundle.presets.apis],
      });
    };
  </script>
</body>
</html>`

// registerDocs wires the interactive API documentation:
//
//	GET /docs         Swagger UI (HTML)
//	GET /openapi.yaml the raw OpenAPI 3.0 specification
func (s *Server) registerDocs(mux *http.ServeMux) {
	mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(openapiSpec)
	})
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(swaggerUI))
	})
}
