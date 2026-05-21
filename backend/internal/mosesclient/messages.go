package mosesclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// ChatMessage mirrors the subset of types.ChatMessage that the bot reads
// when polling a conversation for a persisted Moses Manager turn reply.
//
// The platform persists every turn — user prompt and assistant answer —
// to chat_messages as a side-effect of running the agentic loop in
// processChatInBackground (ai_chat_handlers.go). The chat-bot relay
// polls GetConversationMessages after firing StreamChatMessage and picks
// the first assistant message newer than its pre-turn baseline.
//
// Full shape lives in moses-platform-prep/backend/internal/types/types.go
// (type ChatMessage). Role is one of "user" | "assistant" | "system".
type ChatMessage struct {
	ID             uuid.UUID       `json:"id"`
	ConversationID uuid.UUID       `json:"conversationId"`
	Role           string          `json:"role"`
	Content        string          `json:"content"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// GetConversationMessages calls
// GET /api/v1/chat/conversations/:id/messages?limit=N.
//
// The platform returns the messages in chronological order (oldest
// first, newest last — see store.GetRecentChatMessages, which reorders a
// DESC-limited subquery back to ASC). When limit <= 0 it is omitted and
// the platform returns the full history.
//
// RBAC: USE AI on the moses-backend side, plus per-conversation tenant
// isolation enforced by the handler. A revoked key surfaces as
// ErrUnauthorized; an unknown conversation as ErrNotFound.
func (c *Client) GetConversationMessages(ctx context.Context, conversationID uuid.UUID, limit int) ([]ChatMessage, error) {
	path := "/api/v1/chat/conversations/" + conversationID.String() + "/messages"
	if limit > 0 {
		path += "?" + url.Values{"limit": {strconv.Itoa(limit)}}.Encode()
	}
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Messages []ChatMessage `json:"messages"`
	}
	if err := c.doJSON(req, &envelope); err != nil {
		return nil, err
	}
	return envelope.Messages, nil
}
