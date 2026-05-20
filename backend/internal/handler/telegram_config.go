// Package handler — Telegram bot configuration endpoints (moses-chat-bot-qcq).
//
// Three endpoints back the in-app "Connect Telegram" wizard and the honest
// LinkNew states:
//
//	GET    /api/v1/provider/telegram/info     — tenant read, any member
//	POST   /api/v1/provider/telegram/connect  — tenant-admin gated
//	DELETE /api/v1/provider/telegram/connect  — tenant-admin gated
//
// All three sit behind RequireUser; connect/disconnect additionally chain
// RequireTenantAdmin (see cmd/server/main.go). The webhook URL passed to
// botconfig.Connect is derived here from the request Host header so the bot
// registers the URL it is actually reachable on — no env guesswork.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"moses-chat-bot/backend/internal/handler/middleware"
	"moses-chat-bot/backend/internal/service/botconfig"
)

// TelegramConfig wires the botconfig service into HTTP.
type TelegramConfig struct {
	svc *botconfig.Service
	// basePath is MOSES_BASE_PATH — the runtime deploy prefix Moses serves the
	// app under (e.g. "/apps/<tenant>/moses-chat-bot"). Prepended to the
	// webhook path so the registered URL matches the ingress route.
	basePath string
}

// NewTelegramConfig constructs the handler. basePath is the MOSES_BASE_PATH
// env value ("" when standalone / root-mounted).
func NewTelegramConfig(svc *botconfig.Service, basePath string) *TelegramConfig {
	return &TelegramConfig{
		svc:      svc,
		basePath: strings.TrimRight(strings.TrimSpace(basePath), "/"),
	}
}

// Register mounts the routes. The caller wraps the info route with RequireUser
// and the connect routes with RequireUser + RequireTenantAdmin.
func (h *TelegramConfig) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/provider/telegram/info", h.handleInfo)
	mux.HandleFunc("POST /api/v1/provider/telegram/connect", h.handleConnect)
	mux.HandleFunc("DELETE /api/v1/provider/telegram/connect", h.handleDisconnect)
}

// telegramInfoResponse is the GET /info shape consumed by the LinkNew page.
type telegramInfoResponse struct {
	Configured bool   `json:"configured"`
	Username   string `json:"username,omitempty"`
}

func (h *TelegramConfig) handleInfo(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantID(r.Context())
	if tenantID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	info, err := h.svc.Info(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load bot status")
		return
	}
	writeJSON(w, http.StatusOK, telegramInfoResponse{
		Configured: info.Configured,
		Username:   info.Username,
	})
}

// connectRequest is the POST /connect body — just the BotFather token.
type connectRequest struct {
	Token string `json:"token"`
}

func (h *TelegramConfig) handleConnect(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantID(r.Context())
	userID := middleware.UserID(r.Context())
	if tenantID == uuid.Nil || userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req connectRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}

	webhookBase := h.webhookBaseURL(r)
	info, err := h.svc.Connect(r.Context(), tenantID, userID, req.Token, webhookBase)
	if err != nil {
		switch {
		case errors.Is(err, botconfig.ErrEmptyToken):
			writeError(w, http.StatusBadRequest, "token is required")
		case errors.Is(err, botconfig.ErrInvalidToken):
			writeError(w, http.StatusBadRequest, "Telegram rejected this token. Double-check you pasted the full token from BotFather.")
		default:
			writeError(w, http.StatusBadGateway, "failed to connect the bot with Telegram")
		}
		return
	}
	writeJSON(w, http.StatusOK, telegramInfoResponse{
		Configured: info.Configured,
		Username:   info.Username,
	})
}

func (h *TelegramConfig) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantID(r.Context())
	if tenantID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if err := h.svc.Disconnect(r.Context(), tenantID); err != nil {
		if errors.Is(err, botconfig.ErrNotConfigured) {
			writeError(w, http.StatusNotFound, "no telegram bot is connected")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to disconnect the bot")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// webhookBaseURL derives the scheme://host[/basePath] the webhook is reachable
// on from the inbound request. Telegram requires HTTPS, and the bot is always
// fronted by the Moses ingress (TLS-terminating), so the scheme is forced to
// https unless an explicit X-Forwarded-Proto says otherwise.
func (h *TelegramConfig) webhookBaseURL(r *http.Request) string {
	scheme := "https"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "http" {
		scheme = "http"
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	return scheme + "://" + host + h.basePath
}
