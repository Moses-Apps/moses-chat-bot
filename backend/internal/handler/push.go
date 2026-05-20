// Package handler — workspace-tool push surface (SPEC §7, T-PUSH-1).
//
// These endpoints are called by Moses Manager via the platform's workspace-tool
// proxy (auto-discovered through /api/openapi.json). The proxy authenticates
// with MOSES_PLATFORM_API_KEY upstream and injects X-Moses-Tenant-ID into the
// downstream request — that is what the handlers below trust. RequireUser is
// NOT applied here (no user cookie / bearer in this call path).
//
// Routing convention (deviation from SPEC §7):
//   - /api/v1/push/message            (POST) — the user-scoped /links group
//     already mounts GET /api/v1/links, so the workspace-tool variant uses a
//     /workspace/ prefix to avoid the path collision while preserving the
//     SPEC operation IDs in OpenAPI:
//   - /api/v1/workspace/links         (GET)            → listLinks
//   - /api/v1/workspace/links/{id}/notify (POST)       → notifyLink
//   - /api/v1/workspace/messages      (GET)            → listRecentMessages
//
// The push endpoint stays at /api/v1/push/message — that path is the only
// workspace-tool-only path that isn't ambiguous with the user-scoped surface.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/handler/middleware"
	"moses-chat-bot/backend/internal/provider"
	"moses-chat-bot/backend/internal/service/relay"
)

// Push wires the workspace-tool endpoints. Construct once at startup with the
// shared Store and relay Sender from cmd/server/main.go.
type Push struct {
	store  *db.Store
	sender *relay.Sender
}

// NewPush builds the handler. Both dependencies are required; passing nil
// will surface as 500s rather than panics.
func NewPush(store *db.Store, sender *relay.Sender) *Push {
	return &Push{store: store, sender: sender}
}

// Register mounts the routes. The caller wraps `mux` with MosesHeaders before
// reaching here (see cmd/server/main.go).
func (h *Push) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/push/message", h.handlePushMessage)
	mux.HandleFunc("GET /api/v1/workspace/links", h.handleListLinks)
	mux.HandleFunc("POST /api/v1/workspace/links/{id}/notify", h.handleNotifyLink)
	mux.HandleFunc("GET /api/v1/workspace/messages", h.handleListRecentMessages)
}

// ============================================================================
// Shared helpers
// ============================================================================

const maxBodyBytes = 64 * 1024 // 64 KiB ceiling on inbound JSON
const maxTextLen = 8000

// resolveTenant pulls X-Moses-Tenant-ID from MosesContext and parses it as a
// UUID. Returns (uuid.Nil, false) and writes a 401 to w on any failure — the
// handler can return immediately.
func resolveTenant(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	mc := middleware.GetMosesContext(r.Context())
	if mc.TenantID == "" {
		writeWorkspaceError(w, http.StatusUnauthorized, "missing X-Moses-Tenant-ID", "unauthenticated")
		return uuid.Nil, false
	}
	tenantID, err := uuid.Parse(mc.TenantID)
	if err != nil {
		writeWorkspaceError(w, http.StatusUnauthorized, "invalid X-Moses-Tenant-ID", "unauthenticated")
		return uuid.Nil, false
	}
	return tenantID, true
}

// writeWorkspaceError emits the {error, code} shape SPEC §7 mandates. We add
// `code` (a machine-readable token) for MM-facing handlers; the legacy
// user-scoped writeError keeps its plain {error} shape.
func writeWorkspaceError(w http.ResponseWriter, status int, msg, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": code})
}

// ============================================================================
// POST /api/v1/push/message
// ============================================================================

type pushMessageRequest struct {
	MosesUserID    string           `json:"moses_user_id"`
	Text           string           `json:"text"`
	Markdown       bool             `json:"markdown,omitempty"`
	ProviderFilter *providerFilterJSON `json:"provider_filter,omitempty"`
}

type providerFilterJSON struct {
	Providers []string `json:"providers,omitempty"`
	ChatIDs   []string `json:"chat_ids,omitempty"`
}

type pushMessageResponse struct {
	SentCount int                  `json:"sent_count"`
	Results   []linkResultResponse `json:"results"`
}

type linkResultResponse struct {
	LinkID       uuid.UUID `json:"link_id"`
	Provider     string    `json:"provider"`
	ChatID       string    `json:"chat_id"`
	Sent         bool      `json:"sent"`
	Error        string    `json:"error,omitempty"`
	MessageRowID uuid.UUID `json:"message_row_id"`
}

func (h *Push) handlePushMessage(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := resolveTenant(w, r)
	if !ok {
		return
	}

	var req pushMessageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeWorkspaceError(w, http.StatusBadRequest, "invalid JSON body", "bad_request")
		return
	}

	mosesUserID, err := uuid.Parse(req.MosesUserID)
	if err != nil {
		writeWorkspaceError(w, http.StatusBadRequest, "moses_user_id must be a UUID", "bad_request")
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeWorkspaceError(w, http.StatusBadRequest, "text is required", "bad_request")
		return
	}
	if len(text) > maxTextLen {
		writeWorkspaceError(w, http.StatusBadRequest, "text exceeds 8000 chars", "bad_request")
		return
	}

	filter := relay.ProviderFilter{}
	if req.ProviderFilter != nil {
		filter.Providers = req.ProviderFilter.Providers
		filter.ChatIDs = req.ProviderFilter.ChatIDs
	}

	results, err := h.sender.SendToMosesUser(r.Context(), tenantID, mosesUserID, provider.OutboundMessage{
		Text:     text,
		Markdown: req.Markdown,
	}, filter)
	if err != nil {
		// Top-level error from sender means we couldn't list links (DB issue,
		// missing tenant). SPEC §7: empty-results is a 200 with sent_count=0;
		// only true infra failures are 500.
		writeWorkspaceError(w, http.StatusInternalServerError, "failed to send", "send_failed")
		return
	}

	out := pushMessageResponse{
		SentCount: 0,
		Results:   make([]linkResultResponse, 0, len(results)),
	}
	for _, r := range results {
		lr := linkResultResponse{
			LinkID:       r.LinkID,
			Provider:     r.Provider,
			ChatID:       r.ChatID,
			Sent:         r.Sent,
			Error:        r.Error,
			MessageRowID: r.MessageRowID,
		}
		out.Results = append(out.Results, lr)
		if r.Sent {
			out.SentCount++
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ============================================================================
// GET /api/v1/workspace/links
// ============================================================================

type workspaceLinkSummary struct {
	ID             uuid.UUID  `json:"id"`
	MosesUserID    uuid.UUID  `json:"moses_user_id"`
	Provider       string     `json:"provider"`
	ProviderUserID string     `json:"provider_user_id"`
	LastUsedAt     *string    `json:"last_used_at,omitempty"`
	CreatedAt      string     `json:"created_at"`
}

type linksResponse struct {
	Links []workspaceLinkSummary `json:"links"`
}

func (h *Push) handleListLinks(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := resolveTenant(w, r)
	if !ok {
		return
	}

	var links []db.ChatRelayLink
	var err error
	if raw := r.URL.Query().Get("moses_user_id"); raw != "" {
		mosesUserID, parseErr := uuid.Parse(raw)
		if parseErr != nil {
			writeWorkspaceError(w, http.StatusBadRequest, "moses_user_id must be a UUID", "bad_request")
			return
		}
		links, err = h.store.ListActiveLinksByMosesUser(r.Context(), tenantID, mosesUserID)
	} else {
		links, err = h.store.ListAllActiveLinksByTenant(r.Context(), tenantID)
	}
	if err != nil {
		writeWorkspaceError(w, http.StatusInternalServerError, "failed to list links", "list_failed")
		return
	}

	out := linksResponse{Links: make([]workspaceLinkSummary, 0, len(links))}
	for _, l := range links {
		s := workspaceLinkSummary{
			ID:             l.ID,
			MosesUserID:    l.MosesUserID,
			Provider:       l.Provider,
			ProviderUserID: l.ProviderUserID,
			CreatedAt:      l.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		}
		if l.LastUsedAt != nil {
			ts := l.LastUsedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
			s.LastUsedAt = &ts
		}
		out.Links = append(out.Links, s)
	}
	writeJSON(w, http.StatusOK, out)
}

// ============================================================================
// POST /api/v1/workspace/links/{id}/notify
// ============================================================================

type notifyRequest struct {
	Text     string `json:"text"`
	Markdown bool   `json:"markdown,omitempty"`
}

type notifyResponse struct {
	Sent         bool      `json:"sent"`
	Error        string    `json:"error,omitempty"`
	MessageRowID uuid.UUID `json:"message_row_id"`
}

func (h *Push) handleNotifyLink(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := resolveTenant(w, r)
	if !ok {
		return
	}

	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeWorkspaceError(w, http.StatusBadRequest, "id must be a UUID", "bad_request")
		return
	}

	var req notifyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeWorkspaceError(w, http.StatusBadRequest, "invalid JSON body", "bad_request")
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeWorkspaceError(w, http.StatusBadRequest, "text is required", "bad_request")
		return
	}
	if len(text) > maxTextLen {
		writeWorkspaceError(w, http.StatusBadRequest, "text exceeds 8000 chars", "bad_request")
		return
	}

	// Two-step lookup so we can surface SPEC §7's 404 (no such row) vs 403
	// (row exists, belongs to another tenant) distinction. The "any tenant"
	// store method is internal-only — only callable from this handler, which
	// runs behind the workspace-tool proxy and immediately re-checks the
	// tenant binding. The 403 path is the audit signal that the platform's
	// proxy let through a tenant-crossing call: it should never fire in
	// production but is the contract we expose.
	link, err := h.store.GetLinkByIDAnyTenant(r.Context(), id)
	if err != nil {
		if db.IsNoRows(err) {
			writeWorkspaceError(w, http.StatusNotFound, "link not found", "not_found")
			return
		}
		writeWorkspaceError(w, http.StatusInternalServerError, "failed to load link", "lookup_failed")
		return
	}
	if link.TenantID != tenantID {
		writeWorkspaceError(w, http.StatusForbidden, "link belongs to a different tenant", "tenant_mismatch")
		return
	}

	rowID, sendErr := h.sender.SendToLink(r.Context(), link, provider.OutboundMessage{
		Text:     text,
		Markdown: req.Markdown,
	}, nil)
	resp := notifyResponse{
		Sent:         sendErr == nil,
		MessageRowID: rowID,
	}
	if sendErr != nil {
		resp.Error = sendErr.Error()
		if errors.Is(sendErr, relay.ErrRateLimited) {
			// SPEC §7: rate limit surfaces as 429 with Retry-After. We pick a
			// conservative 5s — the bucket refills at capacity/60 per second.
			w.Header().Set("Retry-After", "5")
			writeJSON(w, http.StatusTooManyRequests, resp)
			return
		}
		// Other send errors (provider error, unknown provider) — return 200
		// with sent=false. The audit row IS persisted (rowID is non-nil) so
		// MM can correlate with /messages.
		writeJSON(w, http.StatusOK, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ============================================================================
// GET /api/v1/workspace/messages
// ============================================================================

type messageSummary struct {
	ID         uuid.UUID `json:"id"`
	LinkID     uuid.UUID `json:"link_id"`
	Direction  string    `json:"direction"`
	Text       string    `json:"text"`
	OccurredAt string    `json:"occurred_at"`
	Error      string    `json:"error,omitempty"`
}

type messagesResponse struct {
	Messages []messageSummary `json:"messages"`
}

func (h *Push) handleListRecentMessages(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := resolveTenant(w, r)
	if !ok {
		return
	}

	rawUser := r.URL.Query().Get("moses_user_id")
	if rawUser == "" {
		writeWorkspaceError(w, http.StatusBadRequest, "moses_user_id is required", "bad_request")
		return
	}
	mosesUserID, err := uuid.Parse(rawUser)
	if err != nil {
		writeWorkspaceError(w, http.StatusBadRequest, "moses_user_id must be a UUID", "bad_request")
		return
	}

	limit := 50
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		parsed, perr := strconv.Atoi(rawLimit)
		if perr != nil || parsed <= 0 {
			writeWorkspaceError(w, http.StatusBadRequest, "limit must be a positive integer", "bad_request")
			return
		}
		if parsed > 100 {
			parsed = 100
		}
		limit = parsed
	}

	msgs, err := h.store.ListRecentByMosesUser(r.Context(), tenantID, mosesUserID, limit)
	if err != nil {
		writeWorkspaceError(w, http.StatusInternalServerError, "failed to list messages", "list_failed")
		return
	}

	out := messagesResponse{Messages: make([]messageSummary, 0, len(msgs))}
	for _, m := range msgs {
		s := messageSummary{
			ID:         m.ID,
			LinkID:     m.LinkID,
			Direction:  m.Direction,
			Text:       m.Text,
			OccurredAt: m.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		}
		if m.Error != nil {
			s.Error = *m.Error
		}
		out.Messages = append(out.Messages, s)
	}
	writeJSON(w, http.StatusOK, out)
}
