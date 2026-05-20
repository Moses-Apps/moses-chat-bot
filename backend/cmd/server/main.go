// Command server boots the moses-chat-bot HTTP service.
//
// As of T-RELAY-1 the inbound relay is wired up: the Telegram adapter
// registers itself in the provider registry, the webhook endpoint
// /api/v1/providers/telegram/webhook accepts updates publicly (signature
// verified via X-Telegram-Bot-Api-Secret-Token), and inbound messages
// drive the Moses Manager streaming bridge with response chunks
// aggregated over a per-link WS subscription.
//
// Push API + workspace-tool OpenAPI (T-PUSH-1) are already wired below.
package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/handler"
	"moses-chat-bot/backend/internal/handler/middleware"
	"moses-chat-bot/backend/internal/mosesclient"
	"moses-chat-bot/backend/internal/provider"
	"moses-chat-bot/backend/internal/service/autopilot"
	"moses-chat-bot/backend/internal/service/botconfig"
	"moses-chat-bot/backend/internal/service/crypto"
	"moses-chat-bot/backend/internal/service/linker"
	"moses-chat-bot/backend/internal/service/relay"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	masterKeys, err := crypto.LoadMasterKeysFromEnv()
	if err != nil {
		log.Fatalf("master keys: %v", err)
	}
	envelope, err := crypto.NewEnvelope(masterKeys)
	if err != nil {
		log.Fatalf("envelope: %v", err)
	}

	pool, err := db.Open(ctx, "")
	if err != nil {
		log.Fatalf("db open: %v", err)
	}
	defer db.Close(pool)

	if err := db.ApplySchema(ctx, pool); err != nil {
		log.Fatalf("schema apply: %v", err)
	}
	store := db.NewStore(pool)

	mosesBase := os.Getenv("MOSES_PLATFORM_API_URL")
	if mosesBase == "" {
		mosesBase = "http://moses-backend.moses.svc.cluster.local:8080"
	}
	platformKey := os.Getenv("MOSES_PLATFORM_API_KEY")
	var auth mosesclient.Auth
	if platformKey != "" {
		auth = mosesclient.BearerAuth{Token: platformKey}
	}
	mosesClient := mosesclient.NewClient(mosesBase, auth)

	link := linker.New(store, envelope, mosesClient)
	link.StartCleanupSweeper(ctx)

	// Provider registry + relay sender wire the workspace-tool push surface.
	// v1 ships Telegram only; the registry is left empty when no adapters are
	// configured so the push endpoints still respond (with sent=false errors
	// per link) — useful for staging environments without a bot token.
	providerRegistry := provider.NewRegistry()
	sender := relay.NewSender(store, providerRegistry, relay.SenderOpts{})
	go sender.Bucket().Run(ctx)

	// Telegram bot configuration (moses-chat-bot-qcq). The bot token is now
	// stored encrypted per-tenant in telegram_bot_config and set via the
	// in-app admin "Connect Telegram" wizard. LoadAtStartup hydrates the live
	// adapter from that table; the TELEGRAM_BOT_TOKEN env var remains a
	// bootstrap/legacy fallback used only when no DB row exists.
	//
	// The webhook route is mounted unconditionally below — when no bot is
	// connected the handler returns 503 rather than 404, so Telegram never
	// sees a missing endpoint during the pre-connect window.
	botConfigSvc := botconfig.New(store, envelope, providerRegistry, logger)
	if _, err := botConfigSvc.LoadAtStartup(ctx, os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_WEBHOOK_SECRET")); err != nil {
		log.Fatalf("telegram bot config load: %v", err)
	}
	if botConfigSvc.ActiveAdapter() == nil {
		logger.Warn("no telegram bot connected; a tenant admin can connect one via the in-app wizard")
	}

	// WS pool for the inbound relay: one persistent socket per linked user
	// (the WS handshake URL embeds the user's MCP key, so we cannot share
	// one connection across users). Idle conns reaped by RunSweeper every
	// minute; per-link IdleTTL=10m.
	wsPool := relay.NewWSConnPool(relay.WSPoolConfig{
		BaseWS:  mosesBase,
		IdleTTL: 10 * time.Minute,
	})
	go wsPool.RunSweeper(ctx, 1*time.Minute)
	defer wsPool.Stop()

	// ChatClientFactory builds a *mosesclient.Client carrying the bearer
	// for one link's API key. Reuses the http.Client's default transport
	// pool across factory invocations.
	chatFactory := func(bearer string) relay.PerKeyChatClient {
		return mosesclient.NewClient(mosesBase, mosesclient.BearerAuth{Token: bearer})
	}

	inbound := relay.NewInbound(
		store, sender, envelope, link, providerRegistry, chatFactory, wsPool,
		relay.InboundOpts{
			StreamTimeout: streamTimeoutFromEnv(),
			Logger:        logger,
		},
	)

	// Autopilot service: per-user platform calls (Start/Stop/Status) plus a
	// 60s sweeper that reconciles terminal sessions back into chat-state.
	autopilotFactory := func(bearer string) autopilot.MosesClient {
		return mosesclient.NewClient(mosesBase, mosesclient.BearerAuth{Token: bearer})
	}
	autopilotSvc := autopilot.New(store, autopilotFactory, envelope, sender, logger)
	inbound.Autopilot = autopilotSvc
	go autopilotSvc.StartSweeper(ctx, 60*time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/api/openapi.json", handler.OpenAPIHandler)

	// Webhook endpoint — publicly accessible (no RequireUser). The active
	// adapter's VerifyWebhookSignature is the authenticator; ResolveProvider
	// fetches whatever bot the tenant admin has connected (or nil → 503). The
	// route is mounted unconditionally so it survives a connect/disconnect
	// without a redeploy.
	webhook := handler.NewWebhookHandler(handler.WebhookConfig{
		ResolveProvider: func() provider.Provider {
			a := botConfigSvc.ActiveAdapter()
			if a == nil {
				return nil
			}
			return a
		},
		Inbound:           inbound,
		MaxConcurrent:     32,
		Logger:            logger,
		BackgroundContext: ctx,
	})
	mux.Handle("/api/v1/providers/telegram/webhook", webhook)

	protected := http.NewServeMux()
	handler.NewLinks(link, store).Register(protected)
	mux.Handle("/api/v1/links/", middleware.RequireUser(mosesClient)(protected))
	mux.Handle("/api/v1/links", middleware.RequireUser(mosesClient)(protected))

	// Telegram bot configuration (moses-chat-bot-qcq): GET /info is a tenant
	// read for any member; POST/DELETE /connect are tenant-admin gated.
	// RequireUser stamps the role; RequireTenantAdmin enforces it. All three
	// routes live on a single mux (the handler registers them); the outer mux
	// dispatches each method-scoped pattern through the right middleware chain.
	tgConfigHandler := handler.NewTelegramConfig(botConfigSvc, os.Getenv("MOSES_BASE_PATH"))
	tgConfigMux := http.NewServeMux()
	tgConfigHandler.Register(tgConfigMux)
	mux.Handle("GET /api/v1/provider/telegram/info",
		middleware.RequireUser(mosesClient)(tgConfigMux))
	tgAdminGated := middleware.RequireUser(mosesClient)(middleware.RequireTenantAdmin(tgConfigMux))
	mux.Handle("POST /api/v1/provider/telegram/connect", tgAdminGated)
	mux.Handle("DELETE /api/v1/provider/telegram/connect", tgAdminGated)

	// Workspace-tool surface (T-PUSH-1 + CHAT-y3u bearer gate): the ingress
	// routes /api/ to the backend, so these endpoints are externally reachable.
	// RequirePlatformAPIKey runs FIRST and constant-time-checks the inbound
	// Authorization bearer against MOSES_PLATFORM_API_KEY. Only after that
	// passes does MosesHeaders extract X-Moses-Tenant-ID — i.e. the tenant
	// header is meaningful only because we've already proved the caller is
	// the platform proxy.
	//
	// Local-dev escape hatch: set BOT_PLATFORM_AUTH_DISABLED=true to bypass
	// the bearer (the middleware logs a warn on every request when set).
	pushMux := http.NewServeMux()
	handler.NewPush(store, sender).Register(pushMux)
	pushWrapped := middleware.RequirePlatformAPIKey(platformKey)(middleware.MosesHeaders(pushMux))
	mux.Handle("/api/v1/push/", pushWrapped)
	mux.Handle("/api/v1/workspace/", pushWrapped)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
		}
	}()

	log.Printf("moses-chat-bot listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server failed: %v", err)
	}
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// streamTimeoutFromEnv reads CHAT_BOT_STREAM_TIMEOUT and parses it as a
// Go duration. Default 5 minutes. Invalid values fall back silently with
// a log message.
func streamTimeoutFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("CHAT_BOT_STREAM_TIMEOUT"))
	if raw == "" {
		return 5 * time.Minute
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("invalid CHAT_BOT_STREAM_TIMEOUT %q; defaulting to 5m", raw)
		return 5 * time.Minute
	}
	return d
}

