// Package relay implements the outbound machinery shared by the inbound-
// response path (T-RELAY-1) and the MM-initiated push path (T-PUSH-1).
//
// The Sender encapsulates: (1) per-link outbound rate limiting, (2) provider
// lookup via Registry, (3) persistence of chat_relay_messages rows including
// failure rows for audit.
package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/provider"
)

// Store is the narrow subset of db.Store the relay depends on. Defining it
// here (rather than reusing the concrete *db.Store) keeps the test surface
// small and lets unit tests mock without touching Postgres.
type Store interface {
	ListActiveLinksByMosesUser(ctx context.Context, tenantID, mosesUserID uuid.UUID) ([]db.ChatRelayLink, error)
	InsertMessage(
		ctx context.Context,
		linkID uuid.UUID,
		direction string,
		providerMessageID *string,
		mosesConversationID *uuid.UUID,
		text string,
		metadata []byte,
		errMsg *string,
	) (uuid.UUID, error)
}

// Compile-time guarantee that the concrete *db.Store satisfies our narrowed
// Store interface. If a method signature drifts on either side, the build
// fails here rather than at the call site in main.
var _ Store = (*db.Store)(nil)

// Errors surfaced by the Sender.
var (
	ErrRateLimited     = errors.New("relay: per-link outbound rate limit exceeded")
	ErrUnknownProvider = errors.New("relay: link references unknown provider")
)

// SenderOpts configures a Sender.
type SenderOpts struct {
	// PerLinkPerMinute is the token-bucket capacity per link.id. Default 30.
	PerLinkPerMinute int
	// Clock is overridable for deterministic tests; defaults to time.Now.
	Clock func() time.Time
}

// Sender ships OutboundMessages to provider chats and persists an audit row
// for every attempt (success or failure).
type Sender struct {
	store    Store
	registry *provider.Registry
	bucket   *Bucket
	clock    func() time.Time
}

// NewSender wires up a Sender. registry must already be populated with the
// adapters the caller intends to ship to.
func NewSender(store Store, registry *provider.Registry, opts SenderOpts) *Sender {
	if opts.PerLinkPerMinute <= 0 {
		opts.PerLinkPerMinute = 30
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	return &Sender{
		store:    store,
		registry: registry,
		bucket:   NewBucket(opts.PerLinkPerMinute, opts.Clock),
		clock:    opts.Clock,
	}
}

// Bucket exposes the rate limiter (e.g. so a callsite can start its sweeper).
func (s *Sender) Bucket() *Bucket { return s.bucket }

// ProviderFilter scopes a fan-out by provider and/or chat ID. Empty slices
// mean "no constraint on this dimension".
type ProviderFilter struct {
	Providers []string
	ChatIDs   []string
}

func (f ProviderFilter) matchesProvider(name string) bool {
	if len(f.Providers) == 0 {
		return true
	}
	for _, p := range f.Providers {
		if p == name {
			return true
		}
	}
	return false
}

func (f ProviderFilter) matchesChat(chatID string) bool {
	if len(f.ChatIDs) == 0 {
		return true
	}
	for _, c := range f.ChatIDs {
		if c == chatID {
			return true
		}
	}
	return false
}

// LinkResult is the per-link outcome of SendToMosesUser. Sent==true implies
// the provider's SendMessage returned nil; otherwise Error is populated. The
// audit row is persisted in either case and MessageRowID points at it.
type LinkResult struct {
	LinkID       uuid.UUID
	Provider     string
	ChatID       string
	Sent         bool
	Error        string
	MessageRowID uuid.UUID
}

// SendToLink delivers msg to exactly one link's primary chat. Used directly by
// the inbound-response path (RELAY-1) and by /links/:id/notify (PUSH-1).
//
// Persistence contract: every code path that returns from SendToLink (success,
// provider error, rate-limited, unknown-provider) inserts a chat_relay_messages
// row with direction='out'. The row's error column is non-null on failure.
// Callers can treat the returned msgID as the audit-trail anchor.
//
// mosesConversationID is optional; pass nil when the row should not be linked
// to a platform-side conversation (e.g. unsolicited push).
func (s *Sender) SendToLink(
	ctx context.Context,
	link *db.ChatRelayLink,
	msg provider.OutboundMessage,
	mosesConversationID *uuid.UUID,
) (uuid.UUID, error) {
	if link == nil {
		return uuid.Nil, errors.New("relay: nil link")
	}

	if !s.bucket.Allow(link.ID) {
		rowID := s.persistFailure(ctx, link.ID, mosesConversationID, msg.Text, "rate_limited")
		return rowID, ErrRateLimited
	}

	prov, ok := s.registry.Get(link.Provider)
	if !ok {
		rowID := s.persistFailure(ctx, link.ID, mosesConversationID, msg.Text, "unknown_provider")
		return rowID, fmt.Errorf("%w: %q", ErrUnknownProvider, link.Provider)
	}

	// v1 simplification: the link's primary provider chat is the same id as
	// the provider user. For Telegram this is exact (1:1 chats). Discord/Slack
	// adapters can extend this when they need per-link multi-chat semantics
	// (see SPEC §6 provider_chat_state).
	chatID := link.ProviderUserID
	chatRef := provider.ChatRef{Provider: link.Provider, ProviderChatID: chatID}

	sendErr := prov.SendMessage(ctx, chatRef, msg)
	if sendErr != nil {
		rowID := s.persistFailure(ctx, link.ID, mosesConversationID, msg.Text, sendErr.Error())
		return rowID, fmt.Errorf("relay: provider send failed: %w", sendErr)
	}

	rowID, err := s.store.InsertMessage(
		ctx,
		link.ID,
		"out",
		nil,
		mosesConversationID,
		msg.Text,
		successMetadata(msg),
		nil,
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("relay: persist outbound row: %w", err)
	}
	return rowID, nil
}

// SendToMosesUser fans out to every active link of (tenantID, mosesUserID),
// filtered by provider/chat. Per-link errors are surfaced in the returned
// slice — the top-level error is non-nil only for DB/tenant failures that
// prevent attempting any send at all.
func (s *Sender) SendToMosesUser(
	ctx context.Context,
	tenantID, mosesUserID uuid.UUID,
	msg provider.OutboundMessage,
	filter ProviderFilter,
) ([]LinkResult, error) {
	links, err := s.store.ListActiveLinksByMosesUser(ctx, tenantID, mosesUserID)
	if err != nil {
		return nil, fmt.Errorf("relay: list active links: %w", err)
	}

	results := make([]LinkResult, 0, len(links))
	for i := range links {
		link := links[i]
		if !filter.matchesProvider(link.Provider) {
			continue
		}
		// v1 simplification (same as SendToLink): chat_id == provider_user_id.
		chatID := link.ProviderUserID
		if !filter.matchesChat(chatID) {
			continue
		}

		rowID, sendErr := s.SendToLink(ctx, &link, msg, nil)
		lr := LinkResult{
			LinkID:       link.ID,
			Provider:     link.Provider,
			ChatID:       chatID,
			MessageRowID: rowID,
		}
		if sendErr != nil {
			lr.Error = sendErr.Error()
		} else {
			lr.Sent = true
		}
		results = append(results, lr)
	}
	return results, nil
}

// persistFailure writes an outbound row whose error column is non-null and
// returns its id. Failures to persist (rare; DB down) are deliberately
// swallowed: the caller is already returning an error, and emitting a second
// error here would mask the original cause. We return uuid.Nil in that case
// so callers can detect "no audit row was written".
func (s *Sender) persistFailure(
	ctx context.Context,
	linkID uuid.UUID,
	mosesConversationID *uuid.UUID,
	text string,
	errMsg string,
) uuid.UUID {
	rowID, err := s.store.InsertMessage(
		ctx,
		linkID,
		"out",
		nil,
		mosesConversationID,
		text,
		nil,
		&errMsg,
	)
	if err != nil {
		return uuid.Nil
	}
	return rowID
}

// successMetadata is the JSON blob stamped on a successful outbound row. We
// always emit a flat object so downstream consumers can extend without a
// schema change.
func successMetadata(msg provider.OutboundMessage) []byte {
	m := map[string]any{
		"markdown": msg.Markdown,
	}
	if msg.ReplyToID != "" {
		m["reply_to_id"] = msg.ReplyToID
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}
