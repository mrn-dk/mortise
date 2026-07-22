package server

import (
	"net/http"

	"github.com/mrn-dk/mortise/internal/server/docs"
)

// The OpenAPI spec is generated from the annotations on the handlers (see
// server.go and apidoc.go) by swaggo/swag. Regenerate after changing them:
//
//go:generate go run github.com/swaggo/swag/cmd/swag init -g cmd/mortise/main.go -o internal/server/docs --parseInternal

// swaggerUI is a self-contained Swagger UI page that loads the generated spec
// from /openapi.json. The UI assets are pulled from a CDN at view time.
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
        url: 'openapi.json',
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
//	GET /openapi.json the generated OpenAPI specification
func (s *Server) registerDocs(mux *http.ServeMux) {
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(docs.SwaggerInfo.ReadDoc()))
	})
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(swaggerUI))
	})
}
