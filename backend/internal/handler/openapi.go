// Package handler — OpenAPI spec endpoint.
//
// The platform's workspace-tool discovery reads /api/openapi.json on the bot's
// in-cluster service URL, parses each operationId, applies SanitizeToolName
// (replace non-alphanumeric with underscore), and surfaces the result as
// workspace_moses_chat_bot_<operationId> MCP tools.
//
// The spec is hand-written and lives next to this file so //go:embed can
// pick it up without a build tag. We pick this location (internal/handler/)
// over backend/api/ because:
//   - go:embed requires the file inside the same package directory
//   - any change to a handler is almost always paired with a spec change,
//     so keeping them adjacent reduces drift
package handler

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.json
var openapiJSON []byte

// OpenAPIHandler serves the embedded OpenAPI 3.0 spec verbatim. No tenant
// scoping, no auth — the spec describes the public surface and is safe to
// expose. Keep the response a byte-stream copy; do not re-marshal (would
// reorder keys and break OpenAPI tooling that diffs against the file).
func OpenAPIHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openapiJSON)
}
