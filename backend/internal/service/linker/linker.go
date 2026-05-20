// Package linker implements the user-facing /link handshake: code mint,
// poll, completion (from inbound provider command), and unlink.
//
// SPEC §4 — the frontend mints the user's platform API key directly via
// its same-origin Moses cookie; the plaintext is forwarded to the bot
// ONCE through POST /api/v1/links/codes, immediately encrypted under
// the per-tenant envelope, and held in pending_links with a 60s TTL.
// The Telegram /link handler in T-RELAY-1 calls CompleteLink to copy
// the encrypted blob into chat_relay_links and consume the pending row.
package linker

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/mosesclient"
	"moses-chat-bot/backend/internal/service/crypto"
)

// Sentinel errors. Callers (HTTP handlers, provider webhook) match these
// with errors.Is to render the right user-facing message.
var (
	ErrInvalidCode      = errors.New("linker: invalid code")
	ErrExpired          = errors.New("linker: code expired")
	ErrLockedOut        = errors.New("linker: too many failed attempts; try again later")
	ErrUnknownUser      = errors.New("linker: provider user has not registered via /start")
	ErrAlreadyLinked    = errors.New("linker: provider user already has an active link")
	ErrLinkNotFound     = errors.New("linker: link not found")
	ErrEmptyAPIKey      = errors.New("linker: api key required")
)

// Default knobs. The HTTP handler may override expiresIn per request,
// clamped to ttlMax to prevent a malicious caller from issuing very
// long-lived pending codes.
const (
	defaultCodeTTL = 60 * time.Second
	ttlMax         = 5 * time.Minute
	cleanupTick    = 30 * time.Second

	// Brute-force defense knobs.
	lockoutThreshold = 3
	lockoutWindow    = 15 * time.Minute
	lockoutDuration  = 15 * time.Minute
)

// Linker holds the dependencies required for the linking flow.
type Linker struct {
	store    *db.Store
	envelope *crypto.Envelope
	moses    *mosesclient.Client

	lockout *lockoutTable
	known   *knownTable
}

// New constructs a Linker. moses may be nil in tests that exercise the
// linking flow without exercising platform revocation; the Unlink path
// then degrades to deactivation-only.
func New(store *db.Store, envelope *crypto.Envelope, moses *mosesclient.Client) *Linker {
	return &Linker{
		store:    store,
		envelope: envelope,
		moses:    moses,
		lockout:  newLockoutTable(),
		known:    newKnownTable(),
	}
}

// RegisterKnown marks (provider, providerUserID) as having sent /start at
// least once. /link requires this gate so a stranger who guesses a code
// can't link before the legitimate Telegram user does (mild defense in
// depth — the 6-hex code space + lockout already make blind guessing
// impractical).
//
// NOTE: in-memory only. A bot restart clears the table and the user is
// asked to send /start again — a one-message inconvenience that avoids
// the operational burden of persisting per-provider-user state for
// what amounts to a courtesy check.
func (l *Linker) RegisterKnown(provider, providerUserID string) {
	l.known.set(provider, providerUserID)
}

// IsKnown reports whether RegisterKnown has been called for this user
// since process start.
func (l *Linker) IsKnown(provider, providerUserID string) bool {
	return l.known.has(provider, providerUserID)
}

// CreateCode mints a fresh 6-hex pending_links row encrypting plaintextAPIKey
// under the per-tenant envelope. Returns the user-facing code + the wall-clock
// expiry the frontend will render as a countdown.
//
// Tenant + user IDs are caller-supplied (they come from the same-origin
// session middleware), so this method does not consult the platform.
func (l *Linker) CreateCode(
	ctx context.Context,
	tenantID uuid.UUID,
	mosesUserID uuid.UUID,
	plaintextAPIKey string,
	apiKeyIDHint *uuid.UUID,
	expiresIn time.Duration,
) (string, time.Time, error) {
	if plaintextAPIKey == "" {
		return "", time.Time{}, ErrEmptyAPIKey
	}
	if expiresIn <= 0 {
		expiresIn = defaultCodeTTL
	}
	if expiresIn > ttlMax {
		expiresIn = ttlMax
	}

	code, err := generateCode()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("linker: generate code: %w", err)
	}

	ciphertext, keyID, err := l.envelope.Encrypt(tenantID, []byte(plaintextAPIKey))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("linker: encrypt: %w", err)
	}

	expiresAt := time.Now().Add(expiresIn)
	if err := l.store.CreatePendingLink(ctx, tenantID, mosesUserID, code, ciphertext, keyID, apiKeyIDHint, expiresAt); err != nil {
		return "", time.Time{}, fmt.Errorf("linker: store pending: %w", err)
	}
	return code, expiresAt, nil
}

// PollStatus is the discriminated union returned by PollCode.
type PollStatus string

const (
	StatusPending   PollStatus = "pending"
	StatusCompleted PollStatus = "completed"
	StatusExpired   PollStatus = "expired"
	StatusUnknown   PollStatus = "unknown"
)

// PollCode reports the current state of a code for the caller. The state
// machine: pending row exists & not expired → pending; pending row gone
// AND a chat_relay_link exists for (tenant, user) created since this
// code was minted → completed; pending row exists but past expires_at
// → expired; nothing matches → unknown.
//
// Completion correlation is best-effort: we surface the most recently
// created active link for (tenant, user) on the assumption the user has
// just one link-attempt in flight. PollCode is purely informational —
// the authoritative state transition happens in CompleteLink, not here.
func (l *Linker) PollCode(
	ctx context.Context,
	tenantID uuid.UUID,
	mosesUserID uuid.UUID,
	code string,
) (PollStatus, *uuid.UUID, error) {
	if !isValidCode(code) {
		return StatusUnknown, nil, nil
	}
	pl, err := l.store.GetPendingLinkByCode(ctx, tenantID, code)
	if err != nil && !db.IsNoRows(err) {
		return "", nil, fmt.Errorf("linker: poll: %w", err)
	}
	if err == nil {
		if pl.MosesUserID != mosesUserID {
			return StatusUnknown, nil, nil
		}
		if time.Now().After(pl.ExpiresAt) {
			return StatusExpired, nil, nil
		}
		return StatusPending, nil, nil
	}

	// No pending row — has the user completed this link?
	links, err := l.store.ListActiveLinksByMosesUser(ctx, tenantID, mosesUserID)
	if err != nil {
		return "", nil, fmt.Errorf("linker: list user links: %w", err)
	}
	if len(links) > 0 {
		// Heuristic: surface the most recently created link.
		id := links[0].ID
		return StatusCompleted, &id, nil
	}
	return StatusUnknown, nil, nil
}

// CompleteLink finalises a /link handshake from the provider side. Called
// only by the Telegram (and future Discord/Slack) webhook handler after
// it has decoded the /link <code> command. Validates:
//   - the provider user is known (sent /start)
//   - the provider user is not in lockout
//   - the code matches a non-expired pending row
//   - that provider_user_id is not already actively linked
//
// On success, copies the encrypted_api_key + tenant + user into a new
// chat_relay_links row inside a transaction and deletes the pending row.
// Resets lockout state for the user.
func (l *Linker) CompleteLink(
	ctx context.Context,
	code string,
	provider string,
	providerUserID string,
) (*db.ChatRelayLink, error) {
	provider = strings.TrimSpace(provider)
	providerUserID = strings.TrimSpace(providerUserID)
	if provider == "" || providerUserID == "" {
		return nil, ErrInvalidCode
	}

	if !l.IsKnown(provider, providerUserID) {
		return nil, ErrUnknownUser
	}
	if l.lockout.isLocked(provider, providerUserID, time.Now()) {
		return nil, ErrLockedOut
	}
	if !isValidCode(code) {
		l.lockout.recordFailure(provider, providerUserID, time.Now())
		return nil, ErrInvalidCode
	}

	pl, err := l.store.GetPendingLinkByCodeAnyTenant(ctx, code)
	if err != nil {
		if db.IsNoRows(err) {
			l.lockout.recordFailure(provider, providerUserID, time.Now())
			return nil, ErrInvalidCode
		}
		return nil, fmt.Errorf("linker: lookup code: %w", err)
	}

	// Constant-time confirm the code we just fetched matches the input,
	// even though we used it as the lookup key. Defense-in-depth against
	// any future change to the store layer that might fuzz comparisons.
	if subtle.ConstantTimeCompare([]byte(pl.Code), []byte(code)) != 1 {
		l.lockout.recordFailure(provider, providerUserID, time.Now())
		return nil, ErrInvalidCode
	}

	if time.Now().After(pl.ExpiresAt) {
		// Past TTL: surface as expired and clean up. Don't count toward
		// lockout — the code WAS the user's, they just took too long.
		_ = l.store.DeletePendingLink(ctx, pl.TenantID, pl.Code)
		return nil, ErrExpired
	}

	// Already-linked check: the partial-unique index will also catch this
	// at INSERT time, but doing the check up-front gives us a clean typed
	// error to return.
	existing, err := l.store.GetActiveLinkByProviderUser(ctx, provider, providerUserID)
	if err != nil && !db.IsNoRows(err) {
		return nil, fmt.Errorf("linker: check existing link: %w", err)
	}
	if existing != nil {
		return nil, ErrAlreadyLinked
	}

	var link *db.ChatRelayLink
	err = pgx.BeginFunc(ctx, l.store.Pool(), func(tx pgx.Tx) error {
		const insertSQL = `
			INSERT INTO chat_relay_links
				(moses_user_id, tenant_id, provider, provider_user_id, encrypted_api_key, encryption_key_id, api_key_id_hint)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id, moses_user_id, tenant_id, provider, provider_user_id, encrypted_api_key, encryption_key_id,
			          api_key_id_hint, is_active, last_used_at, created_at, deactivated_at, deactivation_reason
		`
		rows, err := tx.Query(ctx, insertSQL,
			pl.MosesUserID, pl.TenantID, provider, providerUserID,
			pl.EncryptedAPIKey, pl.EncryptionKeyID, pl.APIKeyIDHint,
		)
		if err != nil {
			return fmt.Errorf("insert link: %w", err)
		}
		defer rows.Close()
		newLink, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[db.ChatRelayLink])
		if err != nil {
			return fmt.Errorf("collect link: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM pending_links WHERE code = $1`, pl.Code); err != nil {
			return fmt.Errorf("delete pending: %w", err)
		}
		link = &newLink
		return nil
	})
	if err != nil {
		return nil, err
	}

	l.lockout.clear(provider, providerUserID)
	return link, nil
}

// Unlink soft-deactivates the link and best-effort revokes the underlying
// platform API key. A platform revoke failure does not block deactivation
// — the bot's own state is the source of truth for the relay; the API
// key may already have been revoked by the user from the platform UI,
// in which case 404 is the success case (mosesclient.RevokeAPIKey
// already collapses 404 to nil).
func (l *Linker) Unlink(ctx context.Context, tenantID uuid.UUID, mosesUserID uuid.UUID, linkID uuid.UUID) error {
	if linkID == uuid.Nil {
		return ErrLinkNotFound
	}
	link, err := l.findOwnedLink(ctx, tenantID, mosesUserID, linkID)
	if err != nil {
		return err
	}
	if err := l.store.DeactivateLink(ctx, tenantID, linkID, "user_unlink"); err != nil {
		return fmt.Errorf("linker: deactivate: %w", err)
	}
	if link.APIKeyIDHint != nil && l.moses != nil {
		if err := l.moses.RevokeAPIKey(ctx, *link.APIKeyIDHint); err != nil {
			log.Printf("linker: best-effort key revoke failed for link %s: %v", linkID, err)
		}
	}
	return nil
}

// findOwnedLink confirms (linkID) belongs to (tenantID, mosesUserID) before
// any privileged op; surfaces ErrLinkNotFound to avoid leaking existence
// of links in other tenants.
func (l *Linker) findOwnedLink(ctx context.Context, tenantID, mosesUserID, linkID uuid.UUID) (*db.ChatRelayLink, error) {
	links, err := l.store.ListActiveLinksByMosesUser(ctx, tenantID, mosesUserID)
	if err != nil {
		return nil, fmt.Errorf("linker: list user links: %w", err)
	}
	for i := range links {
		if links[i].ID == linkID {
			return &links[i], nil
		}
	}
	return nil, ErrLinkNotFound
}

// StartCleanupSweeper kicks off the background goroutine that purges
// pending_links rows past their TTL every cleanupTick. Stops when ctx is
// cancelled. Idempotent on shutdown — a SIGTERM cancels ctx and the
// goroutine returns on its next loop iteration.
func (l *Linker) StartCleanupSweeper(ctx context.Context) {
	go func() {
		t := time.NewTicker(cleanupTick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := l.store.CleanupExpiredPendingLinks(ctx, time.Now()); err != nil {
					log.Printf("linker: cleanup sweep failed: %v", err)
				} else if n > 0 {
					log.Printf("linker: cleaned %d expired pending link(s)", n)
				}
			}
		}
	}()
}

// SweepOnce runs a single cleanup pass synchronously. Exposed for tests
// and for the once-at-startup warm-up.
func (l *Linker) SweepOnce(ctx context.Context) (int64, error) {
	return l.store.CleanupExpiredPendingLinks(ctx, time.Now())
}

// generateCode returns a fresh 6-hex code drawn from crypto/rand. 24 bits
// of entropy (~16.8M possibilities) combined with 60s TTL + 3-strikes
// lockout makes guessing impractical.
func generateCode() (string, error) {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// isValidCode rejects malformed codes early so they don't burn a lockout
// strike and don't hit the DB.
func isValidCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ============================================================================
// knownTable — per-(provider, provider_user_id) "/start has been received" set.
// ============================================================================

type knownTable struct{ m sync.Map } // key string → time.Time

func newKnownTable() *knownTable { return &knownTable{} }

func keyOf(provider, providerUserID string) string {
	return provider + "/" + providerUserID
}

func (k *knownTable) set(provider, providerUserID string) {
	k.m.Store(keyOf(provider, providerUserID), time.Now())
}

func (k *knownTable) has(provider, providerUserID string) bool {
	_, ok := k.m.Load(keyOf(provider, providerUserID))
	return ok
}
