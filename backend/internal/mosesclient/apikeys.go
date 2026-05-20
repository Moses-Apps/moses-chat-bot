package mosesclient

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// RevokeAPIKey revokes a moses-backend MCP API key by its UUID.
//
// SPEC §4 (key revocation cycle): the chat-bot does NOT mint user keys
// — the frontend does that directly via cookie auth, since the
// platform's /api/v1/api-keys POST handler is not reachable through
// the iframe-SDK proxy (platform_action_dispatcher.go only knows
// chat_prompt + launch_agent). The bot DOES revoke on /unlink (best
// effort) and the moses-backend route lives at:
//
//	DELETE /api/v1/api-keys/:keyId    (routes_admin.go:279)
//
// 404 is collapsed to nil — the key was already gone, which is a fine
// terminal state for a best-effort revoke. Other errors are surfaced
// as typed *APIError values.
func (c *Client) RevokeAPIKey(ctx context.Context, keyID uuid.UUID) error {
	req, err := c.newRequest(ctx, http.MethodDelete, "/api/v1/api-keys/"+keyID.String(), nil)
	if err != nil {
		return err
	}
	err = c.doJSON(req, nil)
	if err == nil {
		return nil
	}
	if isNotFound(err) {
		return nil
	}
	return err
}
