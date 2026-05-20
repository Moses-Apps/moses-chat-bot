package telegram

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"moses-chat-bot/backend/internal/provider"
)

// ProviderName is the stable identifier this adapter registers under.
const ProviderName = "telegram"

// telegramSecretHeader is the canonical-case header Telegram uses when it
// echoes back the secret_token we configured at setWebhook time.
const telegramSecretHeader = "X-Telegram-Bot-Api-Secret-Token"

// webhookPath is the suffix appended to baseURL by SetupWebhook. Kept as a
// package constant so tests can assert on the exact registered URL.
const webhookPath = "/api/v1/providers/telegram/webhook"

// maxMessageLength is the per-message Telegram cap. Outbound text is
// chunked at this byte boundary before sending.
const maxMessageLength = 4096

// rateLimitBudget caps how long a single SendMessage call is allowed to
// wait across cumulative 429 Retry-After hints before giving up.
const rateLimitBudget = 30 * time.Second

// Config is the user-supplied configuration for the Telegram adapter.
// Validation happens in New; zero-value fields produce a clear error.
type Config struct {
	BotToken      string
	WebhookSecret string
	// PublicURL is optional; SetupWebhook prefers its own baseURL argument.
	// When AutoSetup is true and SetupWebhook is invoked without a baseURL,
	// PublicURL is the fallback.
	PublicURL string
	AutoSetup bool
	// HTTPClient lets tests inject a httptest.Server-backed client.
	HTTPClient *http.Client
	// BaseURL overrides the default Telegram API host. Tests set this to
	// the URL of a httptest.Server impersonating api.telegram.org.
	BaseURL string
	// Logger is optional; when nil, log.Default() is used.
	Logger *log.Logger
}

// Adapter implements provider.Provider for Telegram.
type Adapter struct {
	botToken      string
	webhookSecret string
	publicURL     string
	autoSetup     bool
	api           *APIClient
	logger        *log.Logger
}

// Compile-time assertion: *Adapter satisfies provider.Provider.
var _ provider.Provider = (*Adapter)(nil)

// New validates cfg and returns a ready-to-use Adapter. Returns a wrapped
// error when token or secret are empty so the caller can fail fast at boot.
func New(cfg Config) (*Adapter, error) {
	if strings.TrimSpace(cfg.BotToken) == "" {
		return nil, errors.New("telegram: BotToken is required")
	}
	if strings.TrimSpace(cfg.WebhookSecret) == "" {
		return nil, errors.New("telegram: WebhookSecret is required")
	}

	client := NewAPIClient(cfg.BotToken, cfg.HTTPClient)
	if cfg.BaseURL != "" {
		client.baseURL = strings.TrimRight(cfg.BaseURL, "/")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}

	return &Adapter{
		botToken:      cfg.BotToken,
		webhookSecret: cfg.WebhookSecret,
		publicURL:     strings.TrimRight(cfg.PublicURL, "/"),
		autoSetup:     cfg.AutoSetup,
		api:           client,
		logger:        logger,
	}, nil
}

// Name returns the stable provider identifier.
func (a *Adapter) Name() string { return ProviderName }

// API exposes the underlying typed Bot API client. The botconfig service uses
// it to validate a token (getMe) and register the webhook + command menu when
// a tenant admin connects a bot through the in-app wizard.
func (a *Adapter) API() *APIClient { return a.api }

// WebhookSecret returns the secret_token this adapter expects Telegram to echo
// in the X-Telegram-Bot-Api-Secret-Token header.
func (a *Adapter) WebhookSecret() string { return a.webhookSecret }

// WebhookPath returns the route suffix the webhook is mounted at, so callers
// can derive the full setWebhook URL without importing the constant.
func WebhookPath() string { return webhookPath }

// VerifyWebhookSignature checks the X-Telegram-Bot-Api-Secret-Token header
// against the configured webhook secret in constant time. The body is
// unused but accepted to match the interface contract.
func (a *Adapter) VerifyWebhookSignature(headers http.Header, _ []byte) error {
	got := headers.Get(telegramSecretHeader)
	if got == "" {
		return provider.ErrSignatureInvalid
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(a.webhookSecret)) != 1 {
		return provider.ErrSignatureInvalid
	}
	return nil
}

// HandleWebhook decodes a Telegram Update and converts it into zero or one
// InboundMessage. Service updates (non-message types) yield an empty slice
// rather than an error so callers can no-op cleanly.
func (a *Adapter) HandleWebhook(_ context.Context, body []byte, _ http.Header) ([]provider.InboundMessage, error) {
	var update Update
	if err := json.Unmarshal(body, &update); err != nil {
		return nil, fmt.Errorf("telegram: decode update: %w", err)
	}
	if update.Message == nil {
		// Edited messages, channel posts, callback queries, etc. are not
		// supported in v1; surface as empty rather than as an error.
		return nil, nil
	}

	msg := update.Message
	var providerUserID string
	if msg.From != nil {
		providerUserID = strconv.FormatInt(msg.From.ID, 10)
	}

	text := msg.Text
	if text == "" {
		// Photos / documents may carry a caption instead of text.
		text = msg.Caption
	}

	attachments := extractAttachments(msg)

	in := provider.InboundMessage{
		Provider:          ProviderName,
		ProviderUserID:    providerUserID,
		ProviderChatID:    strconv.FormatInt(msg.Chat.ID, 10),
		Text:              text,
		Attachments:       attachments,
		ReceivedAt:        time.Now().UTC(),
		ProviderMessageID: strconv.FormatInt(update.UpdateID, 10),
		RawJSON:           append([]byte(nil), body...),
	}
	return []provider.InboundMessage{in}, nil
}

// extractAttachments translates Telegram media payloads to provider.Attachment.
// v1 does NOT resolve file_id to a public URL (a follow-up getFile call) — the
// file_id is stashed in Caption so downstream code can opt into resolution.
func extractAttachments(msg *Message) []provider.Attachment {
	var out []provider.Attachment

	if len(msg.Photo) > 0 {
		// Telegram orders PhotoSize from smallest to largest; pick the
		// last entry for the highest-resolution representation.
		largest := msg.Photo[len(msg.Photo)-1]
		out = append(out, provider.Attachment{
			Kind:    "photo",
			URL:     "",
			Caption: largest.FileID,
		})
	}
	if msg.Voice != nil {
		out = append(out, provider.Attachment{
			Kind:     "voice",
			URL:      "",
			MimeType: msg.Voice.MimeType,
			Caption:  msg.Voice.FileID,
		})
	}
	if msg.Document != nil {
		out = append(out, provider.Attachment{
			Kind:     "document",
			URL:      "",
			MimeType: msg.Document.MimeType,
			Caption:  msg.Document.FileID,
		})
	}
	return out
}

// SendMessage delivers msg to the Telegram chat referenced by chat. Long
// messages are chunked at 4096 bytes and sent sequentially; the first
// error short-circuits subsequent chunks.
//
// Markdown handling (v1): when msg.Markdown is true we do NOT set
// parse_mode. Telegram's MarkdownV2 requires escaping a long list of
// reserved characters; emitting unescaped content yields a 400 from the
// API. Until a proper escaper is in place we opt to send the literal text
// rather than risk a hard failure. Tracked for v1.1.
func (a *Adapter) SendMessage(ctx context.Context, chat provider.ChatRef, msg provider.OutboundMessage) error {
	if chat.ProviderChatID == "" {
		return errors.New("telegram: SendMessage: empty ProviderChatID")
	}

	chunks := Chunk(msg.Text, maxMessageLength)
	if len(chunks) == 0 {
		// Nothing to send; treat as a no-op rather than an error.
		return nil
	}

	var waited time.Duration
	for _, chunk := range chunks {
		params := SendMessageParams{
			ChatID: chat.ProviderChatID,
			Text:   chunk,
		}

		// Retry loop: a single chunk may collide with the per-chat rate
		// limit and require a Retry-After sleep before succeeding.
		for {
			err := a.api.SendMessage(ctx, params)
			if err == nil {
				break
			}

			if retry, ok := IsRateLimited(err); ok {
				waited += retry
				if waited > rateLimitBudget {
					return fmt.Errorf("%w: cumulative wait %s exceeded budget", provider.ErrRateLimited, waited)
				}
				select {
				case <-time.After(retry):
					continue
				case <-ctx.Done():
					return ctx.Err()
				}
			}

			// All other errors (network, 4xx other than 429) flow through.
			return err
		}
	}
	return nil
}

// SetupWebhook is the idempotent webhook-registration entry point. It is
// no-op-by-default: only when Config.AutoSetup is true does it touch the
// Bot API. This guards against CI / stale-token deploys clobbering a
// production webhook configuration.
func (a *Adapter) SetupWebhook(ctx context.Context, baseURL string) error {
	if !a.autoSetup {
		a.logger.Printf("telegram: webhook auto-setup disabled; set BOT_WEBHOOK_AUTO_SETUP=true to register")
		return nil
	}

	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = a.publicURL
	}
	if base == "" {
		return errors.New("telegram: SetupWebhook: baseURL is required when AutoSetup is enabled")
	}

	target := base + webhookPath

	// Pre-check: skip if the URL is already what we'd set. Telegram does
	// not return the secret_token through getWebhookInfo, so we cannot
	// verify it remotely — URL parity is the best we can do.
	info, err := a.api.GetWebhookInfo(ctx)
	if err == nil && info != nil && info.URL == target {
		a.logger.Printf("telegram: webhook already registered for %s; skipping setWebhook", target)
		return nil
	}

	return a.api.SetWebhook(ctx, SetWebhookParams{
		URL:            target,
		SecretToken:    a.webhookSecret,
		AllowedUpdates: []string{"message"},
	})
}
