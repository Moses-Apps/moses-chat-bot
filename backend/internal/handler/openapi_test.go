package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAPIHandler_ServesJSON_200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(OpenAPIHandler))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Greater(t, len(body), 100, "spec body looks too small")
}

func TestOpenAPISpec_ValidatesAsOpenAPI30(t *testing.T) {
	// Minimal sanity check: parse the embedded JSON and assert (a) it has an
	// "openapi" field starting with "3.0", and (b) every operationId from SPEC
	// §7 is present. Avoids pulling in kin-openapi as a dependency; this is
	// the lightest contract the workspace-tool proxy actually cares about.
	var doc map[string]interface{}
	require.NoError(t, json.Unmarshal(openapiJSON, &doc))

	version, ok := doc["openapi"].(string)
	require.True(t, ok, "openapi field missing or not a string")
	require.True(t, strings.HasPrefix(version, "3.0"), "expected OpenAPI 3.0.x, got %s", version)

	paths, ok := doc["paths"].(map[string]interface{})
	require.True(t, ok, "paths missing or wrong type")

	wantOps := map[string]string{
		"/api/v1/push/message":                  "pushMessage",
		"/api/v1/workspace/links":               "listLinks",
		"/api/v1/workspace/links/{id}/notify":   "notifyLink",
		"/api/v1/workspace/messages":            "listRecentMessages",
	}
	for path, wantOp := range wantOps {
		raw, ok := paths[path]
		require.True(t, ok, "path %q missing from spec", path)
		methods := raw.(map[string]interface{})

		var gotOp string
		for _, m := range methods {
			op := m.(map[string]interface{})
			if id, ok := op["operationId"].(string); ok {
				gotOp = id
				break
			}
		}
		require.Equal(t, wantOp, gotOp, "wrong operationId on %q", path)
	}

	// Components.schemas — verify each schema SPEC §7 specifies is defined.
	components := doc["components"].(map[string]interface{})
	schemas := components["schemas"].(map[string]interface{})
	wantSchemas := []string{
		"PushMessageRequest", "PushMessageResponse",
		"LinkSummary", "LinksResponse",
		"NotifyRequest", "NotifyResponse",
		"MessageSummary", "MessagesResponse",
	}
	for _, s := range wantSchemas {
		_, ok := schemas[s]
		require.True(t, ok, "schema %q missing", s)
	}
}
