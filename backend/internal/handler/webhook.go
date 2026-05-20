package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"moses-chat-bot/backend/internal/provider"
	"moses-chat-bot/backend/internal/service/relay"
)

// WebhookConfig configures a single provider's webhook endpoint.
type WebhookConfig struct {
	// Provider is the adapter (Telegram, Discord, …) used to verify the
	// signature and decode the body into InboundMessages.
	//
	// Either Provider or ResolveProvider must be set. ResolveProvider takes
	// precedence: it is the dynamic path (moses-chat-bot-qcq) where the live
	// adapter can be (re)connected at runtime by a tenant admin without a
	// redeploy. Provider stays as the static path for tests.
	Provider provider.Provider

	// ResolveProvider returns the currently active adapter, or nil when no bot
	// is connected. Called on every request so a connect / disconnect takes
	// effect immediately. When it returns nil the handler responds 503 — the
	// webhook route is mounted unconditionally so Telegram does not see a 404
	// during the window before the first connect.
	ResolveProvider func() provider.Provider

	// Inbound runs the relay pipeline for each decoded InboundMessage.
	Inbound *relay.Inbound

	// MaxConcurrent caps in-flight dispatch goroutines. Telegram bursts a
	// single update at a time per chat, but admin operations (resync,
	// retries of pending updates) can pile up. Default 32. A negative or
	// zero value resolves to the default; the semaphore is opt-out only
	// via MaxConcurrent < 0 → unbounded (NOT recommended).
	MaxConcurrent int

	// MaxBodyBytes is the cap on the webhook body to prevent OOM from a
	// malicious caller. Default 1 MiB — Telegram updates are typically
	// well under 100 KiB even with attachments.
	MaxBodyBytes int64

	// Logger is required; pass slog.Default() if you have no specific one.
	Logger *slog.Logger

	// BackgroundContext is the parent ctx that survives the HTTP request
	// scope — relay dispatch outlives the webhook handler because we
	// return 200 before MM finishes. Default context.Background().
	BackgroundContext context.Context
}

// WebhookHandler is the HTTP handler bound to POST /api/v1/providers/<name>/webhook.
type WebhookHandler struct {
	cfg WebhookConfig
	sem chan struct{}
}

// NewWebhookHandler wires up the handler. The semaphore is created here
// so RunHandler tests can hit it without coordinating with main.
func NewWebhookHandler(cfg WebhookConfig) *WebhookHandler {
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = 32
	}
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = 1 << 20
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.BackgroundContext == nil {
		cfg.BackgroundContext = context.Background()
	}
	var sem chan struct{}
	if cfg.MaxConcurrent > 0 {
		sem = make(chan struct{}, cfg.MaxConcurrent)
	}
	return &WebhookHandler{cfg: cfg, sem: sem}
}

// ServeHTTP implements http.Handler.
//
// Flow:
//  1. Read body (capped).
//  2. Verify provider signature; 401 on mismatch.
//  3. Decode → []InboundMessage.
//  4. Dispatch each via the semaphore-bounded goroutine pool.
//  5. Return 200 to the provider immediately — Telegram retries on non-2xx,
//     and the relay handles dedup downstream, so we never stall the ack.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Resolve the active adapter. ResolveProvider is the dynamic path: a
	// tenant admin may not have connected a bot yet, in which case there is no
	// adapter and we 503 (the route stays mounted so Telegram never 404s).
	prov := h.cfg.Provider
	if h.cfg.ResolveProvider != nil {
		prov = h.cfg.ResolveProvider()
	}
	if prov == nil {
		http.Error(w, "no telegram bot connected", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, h.cfg.MaxBodyBytes))
	if err != nil {
		h.cfg.Logger.Warn("webhook: read body failed", slog.String("err", err.Error()))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if err := prov.VerifyWebhookSignature(r.Header, body); err != nil {
		if errors.Is(err, provider.ErrSignatureInvalid) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.cfg.Logger.Warn("webhook: verify failed", slog.String("err", err.Error()))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	messages, err := prov.HandleWebhook(r.Context(), body, r.Header)
	if err != nil {
		// We deliberately return 200 even on decode errors so the
		// provider does not infinitely retry a malformed payload —
		// Telegram especially will retry forever. Log loudly so the
		// drift is visible.
		h.cfg.Logger.Warn("webhook: decode failed",
			slog.String("provider", prov.Name()),
			slog.String("err", err.Error()),
		)
		w.WriteHeader(http.StatusOK)
		return
	}

	for _, msg := range messages {
		m := msg
		// Acquire the semaphore. If the buffer is full, drop the
		// message (Telegram will resend on its own retry timer).
		if h.sem != nil {
			select {
			case h.sem <- struct{}{}:
			default:
				h.cfg.Logger.Warn("webhook: semaphore full; dropping message",
					slog.String("provider_message_id", m.ProviderMessageID),
				)
				continue
			}
		}
		go func() {
			if h.sem != nil {
				defer func() { <-h.sem }()
			}
			// Use the background context so SIGTERM-driven request
			// cancel doesn't kill an in-flight MM dispatch.
			if err := h.cfg.Inbound.HandleInbound(h.cfg.BackgroundContext, m); err != nil {
				h.cfg.Logger.Warn("webhook: HandleInbound returned error",
					slog.String("provider_message_id", m.ProviderMessageID),
					slog.String("err", err.Error()),
				)
			}
		}()
	}

	w.WriteHeader(http.StatusOK)
}
