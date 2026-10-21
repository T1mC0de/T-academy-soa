package app

import (
	"encoding/json"
	"net/http"

	"marketplace/internal/api"
)

// Страница Swagger UI. Сам Swagger UI загружается с CDN,
// а спецификацию берёт у нашего сервера по адресу /openapi.json.
const swaggerPage = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <title>Marketplace API</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
  <script>SwaggerUIBundle({ url: "/openapi.json", dom_id: "#swagger-ui" });</script>
</body>
</html>`

// GET /docs — страница Swagger UI.
func swaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(swaggerPage))
}

// GET /openapi.json — спецификация (она вшита в сгенерированный код).
func openAPISpec(w http.ResponseWriter, r *http.Request) {
	spec, err := api.GetSpec()
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(spec)
}
