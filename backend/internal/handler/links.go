// Package handler holds the bot's stdlib-http user-scoped endpoints.
//
// SPEC §4 — these endpoints are reachable through the iframe under
// /apps/<tenant>/moses-chat-bot/api/v1/links/* via cookie auth. The
// internal CompleteLink path is NOT exposed over HTTP; the inbound
// provider webhook (T-RELAY-1) calls linker.CompleteLink directly.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/handler/middleware"
	"moses-chat-bot/backend/internal/service/linker"
)

// Links wires the linker service into HTTP. Mount under /api/v1.
type Links struct {
	linker  *linker.Linker
	store   *db.Store
	limiter *rateLimiter
}

// NewLinks constructs the handler. Rate limit is 5 code-mints / min / user
// — generous enough for a normal user retrying the wizard, tight enough
// to keep an authenticated-but-malicious caller from flooding the
// pending_links table.
func NewLinks(l *linker.Linker, store *db.Store) *Links {
	return &Links{
		linker:  l,
		store:   store,
		limiter: newRateLimiter(5, time.Minute),
	}
}

// Register mounts the routes on the given mux. Caller wraps everything
// with the RequireUser middleware before reaching here.
func (h *Links) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/links/codes", h.handleCreateCode)
	mux.HandleFunc("GET /api/v1/links/codes/{code}", h.handlePollCode)
	mux.HandleFunc("GET /api/v1/links", h.handleListLinks)
	mux.HandleFunc("DELETE /api/v1/links/{id}", h.handleDeleteLink)
}

// ============================================================================
// POST /api/v1/links/codes
// ============================================================================

type createCodeRequest struct {
	APIKey           string `json:"apiKey"`
	APIKeyIDHint     string `json:"apiKeyIdHint,omitempty"`
	ExpiresInSeconds int    `json:"expiresInSeconds,omitempty"`
}

type createCodeResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (h *Links) handleCreateCode(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserID(r.Context())
	tenantID := middleware.TenantID(r.Context())
	if userID == uuid.Nil || tenantID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	if !h.limiter.allow(userID) {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	var req createCodeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.APIKey) == "" {
		writeError(w, http.StatusBadRequest, "apiKey is required")
		return
	}

	var hint *uuid.UUID
	if req.APIKeyIDHint != "" {
		parsed, err := uuid.Parse(req.APIKeyIDHint)
		if err != nil {
			writeError(w, http.StatusBadRequest, "apiKeyIdHint must be a UUID")
			return
		}
		hint = &parsed
	}

	expiresIn := time.Duration(req.ExpiresInSeconds) * time.Second
	code, expiresAt, err := h.linker.CreateCode(r.Context(), tenantID, userID, req.APIKey, hint, expiresIn)
	if err != nil {
		if errors.Is(err, linker.ErrEmptyAPIKey) {
			writeError(w, http.StatusBadRequest, "apiKey is required")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to mint code")
		return
	}

	writeJSON(w, http.StatusCreated, createCodeResponse{Code: code, ExpiresAt: expiresAt})
}

// ============================================================================
// GET /api/v1/links/codes/:code
// ============================================================================

type pollCodeResponse struct {
	Status string     `json:"status"`
	LinkID *uuid.UUID `json:"linkId,omitempty"`
}

func (h *Links) handlePollCode(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserID(r.Context())
	tenantID := middleware.TenantID(r.Context())
	if userID == uuid.Nil || tenantID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	code := r.PathValue("code")
	status, linkID, err := h.linker.PollCode(r.Context(), tenantID, userID, code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to poll code")
		return
	}

	switch status {
	case linker.StatusPending, linker.StatusCompleted:
		writeJSON(w, http.StatusOK, pollCodeResponse{Status: string(status), LinkID: linkID})
	case linker.StatusExpired:
		writeJSON(w, http.StatusGone, pollCodeResponse{Status: string(status)})
	default:
		writeError(w, http.StatusNotFound, "unknown code")
	}
}

// ============================================================================
// GET /api/v1/links
// ============================================================================

type linkSummary struct {
	ID             uuid.UUID  `json:"id"`
	Provider       string     `json:"provider"`
	ProviderUserID string     `json:"providerUserId"`
	IsActive       bool       `json:"isActive"`
	LastUsedAt     *time.Time `json:"lastUsedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

func (h *Links) handleListLinks(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserID(r.Context())
	tenantID := middleware.TenantID(r.Context())
	if userID == uuid.Nil || tenantID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	links, err := h.store.ListActiveLinksByMosesUser(r.Context(), tenantID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list links")
		return
	}
	out := make([]linkSummary, 0, len(links))
	for _, l := range links {
		out = append(out, linkSummary{
			ID:             l.ID,
			Provider:       l.Provider,
			ProviderUserID: l.ProviderUserID,
			IsActive:       l.IsActive,
			LastUsedAt:     l.LastUsedAt,
			CreatedAt:      l.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ============================================================================
// DELETE /api/v1/links/:id
// ============================================================================

func (h *Links) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserID(r.Context())
	tenantID := middleware.TenantID(r.Context())
	if userID == uuid.Nil || tenantID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	if err := h.linker.Unlink(r.Context(), tenantID, userID, id); err != nil {
		if errors.Is(err, linker.ErrLinkNotFound) {
			writeError(w, http.StatusNotFound, "link not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to unlink")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ============================================================================
// helpers
// ============================================================================

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ============================================================================
// rateLimiter — per-user token bucket. Tiny enough to inline.
// ============================================================================

type rateLimiter struct {
	mu        sync.Mutex
	buckets   map[uuid.UUID]*rlBucket
	limit     int
	window    time.Duration
}

type rlBucket struct {
	count       int
	windowStart time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		buckets: make(map[uuid.UUID]*rlBucket),
		limit:   limit,
		window:  window,
	}
}

func (r *rateLimiter) allow(userID uuid.UUID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	b, ok := r.buckets[userID]
	if !ok || now.Sub(b.windowStart) > r.window {
		r.buckets[userID] = &rlBucket{count: 1, windowStart: now}
		return true
	}
	if b.count >= r.limit {
		return false
	}
	b.count++
	return true
}
