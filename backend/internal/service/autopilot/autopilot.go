// Package autopilot implements the `/autopilot start|stop|status` slash
// commands plus a background sweeper that reconciles terminal autonomous
// sessions back into provider_chat_state.
//
// Per SPEC §9 the autonomous session is tenant-singleton; Start performs
// an explicit pre-flight check against the platform so that two users in
// the same tenant cannot stomp each other's sessions. Stop / Status read
// the session id off the local chat-state row and round-trip the platform
// once per command. The Sweeper polls non-null session ids on a fixed
// interval and clears + DMs on terminal status (completed | cancelled |
// failed), 404 (admin-deleted), and 401 (platform_401 deactivation).
package autopilot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/mosesclient"
	"moses-chat-bot/backend/internal/provider"
	"moses-chat-bot/backend/internal/service/crypto"
)

// Store is the narrow database surface this service needs. The concrete
// *db.Store satisfies it (compile-time check at the bottom of this file).
// We deliberately keep the surface tight so unit tests can substitute an
// in-memory fake without dragging the rest of the store along.
//
// Tenant isolation: every method except ListWithActiveAutopilot and
// GetLinkByIDAnyTenant takes tenant_id (directly or implicitly via the
// link row). Those two are the documented cross-tenant exceptions — the
// sweeper needs them to iterate every tenant's pending session and
// resolve the link row before it knows the tenant. The pair is used
// strictly inside the sweeper loop and never exposed outwards.
type Store interface {
	// UpdateAutopilot writes (autopilot_enabled, autopilot_session_id) to
	// the provider_chat_state row keyed by (link_id, provider_chat_id).
	UpdateAutopilot(ctx context.Context, linkID uuid.UUID, providerChatID string, sessionID *uuid.UUID, enabled bool) error

	// GetOrCreate returns the (link_id, provider_chat_id) chat-state row,
	// creating an empty one on demand. Start needs this because the user
	// may type /autopilot before any regular message has materialised a
	// row.
	GetOrCreate(ctx context.Context, linkID uuid.UUID, providerChatID string) (*db.ProviderChatState, error)

	// ListWithActiveAutopilot returns every chat-state row whose
	// autopilot_session_id is non-null. The query is cross-tenant on
	// purpose — the sweeper iterates every tenant's pending session
	// then re-resolves each link tenant-scoped via GetLinkByIDAnyTenant.
	ListWithActiveAutopilot(ctx context.Context) ([]db.ProviderChatState, error)

	// GetLinkByIDAnyTenant is the cross-tenant link lookup the sweeper
	// uses after ListWithActiveAutopilot. The concrete *db.Store
	// implements this with a deliberate cross-tenant SELECT (already
	// scoped to read-only and to internal callers in store.go).
	GetLinkByIDAnyTenant(ctx context.Context, id uuid.UUID) (*db.ChatRelayLink, error)

	// DeactivateLink is invoked when the sweeper observes a 401 against
	// the platform: the API key was revoked, the link is no longer
	// usable, and the user should be told to relink.
	DeactivateLink(ctx context.Context, tenantID uuid.UUID, id uuid.UUID, reason string) error
}

// MosesClient is the narrow per-bearer subset of mosesclient.Client this
// service touches. We rely on a factory so each user's API key drives its
// own bearer; reusing a single client across users would leak credentials.
type MosesClient interface {
	StartAutonomous(ctx context.Context, opts mosesclient.AutonomousStartOpts) (*mosesclient.AutonomousSession, error)
	StopAutonomous(ctx context.Context, sessionID uuid.UUID) error
	GetAutonomous(ctx context.Context, sessionID uuid.UUID) (*mosesclient.AutonomousSession, error)
	GetActiveAutonomous(ctx context.Context) (*mosesclient.AutonomousSession, error)
}

// ClientFactory constructs a MosesClient authenticated as the link's
// user. Production wires this to a closure around mosesclient.NewClient +
// BearerAuth; tests substitute a fake that records calls.
type ClientFactory func(bearer string) MosesClient

// Sender is the narrow outbound surface this service needs. relay.Sender
// satisfies it. Defining the interface here (rather than importing
// relay.Sender directly) breaks the package import cycle that would form
// when relay/inbound.go calls into autopilot to dispatch slash commands.
type Sender interface {
	SendToLink(ctx context.Context, link *db.ChatRelayLink, msg provider.OutboundMessage, mosesConversationID *uuid.UUID) (uuid.UUID, error)
}

// Service owns the command + sweeper logic. Construct via New.
type Service struct {
	Store    Store
	Factory  ClientFactory
	Envelope *crypto.Envelope
	Sender   Sender
	Logger   *slog.Logger
}

// New constructs a Service with safe defaults. All deps are required for
// production; tests pass fakes for Store/Factory/Sender. logger may be
// nil — slog.Default is used in that case.
func New(store Store, factory ClientFactory, env *crypto.Envelope, sender Sender, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		Store:    store,
		Factory:  factory,
		Envelope: env,
		Sender:   sender,
		Logger:   logger,
	}
}

// Start handles `/autopilot start`. Returns the reply text to send back
// to the user.
//
// Flow (per SPEC §9):
//  1. Decrypt the link's API key and build a per-user platform client.
//  2. Pre-flight: GET /autonomous/active.
//     - If a session exists AND owned by this user: "already running".
//     - If a session exists AND owned by someone else: refuse — tenant
//     singleton must not be silently stomped.
//     - If nil: proceed.
//  3. POST /autonomous/start with platform defaults (no overrides). On
//     403 surface a friendly RBAC message.
//  4. Persist (autopilot_session_id, autopilot_enabled=true) on the
//     chat-state row.
func (s *Service) Start(ctx context.Context, link *db.ChatRelayLink, providerChatID string) (string, error) {
	if link == nil {
		return "", errors.New("autopilot: nil link")
	}

	bearer, err := s.decryptBearer(link)
	if err != nil {
		return "", err
	}
	client := s.Factory(bearer)

	// Pre-flight: tenant-singleton check.
	active, err := client.GetActiveAutonomous(ctx)
	if err != nil {
		return "", fmt.Errorf("autopilot: pre-flight: %w", err)
	}
	if active != nil {
		if active.StartedBy == link.MosesUserID {
			// Same user — be idempotent: persist the row (the user might
			// have started the session via the web UI) and report back.
			if err := s.persistSession(ctx, link.ID, providerChatID, &active.ID, true); err != nil {
				return "", fmt.Errorf("autopilot: persist existing session: %w", err)
			}
			return fmt.Sprintf(
				"Autopilot is already running for you. Session %s — say /autopilot stop to halt.",
				shortID(active.ID),
			), nil
		}
		return "Tenant has an active autopilot owned by another user; ask them to /autopilot stop first.", nil
	}

	// Start. Platform defaults (no overrides).
	session, err := client.StartAutonomous(ctx, mosesclient.AutonomousStartOpts{})
	if err != nil {
		if errors.Is(err, mosesclient.ErrForbidden) {
			return "You lack CREATE AUTONOMOUS_SESSIONS for this tenant.", nil
		}
		if errors.Is(err, mosesclient.ErrUnauthorized) {
			// Best-effort link deactivation; the user has nothing to do
			// with the key any more.
			_ = s.Store.DeactivateLink(ctx, link.TenantID, link.ID, "platform_401")
			return "Your Moses key was revoked — please /unlink and re-link from the web UI.", nil
		}
		return "", fmt.Errorf("autopilot: start: %w", err)
	}

	if err := s.persistSession(ctx, link.ID, providerChatID, &session.ID, true); err != nil {
		return "", fmt.Errorf("autopilot: persist session: %w", err)
	}

	return fmt.Sprintf(
		"Autopilot started. Session %s — say /autopilot stop to halt.",
		shortID(session.ID),
	), nil
}

// Stop handles `/autopilot stop`. Returns the reply text.
func (s *Service) Stop(ctx context.Context, link *db.ChatRelayLink, providerChatID string) (string, error) {
	if link == nil {
		return "", errors.New("autopilot: nil link")
	}

	state, err := s.Store.GetOrCreate(ctx, link.ID, providerChatID)
	if err != nil {
		return "", fmt.Errorf("autopilot: load state: %w", err)
	}
	if state.AutopilotSessionID == nil {
		return "No autopilot active.", nil
	}
	sessionID := *state.AutopilotSessionID

	bearer, err := s.decryptBearer(link)
	if err != nil {
		return "", err
	}
	client := s.Factory(bearer)

	if err := client.StopAutonomous(ctx, sessionID); err != nil {
		switch {
		case errors.Is(err, mosesclient.ErrNotFound):
			// Already gone on the platform side. Clear local bookkeeping
			// and report success — surfacing "not found" would confuse
			// the user.
			if clearErr := s.persistSession(ctx, link.ID, providerChatID, nil, false); clearErr != nil {
				return "", fmt.Errorf("autopilot: clear stale session: %w", clearErr)
			}
			return fmt.Sprintf("Autopilot session %s was already stopped. Cleared.", shortID(sessionID)), nil
		case errors.Is(err, mosesclient.ErrUnauthorized):
			_ = s.Store.DeactivateLink(ctx, link.TenantID, link.ID, "platform_401")
			return "Your Moses key was revoked — please /unlink and re-link from the web UI.", nil
		default:
			return "", fmt.Errorf("autopilot: stop: %w", err)
		}
	}

	if err := s.persistSession(ctx, link.ID, providerChatID, nil, false); err != nil {
		return "", fmt.Errorf("autopilot: clear session: %w", err)
	}
	return fmt.Sprintf("Autopilot stopped. Session %s halted.", shortID(sessionID)), nil
}

// Status handles `/autopilot status`. Returns the reply text.
func (s *Service) Status(ctx context.Context, link *db.ChatRelayLink, providerChatID string) (string, error) {
	if link == nil {
		return "", errors.New("autopilot: nil link")
	}

	state, err := s.Store.GetOrCreate(ctx, link.ID, providerChatID)
	if err != nil {
		return "", fmt.Errorf("autopilot: load state: %w", err)
	}
	if state.AutopilotSessionID == nil {
		return "No autopilot active.", nil
	}
	sessionID := *state.AutopilotSessionID

	bearer, err := s.decryptBearer(link)
	if err != nil {
		return "", err
	}
	client := s.Factory(bearer)

	session, err := client.GetAutonomous(ctx, sessionID)
	if err != nil {
		switch {
		case errors.Is(err, mosesclient.ErrNotFound):
			// Platform forgot about the session — clear our pointer so
			// the next /autopilot start works without manual cleanup.
			if clearErr := s.persistSession(ctx, link.ID, providerChatID, nil, false); clearErr != nil {
				return "", fmt.Errorf("autopilot: clear stale session: %w", clearErr)
			}
			return fmt.Sprintf("Autopilot session %s no longer exists on Moses. Cleared local pointer.", shortID(sessionID)), nil
		case errors.Is(err, mosesclient.ErrUnauthorized):
			_ = s.Store.DeactivateLink(ctx, link.TenantID, link.ID, "platform_401")
			return "Your Moses key was revoked — please /unlink and re-link from the web UI.", nil
		default:
			return "", fmt.Errorf("autopilot: status: %w", err)
		}
	}

	return formatStatus(session), nil
}

// SweepTerminalSessions polls once and acts on terminal / 404 / 401 rows.
// Exported for the test path; production callers should use StartSweeper.
func (s *Service) SweepTerminalSessions(ctx context.Context) error {
	rows, err := s.Store.ListWithActiveAutopilot(ctx)
	if err != nil {
		return fmt.Errorf("autopilot: list active: %w", err)
	}
	for i := range rows {
		row := rows[i]
		s.sweepOne(ctx, row)
	}
	return nil
}

// StartSweeper launches the background poll loop. It runs until ctx is
// cancelled. interval <= 0 falls back to 60s (the SPEC §9 default).
func (s *Service) StartSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Fire one sweep immediately so terminal sessions get cleared without
	// waiting up to a full interval on startup.
	if err := s.SweepTerminalSessions(ctx); err != nil {
		s.Logger.Warn("autopilot sweep failed", slog.String("err", err.Error()))
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.SweepTerminalSessions(ctx); err != nil {
				s.Logger.Warn("autopilot sweep failed", slog.String("err", err.Error()))
			}
		}
	}
}

// sweepOne handles one row, resolving its link tenant-scoped and acting
// on the platform's response. Per-row errors are logged but never abort
// the broader sweep: one bad row must not stall every other tenant's
// reconciliation.
func (s *Service) sweepOne(ctx context.Context, row db.ProviderChatState) {
	if row.AutopilotSessionID == nil {
		return
	}
	sessionID := *row.AutopilotSessionID

	// ListWithActiveAutopilot is cross-tenant; resolve the tenant now via
	// the documented cross-tenant link lookup (see Store doc).
	link, err := s.Store.GetLinkByIDAnyTenant(ctx, row.LinkID)
	if err != nil {
		if db.IsNoRows(err) {
			// Link was hard-deleted while we held a dangling chat-state
			// row. Clear the pointer so we don't poll forever.
			_ = s.Store.UpdateAutopilot(ctx, row.LinkID, row.ProviderChatID, nil, false)
			return
		}
		s.Logger.Warn("autopilot sweep: resolve link",
			slog.String("link_id", row.LinkID.String()),
			slog.String("err", err.Error()),
		)
		return
	}

	bearer, err := s.Envelope.Decrypt(link.TenantID, link.EncryptedAPIKey, link.EncryptionKeyID)
	if err != nil {
		s.Logger.Warn("autopilot sweep: decrypt",
			slog.String("link_id", link.ID.String()),
			slog.String("err", err.Error()),
		)
		return
	}
	client := s.Factory(string(bearer))

	session, err := client.GetAutonomous(ctx, sessionID)
	if err != nil {
		switch {
		case errors.Is(err, mosesclient.ErrNotFound):
			_ = s.persistSession(ctx, link.ID, row.ProviderChatID, nil, false)
			s.dm(ctx, link, "Autopilot session vanished; cleared bookkeeping.")
		case errors.Is(err, mosesclient.ErrUnauthorized):
			_ = s.Store.DeactivateLink(ctx, link.TenantID, link.ID, "platform_401")
			s.dm(ctx, link, "Your Moses key was revoked — please /unlink and re-link from the web UI.")
		default:
			s.Logger.Warn("autopilot sweep: get session",
				slog.String("session_id", sessionID.String()),
				slog.String("err", err.Error()),
			)
		}
		return
	}

	if !isTerminal(session.Status) {
		return
	}

	if err := s.persistSession(ctx, link.ID, row.ProviderChatID, nil, false); err != nil {
		s.Logger.Warn("autopilot sweep: clear",
			slog.String("link_id", link.ID.String()),
			slog.String("err", err.Error()),
		)
	}
	s.dm(ctx, link, completionSummary(session))
}

// persistSession is the single write path for autopilot bookkeeping.
// Centralised so future shape changes (e.g. recording last-status) have
// one call site to update.
func (s *Service) persistSession(ctx context.Context, linkID uuid.UUID, providerChatID string, sessionID *uuid.UUID, enabled bool) error {
	// Make sure a chat-state row exists before we try to update it; the
	// user might have run /autopilot start before sending any regular
	// message, in which case the relay-level GetOrCreate hasn't fired.
	if _, err := s.Store.GetOrCreate(ctx, linkID, providerChatID); err != nil {
		return err
	}
	return s.Store.UpdateAutopilot(ctx, linkID, providerChatID, sessionID, enabled)
}

func (s *Service) decryptBearer(link *db.ChatRelayLink) (string, error) {
	plaintext, err := s.Envelope.Decrypt(link.TenantID, link.EncryptedAPIKey, link.EncryptionKeyID)
	if err != nil {
		return "", fmt.Errorf("autopilot: decrypt api key: %w", err)
	}
	return string(plaintext), nil
}

// dm pushes a DM to the link's primary chat. Failures are logged and
// swallowed: the user not receiving a sweeper notice is annoying but not
// a reason to retry forever.
func (s *Service) dm(ctx context.Context, link *db.ChatRelayLink, text string) {
	if s.Sender == nil {
		return
	}
	if _, err := s.Sender.SendToLink(ctx, link, provider.OutboundMessage{Text: text}, nil); err != nil {
		s.Logger.Warn("autopilot dm failed",
			slog.String("link_id", link.ID.String()),
			slog.String("err", err.Error()),
		)
	}
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

// isTerminal reports whether a session status is one the sweeper should
// act on. Mirrors the platform's autonomous-session lifecycle.
func isTerminal(status string) bool {
	switch status {
	case "completed", "cancelled", "canceled", "failed":
		return true
	}
	return false
}

// completionSummary picks the best human-readable text for a terminal
// session: the platform's persisted Summary (set by CompletionAggregator)
// if available, otherwise a counter-derived fallback.
func completionSummary(session *mosesclient.AutonomousSession) string {
	if session.Summary != nil && *session.Summary != "" {
		return *session.Summary
	}
	return fmt.Sprintf(
		"Autopilot %s: %d tickets attempted (%d succeeded, %d failed, %d skipped).",
		session.Status,
		session.TicketsExecuted,
		session.TicketsSucceeded,
		session.TicketsFailed,
		session.TicketsSkipped,
	)
}

// formatStatus renders /autopilot status output. Multi-line; Telegram is
// happy with that.
func formatStatus(session *mosesclient.AutonomousSession) string {
	lines := []string{
		fmt.Sprintf("Autopilot session %s", shortID(session.ID)),
		fmt.Sprintf("Status: %s", session.Status),
		fmt.Sprintf("Mode: %s", session.Mode),
		fmt.Sprintf("Tickets: %d done (%d ok / %d failed / %d skipped)",
			session.TicketsExecuted,
			session.TicketsSucceeded,
			session.TicketsFailed,
			session.TicketsSkipped,
		),
		fmt.Sprintf("Concurrency: %d, retries: %d, timeout: %dh",
			session.MaxConcurrentAgents,
			session.MaxRetriesPerTicket,
			session.SessionTimeoutHours,
		),
		fmt.Sprintf("Started: %s", session.CreatedAt.UTC().Format(time.RFC3339)),
	}
	if session.CompletedAt.Time != nil {
		lines = append(lines, fmt.Sprintf("Completed: %s", session.CompletedAt.Time.UTC().Format(time.RFC3339)))
	}
	if session.Summary != nil && *session.Summary != "" {
		lines = append(lines, "Summary: "+*session.Summary)
	}
	return joinLines(lines)
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var n int
	for _, l := range lines {
		n += len(l) + 1
	}
	out := make([]byte, 0, n)
	for i, l := range lines {
		if i > 0 {
			out = append(out, '\n')
		}
		out = append(out, l...)
	}
	return string(out)
}

// shortID returns the first 8 hex chars of a UUID. Telegram users care
// about which session, not full canonical formatting.
func shortID(id uuid.UUID) string {
	s := id.String()
	if len(s) >= 8 {
		return s[:8]
	}
	return s
}

// ---------------------------------------------------------------------
// Compile-time assertions
// ---------------------------------------------------------------------

// _ ensures *db.Store can act as the autopilot Store. UpdateAutopilot,
// GetOrCreate, ListWithActiveAutopilot, GetLinkByIDAnyTenant,
// DeactivateLink all exist on the concrete store.
var _ Store = (*db.Store)(nil)
