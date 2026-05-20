// Package botconfig manages a tenant's in-app Telegram bot connection
// (moses-chat-bot-qcq).
//
// Telegram bots can ONLY be created by a human via @BotFather — there is no
// API or OAuth path. The in-app wizard therefore hands the user off to a
// t.me/botfather deep link and takes the resulting token back: this service
// validates that token (getMe), seals it under the per-tenant crypto envelope,
// registers the webhook + command menu with Telegram, and swaps the live
// in-process adapter so the bot starts relaying with no redeploy.
//
// Scoping: the bot configuration + token are TENANT-scoped (one row per
// tenant in telegram_bot_config). Connect/Disconnect are gated to tenant
// admins by the HTTP layer (middleware.RequireTenantAdmin); this service
// trusts the (tenantID, userID) it is handed.
package botconfig

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/google/uuid"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/provider"
	"moses-chat-bot/backend/internal/provider/telegram"
	"moses-chat-bot/backend/internal/service/crypto"
)

// Sentinel errors. The HTTP handler matches these with errors.Is to render
// the right status code / user-facing message.
var (
	// ErrEmptyToken is returned when Connect is called with a blank token.
	ErrEmptyToken = errors.New("botconfig: bot token is required")
	// ErrInvalidToken is returned when Telegram rejects the token at getMe
	// (revoked / malformed / wrong). It is a 400-class user error, not an
	// infrastructure failure.
	ErrInvalidToken = errors.New("botconfig: telegram rejected the bot token")
	// ErrNotConfigured is returned by Disconnect when the tenant has no bot.
	ErrNotConfigured = errors.New("botconfig: tenant has no telegram bot connected")
)

// webhookSecretBytes is the entropy of the generated setWebhook secret_token.
// 32 bytes → 64 hex chars, comfortably under the 128-char column and the
// 256-char limit Telegram imposes on secret_token.
const webhookSecretBytes = 32

// commandMenu is the slash-command menu registered with Telegram on connect.
// Mirrors SPEC §12; kept here so a connect refreshes the menu to the current
// command set.
var commandMenu = []telegram.BotCommand{
	{Command: "start", Description: "Welcome message and linking instructions"},
	{Command: "link", Description: "Link this chat to your Moses account with a code"},
	{Command: "unlink", Description: "Disconnect this chat from your Moses account"},
	{Command: "help", Description: "List available commands"},
	{Command: "tickets", Description: "Show your open tickets"},
	{Command: "status", Description: "Show platform / autopilot status"},
	{Command: "autopilot", Description: "Start, stop, or check an autopilot session"},
	{Command: "clear", Description: "Start a fresh Moses conversation"},
}

// Info is the tenant-readable bot status returned to the LinkNew page.
type Info struct {
	// Configured reports whether the tenant has a Telegram bot connected.
	Configured bool `json:"configured"`
	// Username is the bot's @username (without the @) when Configured; empty
	// otherwise.
	Username string `json:"username,omitempty"`
}

// adapterBuilder constructs a telegram.Adapter from a token + webhook secret.
// Indirected so tests can inject an adapter pointed at a stub Telegram server.
type adapterBuilder func(token, webhookSecret string) (*telegram.Adapter, error)

// Service owns the tenant Telegram bot lifecycle: persistence, encryption, the
// Telegram API hand-offs, and the live in-process adapter registration.
//
// v1 is single-tenant per deploy (one bot per Moses instance), so the Service
// keeps exactly one live adapter. The webhook handler resolves the active
// adapter through ActiveAdapter; multi-bot fan-out is a future bead.
type Service struct {
	store    *db.Store
	envelope *crypto.Envelope
	registry *provider.Registry
	logger   *slog.Logger

	build adapterBuilder

	mu      sync.RWMutex
	adapter *telegram.Adapter
}

// New constructs a Service. registry is the shared provider registry the relay
// sender reads; Connect swaps the telegram entry in it at runtime.
func New(store *db.Store, envelope *crypto.Envelope, registry *provider.Registry, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:    store,
		envelope: envelope,
		registry: registry,
		logger:   logger,
		build: func(token, webhookSecret string) (*telegram.Adapter, error) {
			return telegram.New(telegram.Config{
				BotToken:      token,
				WebhookSecret: webhookSecret,
				// AutoSetup stays false — botconfig drives setWebhook itself
				// with the URL derived from the connect request's Host header.
				AutoSetup: false,
				Logger:    nil,
			})
		},
	}
}

// SetAdapterBuilder overrides the adapter constructor. Tests use it to point the
// adapter (and its API client) at a stub Telegram server.
func (s *Service) SetAdapterBuilder(b adapterBuilder) { s.build = b }

// ActiveAdapter returns the live Telegram adapter, or nil when no bot is
// connected. The webhook handler calls this on every request so a connect /
// disconnect takes effect without a redeploy.
func (s *Service) ActiveAdapter() *telegram.Adapter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.adapter
}

// LoadAtStartup hydrates the live adapter from persisted state. It reads
// telegram_bot_config first; if a row exists its token is decrypted and the
// adapter is registered. envFallbackToken is the legacy TELEGRAM_BOT_TOKEN —
// used only when no DB row exists, to keep bootstrap / pre-wizard deploys
// working. Returns the registered adapter (may be nil when neither source is
// available).
func (s *Service) LoadAtStartup(ctx context.Context, envFallbackToken, envFallbackSecret string) (*telegram.Adapter, error) {
	configs, err := s.store.ListBotConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("botconfig: list configs: %w", err)
	}

	if len(configs) > 0 {
		// v1 is single-bot; if more than one tenant row exists (multi-tenant
		// future), the first is registered and the rest are logged. The
		// webhook handler still resolves per-request, so this is a startup
		// warm-up only.
		cfg := configs[0]
		if len(configs) > 1 {
			s.logger.Warn("botconfig: multiple tenant bot configs; only the first is registered at startup",
				slog.Int("count", len(configs)))
		}
		token, derr := s.envelope.Decrypt(cfg.TenantID, cfg.EncryptedToken, cfg.EncryptionKeyID)
		if derr != nil {
			s.logger.Error("botconfig: cannot decrypt stored bot token; bot not registered",
				slog.String("tenant_id", cfg.TenantID.String()),
				slog.String("err", derr.Error()))
			return nil, nil
		}
		adapter, berr := s.build(string(token), cfg.WebhookSecret)
		if berr != nil {
			return nil, fmt.Errorf("botconfig: build adapter from stored config: %w", berr)
		}
		if rerr := s.registry.Replace(adapter); rerr != nil {
			return nil, fmt.Errorf("botconfig: register stored adapter: %w", rerr)
		}
		s.mu.Lock()
		s.adapter = adapter
		s.mu.Unlock()
		s.logger.Info("botconfig: registered telegram bot from stored config",
			slog.String("tenant_id", cfg.TenantID.String()))
		return adapter, nil
	}

	// No DB row — fall back to the legacy env token for bootstrap deploys.
	envFallbackToken = strings.TrimSpace(envFallbackToken)
	if envFallbackToken == "" {
		return nil, nil
	}
	if strings.TrimSpace(envFallbackSecret) == "" {
		// The legacy adapter required an explicit webhook secret; generate one
		// when the env path omitted it so the adapter still validates.
		secret, gerr := generateWebhookSecret()
		if gerr != nil {
			return nil, gerr
		}
		envFallbackSecret = secret
	}
	adapter, berr := s.build(envFallbackToken, envFallbackSecret)
	if berr != nil {
		return nil, fmt.Errorf("botconfig: build adapter from env token: %w", berr)
	}
	if rerr := s.registry.Replace(adapter); rerr != nil {
		return nil, fmt.Errorf("botconfig: register env adapter: %w", rerr)
	}
	s.mu.Lock()
	s.adapter = adapter
	s.mu.Unlock()
	s.logger.Warn("botconfig: registered telegram bot from TELEGRAM_BOT_TOKEN env fallback (no DB config)")
	return adapter, nil
}

// Connect validates token via Telegram getMe, persists it encrypted, registers
// the webhook + command menu, and swaps the live adapter — all without a
// redeploy. webhookBaseURL is the scheme://host the webhook is reachable on,
// derived by the handler from the request Host header + MOSES_BASE_PATH; the
// webhook path suffix is appended here.
//
// On any failure after the getMe validation the persisted row is left untouched
// (we only upsert once the token is proven good), so a partial failure never
// leaves a tenant with a half-connected bot.
func (s *Service) Connect(ctx context.Context, tenantID, userID uuid.UUID, token, webhookBaseURL string) (*Info, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrEmptyToken
	}
	if tenantID == uuid.Nil || userID == uuid.Nil {
		return nil, errors.New("botconfig: tenant and user are required")
	}

	webhookSecret, err := generateWebhookSecret()
	if err != nil {
		return nil, err
	}

	adapter, err := s.build(token, webhookSecret)
	if err != nil {
		return nil, fmt.Errorf("botconfig: build adapter: %w", err)
	}
	api := adapter.API()

	// 1. Validate the token. getMe is the canonical check — a 4xx from
	//    Telegram means the token is bad; a network error is infrastructure.
	me, err := api.GetMe(ctx)
	if err != nil {
		if te, ok := telegram.AsTelegramError(err); ok {
			s.logger.Warn("botconfig: telegram rejected token at getMe",
				slog.String("tenant_id", tenantID.String()),
				slog.Int("telegram_code", te.Code()))
			return nil, ErrInvalidToken
		}
		return nil, fmt.Errorf("botconfig: getMe: %w", err)
	}

	// 2. Seal the token under the per-tenant envelope and persist.
	ciphertext, keyID, err := s.envelope.Encrypt(tenantID, []byte(token))
	if err != nil {
		return nil, fmt.Errorf("botconfig: encrypt token: %w", err)
	}
	botID := me.ID
	botUsername := me.Username
	if _, err := s.store.UpsertBotConfig(ctx, tenantID, ciphertext, keyID, &botID, &botUsername, webhookSecret, userID); err != nil {
		return nil, fmt.Errorf("botconfig: persist config: %w", err)
	}

	// 3. Register the webhook with Telegram. The row is already persisted; a
	//    setWebhook failure surfaces to the admin but the token is saved, so a
	//    retry of Connect re-runs setWebhook against the same config.
	target := strings.TrimRight(webhookBaseURL, "/") + telegram.WebhookPath()
	if err := api.SetWebhook(ctx, telegram.SetWebhookParams{
		URL:            target,
		SecretToken:    webhookSecret,
		AllowedUpdates: []string{"message"},
	}); err != nil {
		return nil, fmt.Errorf("botconfig: setWebhook: %w", err)
	}

	// 4. Register the command menu (best-effort — a failure here does not
	//    block the connection; the bot still relays without a menu).
	if err := api.SetMyCommands(ctx, telegram.SetMyCommandsParams{Commands: commandMenu}); err != nil {
		s.logger.Warn("botconfig: setMyCommands failed; bot connected without command menu",
			slog.String("tenant_id", tenantID.String()),
			slog.String("err", err.Error()))
	}

	// 5. Swap the live adapter so inbound + outbound start working now.
	if err := s.registry.Replace(adapter); err != nil {
		return nil, fmt.Errorf("botconfig: register adapter: %w", err)
	}
	s.mu.Lock()
	s.adapter = adapter
	s.mu.Unlock()

	s.logger.Info("botconfig: telegram bot connected",
		slog.String("tenant_id", tenantID.String()),
		slog.String("bot_username", botUsername))

	return &Info{Configured: true, Username: botUsername}, nil
}

// Disconnect tears down a tenant's Telegram bot: it best-effort deletes the
// webhook with Telegram, deletes the persisted config, and drops the live
// adapter. ErrNotConfigured is returned when nothing was connected.
func (s *Service) Disconnect(ctx context.Context, tenantID uuid.UUID) error {
	if tenantID == uuid.Nil {
		return errors.New("botconfig: tenant is required")
	}

	cfg, err := s.store.GetBotConfig(ctx, tenantID)
	if err != nil {
		if db.IsNoRows(err) {
			return ErrNotConfigured
		}
		return fmt.Errorf("botconfig: load config: %w", err)
	}

	// Best-effort deleteWebhook so Telegram stops POSTing to a bot we no
	// longer relay. A failure here is logged, not fatal.
	if token, derr := s.envelope.Decrypt(tenantID, cfg.EncryptedToken, cfg.EncryptionKeyID); derr == nil {
		if adapter, berr := s.build(string(token), cfg.WebhookSecret); berr == nil {
			if werr := adapter.API().DeleteWebhook(ctx); werr != nil {
				s.logger.Warn("botconfig: deleteWebhook failed during disconnect",
					slog.String("tenant_id", tenantID.String()),
					slog.String("err", werr.Error()))
			}
		}
	} else {
		s.logger.Warn("botconfig: cannot decrypt token for deleteWebhook; skipping",
			slog.String("tenant_id", tenantID.String()))
	}

	if err := s.store.DeleteBotConfig(ctx, tenantID); err != nil {
		return fmt.Errorf("botconfig: delete config: %w", err)
	}

	s.registry.Remove(telegram.ProviderName)
	s.mu.Lock()
	s.adapter = nil
	s.mu.Unlock()

	s.logger.Info("botconfig: telegram bot disconnected", slog.String("tenant_id", tenantID.String()))
	return nil
}

// Info reports whether the tenant has a Telegram bot connected and, when it
// does, the bot's @username. It is a tenant read — any member may call it (the
// LinkNew page needs it).
func (s *Service) Info(ctx context.Context, tenantID uuid.UUID) (*Info, error) {
	if tenantID == uuid.Nil {
		return nil, errors.New("botconfig: tenant is required")
	}
	cfg, err := s.store.GetBotConfig(ctx, tenantID)
	if err != nil {
		if db.IsNoRows(err) {
			return &Info{Configured: false}, nil
		}
		return nil, fmt.Errorf("botconfig: load config: %w", err)
	}
	out := &Info{Configured: true}
	if cfg.BotUsername != nil {
		out.Username = *cfg.BotUsername
	}
	return out, nil
}

// generateWebhookSecret returns a fresh hex-encoded secret for Telegram's
// setWebhook secret_token field.
func generateWebhookSecret() (string, error) {
	b := make([]byte, webhookSecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("botconfig: generate webhook secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}
