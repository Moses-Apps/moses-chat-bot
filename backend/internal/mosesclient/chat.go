package mosesclient

import (
	"context"
	"net/http"
	"time"
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
// IMPORTANT: this method returns as soon as the platform acknowledges
// the request (HTTP 200, normally within milliseconds). The actual
// assistant output flows back over the WebSocket
// (/api/v1/ai/ws?token=...) as assistant_message_chunk events that
// the caller aggregates via WSSubscriber.
//
// The caller is responsible for subscribing to the conversation
// BEFORE calling this method, to avoid losing early chunks. The
// platform also pre-subscribes the user's existing WS connections via
// AIHandler.StreamChatMessage → wsHandler.SubscribeUserToConversation
// (ai_chat_handlers.go:425) as defence-in-depth.
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

// ChatSyncOpts is the request shape POSTed to /api/v1/ai/chat (the
// non-streaming fallback). Same fields as ChatStreamOpts.
type ChatSyncOpts = ChatStreamOpts

// ChatSyncResponse mirrors types.ChatMessageResponse on the platform
// side: the full assistant turn returned synchronously when streaming
// is unavailable. Used as a degraded-mode fallback when the bot
// cannot hold a WebSocket open.
type ChatSyncResponse struct {
	Message     string    `json:"message"`
	Role        string    `json:"role"`
	Provider    string    `json:"provider,omitempty"`
	Model       string    `json:"model,omitempty"`
	Timestamp   string    `json:"timestamp,omitempty"`
	ReceivedAt  time.Time `json:"-"` // client-side stamp for latency tracking
	ToolCalls   int       `json:"tool_calls,omitempty"`
}

// SendChatMessageSync calls POST /api/v1/ai/chat and waits for the
// complete assistant turn. Use only when streaming is unavailable —
// long agentic loops may exceed DefaultTimeout (30s).
func (c *Client) SendChatMessageSync(ctx context.Context, opts ChatSyncOpts) (*ChatSyncResponse, error) {
	req, err := c.newRequest(ctx, http.MethodPost, "/api/v1/ai/chat", opts)
	if err != nil {
		return nil, err
	}
	var resp ChatSyncResponse
	if err := c.doJSON(req, &resp); err != nil {
		return nil, err
	}
	resp.ReceivedAt = time.Now()
	return &resp, nil
}
