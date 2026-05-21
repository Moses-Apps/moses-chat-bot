package mosesclient

import (
	"context"
	"net/http"
)

// ChatStreamOpts is the request shape POSTed to
// /api/v1/ai/chat/stream. Mirror of types.ChatMessageRequest in
// moses-platform-prep but trimmed to the fields the bot uses.
type ChatStreamOpts struct {
	Message        string `json:"message"`
	ConversationID string `json:"conversationId,omitempty"`
	RequestID      string `json:"requestId,omitempty"`
	// ProviderOverride forces a specific provider key for this turn
	// (e.g. "anthropic-claude-sonnet-4-5"). Leave empty to use the
	// user's affinity / default cascade.
	ProviderOverride string `json:"provider_override,omitempty"`
	// MaxIterations bounds the agentic-loop iteration count (platform
	// default 50). 0 = platform default.
	MaxIterations int `json:"max_iterations,omitempty"`
}

// ChatStreamAck is what the platform returns on the immediate 200
// from POST /api/v1/ai/chat/stream. The body of the actual assistant
// turn does NOT flow through this response — it streams via the
// WebSocket at /api/v1/ai/ws. The caller is expected to have already
// subscribed to ConversationID before calling this method.
//
// See moses-platform-prep/backend/internal/api/ai_chat_handlers.go:367
// — the handler runs the agentic loop in a background goroutine and
// only returns {status, conversationId, requestId} on the HTTP hop.
type ChatStreamAck struct {
	Status         string `json:"status"`         // typically "processing"
	ConversationID string `json:"conversationId"` // may be server-assigned if request omitted it
	RequestID      string `json:"requestId"`      // may be server-assigned if request omitted it
}

// StreamChatMessage triggers a Moses Manager run.
//
// This is a fire-and-forget invocation: the method returns as soon as
// the platform acknowledges the request (HTTP 200, normally within
// milliseconds — see ChatStreamAck). The platform then runs the agentic
// loop in a server-side background goroutine on its own context, fully
// decoupled from this HTTP connection (ai_chat_handlers.go
// StreamChatMessage → processChatInBackground). The turn therefore runs
// to completion even though the caller consumes no stream and may
// disconnect immediately.
//
// The chat-bot relay relies on exactly that: it fires this to start a
// turn and never harvests output — Moses Manager delivers its reply by
// calling the chat-bot's `notifyLink` workspace tool. The streaming
// path is also the only path that routes Anthropic OAuth subscriptions;
// the synchronous /api/v1/ai/chat path 500s for them (CHAT-6j4in).
func (c *Client) StreamChatMessage(ctx context.Context, opts ChatStreamOpts) (*ChatStreamAck, error) {
	req, err := c.newRequest(ctx, http.MethodPost, "/api/v1/ai/chat/stream", opts)
	if err != nil {
		return nil, err
	}
	var ack ChatStreamAck
	if err := c.doJSON(req, &ack); err != nil {
		return nil, err
	}
	return &ack, nil
}
