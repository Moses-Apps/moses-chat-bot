package mosesclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Conversation mirrors the subset of types.ChatConversation that the
// bot cares about. The platform handler at chat_handlers.go:58 returns
// {"conversation": ChatConversation} on 201; we unwrap to the inner
// object for callers.
type Conversation struct {
	ID        uuid.UUID `json:"id"`
	UserID    uuid.UUID `json:"userId"`
	TenantID  uuid.UUID `json:"tenantId"`
	Title     *string   `json:"title,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CreateConversationOpts is the request shape POSTed to
// /api/v1/chat/conversations. The platform handler at
// chat_handlers.go:58 accepts {title, context} — `Context` here is a
// free-form object the platform stores as JSONB (chart_id, source
// tag, etc.).
type CreateConversationOpts struct {
	// Title — human-readable conversation title. Optional.
	Title *string

	// Source — an opaque tag the bot writes into Context so platform
	// audits can attribute conversations to the bridge. Stored under
	// the "source" key; e.g. "chat-bot-bridge".
	Source string

	// ChartID — optional chart UUID to scope the conversation to a
	// workspace. Stored under "chart_id" in Context.
	ChartID *uuid.UUID

	// ExtraContext lets callers attach arbitrary JSON-able key/value
	// pairs to the conversation context. Source + ChartID are added
	// on top of this (Source/ChartID win on key conflict).
	ExtraContext map[string]interface{}
}

// CreateConversation posts to POST /api/v1/chat/conversations and
// returns the created row. RBAC: USE AI on the moses-backend side.
func (c *Client) CreateConversation(ctx context.Context, opts CreateConversationOpts) (*Conversation, error) {
	contextMap := make(map[string]interface{}, len(opts.ExtraContext)+2)
	for k, v := range opts.ExtraContext {
		contextMap[k] = v
	}
	if opts.Source != "" {
		contextMap["source"] = opts.Source
	}
	if opts.ChartID != nil {
		contextMap["chart_id"] = opts.ChartID.String()
	}
	contextJSON, err := json.Marshal(contextMap)
	if err != nil {
		return nil, err
	}

	body := struct {
		Title   *string         `json:"title,omitempty"`
		Context json.RawMessage `json:"context,omitempty"`
	}{
		Title:   opts.Title,
		Context: contextJSON,
	}

	req, err := c.newRequest(ctx, http.MethodPost, "/api/v1/chat/conversations", body)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Conversation Conversation `json:"conversation"`
	}
	if err := c.doJSON(req, &envelope); err != nil {
		return nil, err
	}
	if envelope.Conversation.ID == uuid.Nil {
		return nil, errors.New("mosesclient: server returned empty conversation")
	}
	return &envelope.Conversation, nil
}

// isNotFound is a small helper that lets HTTP methods collapse a 404
// to nil without leaking the sentinel-check pattern.
func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
