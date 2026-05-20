package db

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the database-access wrapper for all relay state. Tenant safety
// (rejection of uuid.Nil tenant_id) is enforced at the top of every method that
// takes a tenant_id; cross-tenant queries — e.g. GetActiveLinkByProviderUser,
// which must resolve a Telegram user_id without yet knowing the tenant — are
// explicit, documented, and limited in number.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool exposes the underlying pool for callers that need raw access (tests, tx).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func requireTenant(tenantID uuid.UUID) error {
	if tenantID == uuid.Nil {
		return ErrMissingTenantContext
	}
	return nil
}

// IsNoRows reports whether err is pgx.ErrNoRows, exposed for callers that don't
// want to import pgx directly.
func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// ============================================================================
// pending_links
// ============================================================================

func (s *Store) CreatePendingLink(
	ctx context.Context,
	tenantID uuid.UUID,
	mosesUserID uuid.UUID,
	code string,
	encryptedAPIKey []byte,
	encryptionKeyID string,
	apiKeyIDHint *uuid.UUID,
	expiresAt time.Time,
) error {
	if err := requireTenant(tenantID); err != nil {
		return err
	}
	const q = `
		INSERT INTO pending_links
			(code, moses_user_id, tenant_id, encrypted_api_key, encryption_key_id, api_key_id_hint, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	_, err := s.pool.Exec(ctx, q, code, mosesUserID, tenantID, encryptedAPIKey, encryptionKeyID, apiKeyIDHint, expiresAt)
	return err
}

func (s *Store) GetPendingLinkByCode(ctx context.Context, tenantID uuid.UUID, code string) (*PendingLink, error) {
	if err := requireTenant(tenantID); err != nil {
		return nil, err
	}
	const q = `
		SELECT code, moses_user_id, tenant_id, encrypted_api_key, encryption_key_id, api_key_id_hint, expires_at, created_at
		FROM pending_links
		WHERE tenant_id = $1 AND code = $2
	`
	rows, err := s.pool.Query(ctx, q, tenantID, code)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pl, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[PendingLink])
	if err != nil {
		return nil, err
	}
	return &pl, nil
}

// GetPendingLinkByCodeAnyTenant resolves a pending link by code without a known
// tenant. Used by /link handlers when the tenant is encoded inside the row, not
// the calling context (the bot endpoint that completes linking doesn't know
// the tenant until it reads this row). Internal only — never exposed to
// untrusted callers.
func (s *Store) GetPendingLinkByCodeAnyTenant(ctx context.Context, code string) (*PendingLink, error) {
	const q = `
		SELECT code, moses_user_id, tenant_id, encrypted_api_key, encryption_key_id, api_key_id_hint, expires_at, created_at
		FROM pending_links
		WHERE code = $1
	`
	rows, err := s.pool.Query(ctx, q, code)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pl, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[PendingLink])
	if err != nil {
		return nil, err
	}
	return &pl, nil
}

func (s *Store) DeletePendingLink(ctx context.Context, tenantID uuid.UUID, code string) error {
	if err := requireTenant(tenantID); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM pending_links WHERE tenant_id = $1 AND code = $2`, tenantID, code)
	return err
}

func (s *Store) CleanupExpiredPendingLinks(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM pending_links WHERE expires_at < $1`, now)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ============================================================================
// chat_relay_links
// ============================================================================

func (s *Store) CreateLink(
	ctx context.Context,
	tenantID uuid.UUID,
	mosesUserID uuid.UUID,
	provider string,
	providerUserID string,
	encryptedAPIKey []byte,
	encryptionKeyID string,
	apiKeyIDHint *uuid.UUID,
) (*ChatRelayLink, error) {
	if err := requireTenant(tenantID); err != nil {
		return nil, err
	}
	const q = `
		INSERT INTO chat_relay_links
			(moses_user_id, tenant_id, provider, provider_user_id, encrypted_api_key, encryption_key_id, api_key_id_hint)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, moses_user_id, tenant_id, provider, provider_user_id, encrypted_api_key, encryption_key_id,
		          api_key_id_hint, is_active, last_used_at, created_at, deactivated_at, deactivation_reason
	`
	rows, err := s.pool.Query(ctx, q, mosesUserID, tenantID, provider, providerUserID, encryptedAPIKey, encryptionKeyID, apiKeyIDHint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	link, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[ChatRelayLink])
	if err != nil {
		return nil, err
	}
	return &link, nil
}

// GetActiveLinkByProviderUser resolves the active link by (provider, provider_user_id)
// across all tenants. Required for inbound provider webhooks that haven't yet
// resolved a tenant. The partial-unique index guarantees at most one row.
func (s *Store) GetActiveLinkByProviderUser(ctx context.Context, provider, providerUserID string) (*ChatRelayLink, error) {
	const q = `
		SELECT id, moses_user_id, tenant_id, provider, provider_user_id, encrypted_api_key, encryption_key_id,
		       api_key_id_hint, is_active, last_used_at, created_at, deactivated_at, deactivation_reason
		FROM chat_relay_links
		WHERE provider = $1 AND provider_user_id = $2 AND is_active = true
	`
	rows, err := s.pool.Query(ctx, q, provider, providerUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	link, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[ChatRelayLink])
	if err != nil {
		return nil, err
	}
	return &link, nil
}

func (s *Store) GetActiveLinkByMosesUser(
	ctx context.Context,
	tenantID uuid.UUID,
	mosesUserID uuid.UUID,
	provider string,
) (*ChatRelayLink, error) {
	if err := requireTenant(tenantID); err != nil {
		return nil, err
	}
	const q = `
		SELECT id, moses_user_id, tenant_id, provider, provider_user_id, encrypted_api_key, encryption_key_id,
		       api_key_id_hint, is_active, last_used_at, created_at, deactivated_at, deactivation_reason
		FROM chat_relay_links
		WHERE tenant_id = $1 AND moses_user_id = $2 AND provider = $3 AND is_active = true
	`
	rows, err := s.pool.Query(ctx, q, tenantID, mosesUserID, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	link, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[ChatRelayLink])
	if err != nil {
		return nil, err
	}
	return &link, nil
}

func (s *Store) ListActiveLinksByMosesUser(
	ctx context.Context,
	tenantID uuid.UUID,
	mosesUserID uuid.UUID,
) ([]ChatRelayLink, error) {
	if err := requireTenant(tenantID); err != nil {
		return nil, err
	}
	const q = `
		SELECT id, moses_user_id, tenant_id, provider, provider_user_id, encrypted_api_key, encryption_key_id,
		       api_key_id_hint, is_active, last_used_at, created_at, deactivated_at, deactivation_reason
		FROM chat_relay_links
		WHERE tenant_id = $1 AND moses_user_id = $2 AND is_active = true
		ORDER BY created_at DESC
	`
	rows, err := s.pool.Query(ctx, q, tenantID, mosesUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[ChatRelayLink])
}

// ListAllActiveLinksByTenant returns every active link in the tenant. Used by
// the workspace-tool GET /workspace/links endpoint when MM asks "who has any
// provider linked?" without specifying a user.
func (s *Store) ListAllActiveLinksByTenant(
	ctx context.Context,
	tenantID uuid.UUID,
) ([]ChatRelayLink, error) {
	if err := requireTenant(tenantID); err != nil {
		return nil, err
	}
	const q = `
		SELECT id, moses_user_id, tenant_id, provider, provider_user_id, encrypted_api_key, encryption_key_id,
		       api_key_id_hint, is_active, last_used_at, created_at, deactivated_at, deactivation_reason
		FROM chat_relay_links
		WHERE tenant_id = $1 AND is_active = true
		ORDER BY created_at DESC
	`
	rows, err := s.pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[ChatRelayLink])
}

// GetLinkByIDAnyTenant resolves a link by id without tenant scoping. The
// caller MUST verify the returned TenantID matches the calling context — this
// method exists because the workspace-tool /workspace/links/:id/notify path
// needs to distinguish "no such link" (404) from "wrong tenant" (403). For
// any caller that doesn't need that distinction, prefer a tenant-scoped query.
//
// Internal use only; never expose this through an unauthenticated path.
func (s *Store) GetLinkByIDAnyTenant(
	ctx context.Context,
	id uuid.UUID,
) (*ChatRelayLink, error) {
	const q = `
		SELECT id, moses_user_id, tenant_id, provider, provider_user_id, encrypted_api_key, encryption_key_id,
		       api_key_id_hint, is_active, last_used_at, created_at, deactivated_at, deactivation_reason
		FROM chat_relay_links
		WHERE id = $1
	`
	rows, err := s.pool.Query(ctx, q, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	link, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[ChatRelayLink])
	if err != nil {
		return nil, err
	}
	return &link, nil
}

// GetLinkByID fetches a single link by id, scoped to tenantID. Returns
// pgx.ErrNoRows when no row matches (callers collapse to 404 so the bot
// never reveals cross-tenant existence). Both active and inactive rows
// are returned — callers decide whether to gate on IsActive.
func (s *Store) GetLinkByID(ctx context.Context, tenantID uuid.UUID, id uuid.UUID) (*ChatRelayLink, error) {
	if err := requireTenant(tenantID); err != nil {
		return nil, err
	}
	const q = `
		SELECT id, moses_user_id, tenant_id, provider, provider_user_id, encrypted_api_key, encryption_key_id,
		       api_key_id_hint, is_active, last_used_at, created_at, deactivated_at, deactivation_reason
		FROM chat_relay_links
		WHERE tenant_id = $1 AND id = $2
	`
	rows, err := s.pool.Query(ctx, q, tenantID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	link, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[ChatRelayLink])
	if err != nil {
		return nil, err
	}
	return &link, nil
}

func (s *Store) DeactivateLink(ctx context.Context, tenantID uuid.UUID, id uuid.UUID, reason string) error {
	if err := requireTenant(tenantID); err != nil {
		return err
	}
	const q = `
		UPDATE chat_relay_links
		SET is_active = false, deactivated_at = NOW(), deactivation_reason = $3
		WHERE tenant_id = $1 AND id = $2 AND is_active = true
	`
	_, err := s.pool.Exec(ctx, q, tenantID, id, reason)
	return err
}

func (s *Store) TouchLastUsed(ctx context.Context, tenantID uuid.UUID, id uuid.UUID) error {
	if err := requireTenant(tenantID); err != nil {
		return err
	}
	_, err := s.pool.Exec(
		ctx,
		`UPDATE chat_relay_links SET last_used_at = NOW() WHERE tenant_id = $1 AND id = $2`,
		tenantID, id,
	)
	return err
}

// ============================================================================
// chat_relay_messages
// ============================================================================

func (s *Store) InsertMessage(
	ctx context.Context,
	linkID uuid.UUID,
	direction string,
	providerMessageID *string,
	mosesConversationID *uuid.UUID,
	text string,
	metadata []byte,
	errMsg *string,
) (uuid.UUID, error) {
	if linkID == uuid.Nil {
		return uuid.Nil, errors.New("link_id is zero UUID")
	}
	if len(metadata) == 0 {
		metadata = []byte("{}")
	}
	const q = `
		INSERT INTO chat_relay_messages
			(link_id, direction, provider_message_id, moses_conversation_id, text, metadata, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, q, linkID, direction, providerMessageID, mosesConversationID, text, metadata, errMsg).Scan(&id)
	return id, err
}

func (s *Store) ListRecentByLink(ctx context.Context, linkID uuid.UUID, limit int) ([]ChatRelayMessage, error) {
	if linkID == uuid.Nil {
		return nil, errors.New("link_id is zero UUID")
	}
	if limit <= 0 {
		limit = 50
	}
	const q = `
		SELECT id, link_id, direction, provider_message_id, moses_conversation_id, text, metadata, occurred_at, error
		FROM chat_relay_messages
		WHERE link_id = $1
		ORDER BY occurred_at DESC
		LIMIT $2
	`
	rows, err := s.pool.Query(ctx, q, linkID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[ChatRelayMessage])
}

func (s *Store) ListRecentByMosesUser(
	ctx context.Context,
	tenantID uuid.UUID,
	mosesUserID uuid.UUID,
	limit int,
) ([]ChatRelayMessage, error) {
	if err := requireTenant(tenantID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	const q = `
		SELECT m.id, m.link_id, m.direction, m.provider_message_id, m.moses_conversation_id,
		       m.text, m.metadata, m.occurred_at, m.error
		FROM chat_relay_messages m
		JOIN chat_relay_links l ON l.id = m.link_id
		WHERE l.tenant_id = $1 AND l.moses_user_id = $2
		ORDER BY m.occurred_at DESC
		LIMIT $3
	`
	rows, err := s.pool.Query(ctx, q, tenantID, mosesUserID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[ChatRelayMessage])
}

func (s *Store) IsDuplicateInbound(ctx context.Context, linkID uuid.UUID, providerMessageID string) (bool, error) {
	if linkID == uuid.Nil {
		return false, errors.New("link_id is zero UUID")
	}
	if providerMessageID == "" {
		return false, nil
	}
	const q = `
		SELECT EXISTS (
			SELECT 1 FROM chat_relay_messages
			WHERE link_id = $1 AND direction = 'in' AND provider_message_id = $2
		)
	`
	var exists bool
	err := s.pool.QueryRow(ctx, q, linkID, providerMessageID).Scan(&exists)
	return exists, err
}

// ============================================================================
// provider_chat_state
// ============================================================================

// GetOrCreate returns the existing (link_id, provider_chat_id) row or creates an
// empty one. UPSERT via ON CONFLICT preserves any pre-existing autopilot/conv
// state. Tenant scoping is enforced indirectly by the FK to chat_relay_links —
// callers should pass a link_id they already own.
func (s *Store) GetOrCreate(ctx context.Context, linkID uuid.UUID, providerChatID string) (*ProviderChatState, error) {
	if linkID == uuid.Nil {
		return nil, errors.New("link_id is zero UUID")
	}
	const q = `
		INSERT INTO provider_chat_state (link_id, provider_chat_id)
		VALUES ($1, $2)
		ON CONFLICT (link_id, provider_chat_id) DO UPDATE
			SET updated_at = provider_chat_state.updated_at
		RETURNING id, link_id, provider_chat_id, moses_conversation_id,
		          autopilot_enabled, autopilot_session_id, settings, created_at, updated_at
	`
	rows, err := s.pool.Query(ctx, q, linkID, providerChatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[ProviderChatState])
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Store) UpdateConversationID(
	ctx context.Context,
	linkID uuid.UUID,
	providerChatID string,
	conversationID uuid.UUID,
) error {
	if linkID == uuid.Nil {
		return errors.New("link_id is zero UUID")
	}
	const q = `
		UPDATE provider_chat_state
		SET moses_conversation_id = $3, updated_at = NOW()
		WHERE link_id = $1 AND provider_chat_id = $2
	`
	_, err := s.pool.Exec(ctx, q, linkID, providerChatID, conversationID)
	return err
}

// ClearConversationID nulls the moses_conversation_id on a chat-state row.
// Used by the /clear slash command so the next inbound message opens a
// fresh Moses Manager conversation.
func (s *Store) ClearConversationID(ctx context.Context, linkID uuid.UUID, providerChatID string) error {
	if linkID == uuid.Nil {
		return errors.New("link_id is zero UUID")
	}
	const q = `
		UPDATE provider_chat_state
		SET moses_conversation_id = NULL, updated_at = NOW()
		WHERE link_id = $1 AND provider_chat_id = $2
	`
	_, err := s.pool.Exec(ctx, q, linkID, providerChatID)
	return err
}

func (s *Store) UpdateAutopilot(
	ctx context.Context,
	linkID uuid.UUID,
	providerChatID string,
	sessionID *uuid.UUID,
	enabled bool,
) error {
	if linkID == uuid.Nil {
		return errors.New("link_id is zero UUID")
	}
	const q = `
		UPDATE provider_chat_state
		SET autopilot_enabled = $3, autopilot_session_id = $4, updated_at = NOW()
		WHERE link_id = $1 AND provider_chat_id = $2
	`
	_, err := s.pool.Exec(ctx, q, linkID, providerChatID, enabled, sessionID)
	return err
}

func (s *Store) ListByLink(ctx context.Context, linkID uuid.UUID) ([]ProviderChatState, error) {
	if linkID == uuid.Nil {
		return nil, errors.New("link_id is zero UUID")
	}
	const q = `
		SELECT id, link_id, provider_chat_id, moses_conversation_id,
		       autopilot_enabled, autopilot_session_id, settings, created_at, updated_at
		FROM provider_chat_state
		WHERE link_id = $1
		ORDER BY created_at DESC
	`
	rows, err := s.pool.Query(ctx, q, linkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProviderChatState])
}

// ListWithActiveAutopilot returns every chat-state row that has a non-null
// autopilot_session_id, joined with its link for tenant context. Used by the
// autopilot sweeper to poll the platform for terminal session states.
func (s *Store) ListWithActiveAutopilot(ctx context.Context) ([]ProviderChatState, error) {
	const q = `
		SELECT id, link_id, provider_chat_id, moses_conversation_id,
		       autopilot_enabled, autopilot_session_id, settings, created_at, updated_at
		FROM provider_chat_state
		WHERE autopilot_session_id IS NOT NULL
		ORDER BY updated_at ASC
	`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProviderChatState])
}
