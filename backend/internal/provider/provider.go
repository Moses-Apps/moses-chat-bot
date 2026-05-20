// Package provider defines the chat-provider abstraction used by
// moses-chat-bot to bridge external messengers (Telegram, Discord, Slack, ...)
// to the Moses Manager relay layer.
//
// Each provider is a plug-in that implements Provider and is registered in
// a Registry at process startup. The relay layer is provider-agnostic: it
// consumes InboundMessage values and emits OutboundMessage values, leaving
// transport, signature verification, and webhook lifecycle to the adapter.
package provider

import (
	"context"
	"net/http"
	"time"
)

// Provider is implemented by each chat-provider adapter (Telegram, etc.).
//
// All methods are called concurrently from multiple goroutines and must be
// safe for that. HandleWebhook is the only ingress point; SendMessage the
// only egress. SetupWebhook is idempotent so it can be invoked on every boot
// without leaving duplicate registrations upstream.
type Provider interface {
	Name() string

	HandleWebhook(ctx context.Context, body []byte, headers http.Header) ([]InboundMessage, error)

	SendMessage(ctx context.Context, chat ChatRef, msg OutboundMessage) error

	// SetupWebhook configures the upstream provider to deliver events to
	// baseURL. Implementations must be idempotent. Callers gate invocation
	// via env (BOT_WEBHOOK_AUTO_SETUP) so this is never run accidentally
	// from a test or local boot.
	SetupWebhook(ctx context.Context, baseURL string) error

	// VerifyWebhookSignature returns ErrSignatureInvalid if the signature
	// or secret header does not authenticate the body. It must be called
	// before any side effects driven by the body.
	VerifyWebhookSignature(headers http.Header, body []byte) error
}

// InboundMessage is the provider-neutral shape passed to the relay layer.
// RawJSON is retained for audit logs; the relay never parses it.
type InboundMessage struct {
	Provider          string
	ProviderUserID    string
	ProviderChatID    string
	Text              string
	Attachments       []Attachment
	ReceivedAt        time.Time
	ProviderMessageID string
	RawJSON           []byte
}

// OutboundMessage is what the relay asks the adapter to deliver. Markdown
// is a hint; adapters that cannot render it must degrade to plain text.
// ReplyToID is the adapter-native id of the message being replied to,
// blank when not threading.
type OutboundMessage struct {
	Text      string
	Markdown  bool
	ReplyToID string
}

// ChatRef addresses a 1:1 chat with a specific user on a specific provider.
type ChatRef struct {
	Provider       string
	ProviderChatID string
}

// Attachment describes a media payload received from the upstream provider.
// URL is provider-hosted; callers resolve to bytes only when they actually
// need the content (e.g. Whisper transcription for voice notes).
type Attachment struct {
	Kind     string
	URL      string
	MimeType string
	Caption  string
}
