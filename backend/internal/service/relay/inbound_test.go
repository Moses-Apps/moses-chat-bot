package relay

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/mosesclient"
	"moses-chat-bot/backend/internal/provider"
	"moses-chat-bot/backend/internal/provider/providertest"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// inboundFake is an in-memory InboundStore. Production-equivalent semantics
// for the methods exercised by HandleInbound; deliberately minimal — full
// CRUD lives in db_test against a real Postgres in handler tests.
type inboundFake struct {
	mu sync.Mutex

	linkByProviderUser map[string]*db.ChatRelayLink // key = provider|providerUserID
	dedup              map[string]bool              // key = linkID|providerMessageID
	chatState          map[string]*db.ProviderChatState // key = linkID|providerChatID
	deactivated        map[uuid.UUID]string             // linkID -> reason
	messages           []insertedRow

	getOrCreateErr   error
	deactivateErr    error
}

func newInboundFake() *inboundFake {
	return &inboundFake{
		linkByProviderUser: map[string]*db.ChatRelayLink{},
		dedup:              map[string]bool{},
		chatState:          map[string]*db.ProviderChatState{},
		deactivated:        map[uuid.UUID]string{},
	}
}

func (f *inboundFake) IsDuplicateInbound(_ context.Context, linkID uuid.UUID, pmid string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dedup[linkID.String()+"|"+pmid], nil
}

func (f *inboundFake) GetActiveLinkByProviderUser(_ context.Context, p, pu string) (*db.ChatRelayLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.linkByProviderUser[p+"|"+pu]
	if !ok {
		return nil, nil
	}
	cp := *v
	return &cp, nil
}

func (f *inboundFake) DeactivateLink(_ context.Context, tenantID, id uuid.UUID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deactivateErr != nil {
		return f.deactivateErr
	}
	f.deactivated[id] = reason
	return nil
}

func (f *inboundFake) InsertMessage(
	_ context.Context,
	linkID uuid.UUID,
	direction string,
	pmid *string,
	conv *uuid.UUID,
	text string,
	metadata []byte,
	errMsg *string,
) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uuid.New()
	f.messages = append(f.messages, insertedRow{
		ID: id, LinkID: linkID, Direction: direction, ProviderMessageID: pmid,
		MosesConversationID: conv, Text: text, Metadata: metadata, Error: errMsg,
	})
	if pmid != nil && *pmid != "" && direction == "in" {
		f.dedup[linkID.String()+"|"+*pmid] = true
	}
	return id, nil
}

func (f *inboundFake) GetOrCreate(_ context.Context, linkID uuid.UUID, providerChatID string) (*db.ProviderChatState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getOrCreateErr != nil {
		return nil, f.getOrCreateErr
	}
	k := linkID.String() + "|" + providerChatID
	if s, ok := f.chatState[k]; ok {
		cp := *s
		return &cp, nil
	}
	s := &db.ProviderChatState{
		ID:             uuid.New(),
		LinkID:         linkID,
		ProviderChatID: providerChatID,
	}
	f.chatState[k] = s
	cp := *s
	return &cp, nil
}

func (f *inboundFake) UpdateConversationID(_ context.Context, linkID uuid.UUID, providerChatID string, convID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := linkID.String() + "|" + providerChatID
	if s, ok := f.chatState[k]; ok {
		c := convID
		s.MosesConversationID = &c
	}
	return nil
}

func (f *inboundFake) ClearConversationID(_ context.Context, linkID uuid.UUID, providerChatID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := linkID.String() + "|" + providerChatID
	if s, ok := f.chatState[k]; ok {
		s.MosesConversationID = nil
	}
	return nil
}

func (f *inboundFake) TouchLastUsed(_ context.Context, _ uuid.UUID, _ uuid.UUID) error {
	return nil
}

func (f *inboundFake) ListActiveLinksByMosesUser(_ context.Context, _, _ uuid.UUID) ([]db.ChatRelayLink, error) {
	// Used by Sender during SendToMosesUser. Inbound test path doesn't fan
	// out, but kept here so the relay.Store interface remains satisfied.
	return nil, nil
}

func (f *inboundFake) seedLink(link *db.ChatRelayLink) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *link
	f.linkByProviderUser[link.Provider+"|"+link.ProviderUserID] = &cp
}

func (f *inboundFake) outboundCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.messages {
		if m.Direction == "out" {
			n++
		}
	}
	return n
}

func (f *inboundFake) outbound() []insertedRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]insertedRow, 0)
	for _, m := range f.messages {
		if m.Direction == "out" {
			out = append(out, m)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Fake mosesclient (per-key chat client)
// ---------------------------------------------------------------------------

// fakeChatClient stands in for a per-key *mosesclient.Client. The relay
// invokes MM via StreamChatMessage (fire-and-forget) and never harvests a
// reply, so the fake only records that the stream was fired.
type fakeChatClient struct {
	mu sync.Mutex

	createErr error
	streamErr error

	createCalls int
	streamCalls int

	lastBearer string
	lastConvID string
	lastStreamMsg string
}

func (c *fakeChatClient) CreateConversation(_ context.Context, _ mosesclient.CreateConversationOpts) (*mosesclient.Conversation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.createCalls++
	if c.createErr != nil {
		return nil, c.createErr
	}
	return &mosesclient.Conversation{ID: uuid.New()}, nil
}

func (c *fakeChatClient) StreamChatMessage(_ context.Context, opts mosesclient.ChatStreamOpts) (*mosesclient.ChatStreamAck, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.streamCalls++
	c.lastConvID = opts.ConversationID
	c.lastStreamMsg = opts.Message
	if c.streamErr != nil {
		return nil, c.streamErr
	}
	return &mosesclient.ChatStreamAck{Status: "processing", ConversationID: opts.ConversationID}, nil
}

// ---------------------------------------------------------------------------
// Test scaffolding
// ---------------------------------------------------------------------------

// inboundFixture bundles everything HandleInbound needs.
type inboundFixture struct {
	store    *inboundFake
	sender   *Sender
	tg       *providertest.InMemoryProvider
	relay    *Inbound
	chat     *fakeChatClient
	envelope *cryptoStub
}

// cryptoStub bypasses the real envelope so tests don't need a master key.
// It echoes ciphertext back as plaintext.
type cryptoStub struct{}

func (cryptoStub) Encrypt(_ uuid.UUID, plaintext []byte) ([]byte, string, error) {
	return append([]byte(nil), plaintext...), "v-test", nil
}
func (cryptoStub) Decrypt(_ uuid.UUID, ct []byte, _ string) ([]byte, error) {
	return append([]byte(nil), ct...), nil
}

// We can't substitute *crypto.Envelope directly because Inbound is typed
// against *crypto.Envelope. The simplest way to keep production typing
// while staying offline is to construct a real Envelope from a random
// master key — same approach as handler tests use.
func newTestEnvelopeForRelay(t *testing.T) *cryptoEnvelope {
	t.Helper()
	return newCryptoEnvelope(t)
}

func newFixture(t *testing.T) *inboundFixture {
	t.Helper()
	store := newInboundFake()
	tg := providertest.NewInMemoryProvider("telegram")
	reg := provider.NewRegistry()
	require.NoError(t, reg.Register(tg))
	env := newTestEnvelopeForRelay(t)

	// Linker requires a real *db.Store — we don't have one offline.
	// HandleInbound only invokes the linker for /unlink and RegisterKnown,
	// so passing nil here works as long as those paths aren't exercised.
	// Tests that do exercise unlink wire a separate testcontainer fixture.
	link := newOfflineLinker(t, env)

	sender := NewSender(adaptInboundFakeToSenderStore(store), reg, SenderOpts{})

	chat := &fakeChatClient{}
	relay := NewInbound(
		store, sender, env, link, reg,
		func(bearer string) PerKeyChatClient {
			chat.mu.Lock()
			chat.lastBearer = bearer
			chat.mu.Unlock()
			return chat
		},
		InboundOpts{
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	)
	return &inboundFixture{
		store: store, sender: sender, tg: tg, relay: relay, chat: chat,
	}
}

// adaptInboundFakeToSenderStore wraps inboundFake to satisfy relay.Store —
// the sender wants ListActiveLinksByMosesUser + InsertMessage, both
// already on inboundFake.
type senderStoreAdapter struct{ *inboundFake }

func (a senderStoreAdapter) ListActiveLinksByMosesUser(ctx context.Context, t, u uuid.UUID) ([]db.ChatRelayLink, error) {
	return a.inboundFake.ListActiveLinksByMosesUser(ctx, t, u)
}

func adaptInboundFakeToSenderStore(f *inboundFake) Store {
	return senderStoreAdapter{f}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestHandleInbound_NoLinkedUser_RepliesLinkInstructions(t *testing.T) {
	fx := newFixture(t)
	msg := provider.InboundMessage{
		Provider:          "telegram",
		ProviderUserID:    "tg-unknown",
		ProviderChatID:    "tg-unknown",
		Text:              "hello",
		ReceivedAt:        time.Now(),
		ProviderMessageID: "u-1",
	}
	require.NoError(t, fx.relay.HandleInbound(context.Background(), msg))
	sent := fx.tg.Snapshot()
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0].Msg.Text, "/link")
	// No DB row for unrecognised user — we can't tie it to a link.
	assert.Equal(t, 0, len(fx.store.messages))
}

func TestHandleInbound_Duplicate_Skipped(t *testing.T) {
	fx := newFixture(t)
	link := seedLink(fx, "telegram", "tg-1")
	msg := provider.InboundMessage{
		Provider: "telegram", ProviderUserID: "tg-1", ProviderChatID: "tg-1",
		Text: "/start", ProviderMessageID: "dup-1", ReceivedAt: time.Now(),
	}
	require.NoError(t, fx.relay.HandleInbound(context.Background(), msg))
	require.NoError(t, fx.relay.HandleInbound(context.Background(), msg))
	// Inbound row once + outbound (welcome) once on the first call. Second
	// call hits dedup before either insert or outbound.
	in := 0
	out := 0
	for _, m := range fx.store.messages {
		if m.LinkID != link.ID {
			continue
		}
		switch m.Direction {
		case "in":
			in++
		case "out":
			out++
		}
	}
	assert.Equal(t, 1, in, "inbound row inserted exactly once")
	assert.Equal(t, 1, out, "welcome reply sent exactly once")
}

func TestHandleInbound_SlashStart_RegistersKnown(t *testing.T) {
	fx := newFixture(t)
	seedLink(fx, "telegram", "tg-start")
	require.False(t, fx.relay.Linker.IsKnown("telegram", "tg-start"))

	msg := provider.InboundMessage{
		Provider: "telegram", ProviderUserID: "tg-start", ProviderChatID: "tg-start",
		Text: "/start", ProviderMessageID: "s-1", ReceivedAt: time.Now(),
	}
	require.NoError(t, fx.relay.HandleInbound(context.Background(), msg))
	assert.True(t, fx.relay.Linker.IsKnown("telegram", "tg-start"))
	sent := fx.tg.Snapshot()
	require.GreaterOrEqual(t, len(sent), 1)
	assert.Contains(t, sent[0].Msg.Text, "Welcome")
}

func TestHandleInbound_SlashLink_AlreadyLinked_Replies(t *testing.T) {
	// /link sent by someone whose provider_user_id is already actively linked
	// returns the already-linked message; we don't burn a lockout strike.
	fx := newFixture(t)
	seedLink(fx, "telegram", "tg-link-1")
	fx.relay.Linker.RegisterKnown("telegram", "tg-link-1")

	msg := provider.InboundMessage{
		Provider: "telegram", ProviderUserID: "tg-link-1", ProviderChatID: "tg-link-1",
		Text: "/link 123abc", ProviderMessageID: "l-1", ReceivedAt: time.Now(),
	}
	require.NoError(t, fx.relay.HandleInbound(context.Background(), msg))
	sent := fx.tg.Snapshot()
	require.GreaterOrEqual(t, len(sent), 1)
	assert.Contains(t, sent[0].Msg.Text, "already linked")
}

func TestHandleInbound_SlashStart_Unlinked_RegistersAndWelcomes(t *testing.T) {
	// Regression: a user with NO link sends /start. The link-resolution gate
	// used to return before command dispatch, so /start never ran. It must
	// now register the user as known and reply with the welcome.
	fx := newFixture(t)
	require.False(t, fx.relay.Linker.IsKnown("telegram", "tg-new"))

	msg := provider.InboundMessage{
		Provider: "telegram", ProviderUserID: "tg-new", ProviderChatID: "tg-new",
		Text: "/start", ProviderMessageID: "ns-1", ReceivedAt: time.Now(),
	}
	require.NoError(t, fx.relay.HandleInbound(context.Background(), msg))
	assert.True(t, fx.relay.Linker.IsKnown("telegram", "tg-new"))
	sent := fx.tg.Snapshot()
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0].Msg.Text, "Welcome")
}

func TestHandleInbound_SlashLink_Unlinked_ReachesLinker(t *testing.T) {
	// Regression: /link from a not-yet-linked user must reach linker.
	// CompleteLink — previously the no-link gate replied "I don't recognise
	// you" and /link never ran, making linking impossible. Here the user is
	// not known (no /start), so CompleteLink returns ErrUnknownUser, which
	// the relay surfaces as a "send /start first" reply — proving /link is
	// now dispatched for unlinked users.
	fx := newFixture(t)

	msg := provider.InboundMessage{
		Provider: "telegram", ProviderUserID: "tg-nolink", ProviderChatID: "tg-nolink",
		Text: "/link abc123", ProviderMessageID: "nl-1", ReceivedAt: time.Now(),
	}
	require.NoError(t, fx.relay.HandleInbound(context.Background(), msg))
	sent := fx.tg.Snapshot()
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0].Msg.Text, "/start")
	assert.NotContains(t, sent[0].Msg.Text, "don't recognise")
}

// TestHandleInbound_AutopilotMixedCase_HandledAsCommand is the regression
// guard for the reported bug: "/autopilot Start" (mobile keyboards
// autocapitalise) was rejected by the case-sensitive arg parser and silently
// forwarded to Moses Manager as a chat message. It must be dispatched as the
// autopilot command. The fixture wires no Autopilot service, so the command
// path replies "not configured" — a reply only dispatchCommand produces,
// proving the message was NOT relayed to MM.
func TestHandleInbound_AutopilotMixedCase_HandledAsCommand(t *testing.T) {
	fx := newFixture(t)
	seedLink(fx, "telegram", "tg-ap")

	msg := provider.InboundMessage{
		Provider: "telegram", ProviderUserID: "tg-ap", ProviderChatID: "tg-ap",
		Text: "/autopilot Start", ProviderMessageID: "ap-1", ReceivedAt: time.Now(),
	}
	require.NoError(t, fx.relay.HandleInbound(context.Background(), msg))

	sent := fx.tg.Snapshot()
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0].Msg.Text, "Autopilot service not configured")
}

func TestHandleInbound_SlashClear_ResetsConversation(t *testing.T) {
	fx := newFixture(t)
	link := seedLink(fx, "telegram", "tg-clear")
	// Pre-seed a chat-state with a conversation id.
	_, err := fx.store.GetOrCreate(context.Background(), link.ID, "tg-clear")
	require.NoError(t, err)
	conv := uuid.New()
	require.NoError(t, fx.store.UpdateConversationID(context.Background(), link.ID, "tg-clear", conv))

	msg := provider.InboundMessage{
		Provider: "telegram", ProviderUserID: "tg-clear", ProviderChatID: "tg-clear",
		Text: "/clear", ProviderMessageID: "c-1", ReceivedAt: time.Now(),
	}
	require.NoError(t, fx.relay.HandleInbound(context.Background(), msg))
	// Conversation cleared.
	state, err := fx.store.GetOrCreate(context.Background(), link.ID, "tg-clear")
	require.NoError(t, err)
	assert.Nil(t, state.MosesConversationID, "/clear should null the conversation pointer")
}

// TestHandleInbound_RegularMessage_FiresStreamNoHarvest pins the reworked
// delivery model: a regular message fires the streaming MM invocation and
// HandleInbound returns WITHOUT harvesting a reply. MM is expected to deliver
// its answer asynchronously by calling the notifyLink workspace tool, so the
// relay sends nothing to the provider on this path and persists no outbound
// row of its own.
func TestHandleInbound_RegularMessage_FiresStreamNoHarvest(t *testing.T) {
	fx := newFixture(t)
	seedLink(fx, "telegram", "tg-mm")

	msg := provider.InboundMessage{
		Provider: "telegram", ProviderUserID: "tg-mm", ProviderChatID: "tg-mm",
		Text: "say hi", ProviderMessageID: "mm-1", ReceivedAt: time.Now(),
	}
	require.NoError(t, fx.relay.HandleInbound(context.Background(), msg))

	// The streaming turn was fired; no synchronous harvest happens.
	fx.chat.mu.Lock()
	assert.GreaterOrEqual(t, fx.chat.streamCalls, 1, "expected the streaming chat path to fire")
	assert.NotEmpty(t, fx.chat.lastConvID, "stream call must carry a conversation id")
	assert.Contains(t, fx.chat.lastStreamMsg, "say hi", "the user's text must reach MM")
	assert.Contains(t, fx.chat.lastStreamMsg, "notifyLink", "the relay prompt must instruct MM to deliver via notifyLink")
	fx.chat.mu.Unlock()

	// The relay does NOT deliver the turn reply itself — MM pushes via
	// notifyLink. So nothing was sent to the provider and no outbound row
	// was persisted on this path.
	assert.Empty(t, fx.tg.Snapshot(), "relay must not send a turn reply itself")
	assert.Empty(t, fx.store.outbound(), "relay must not persist an outbound row for the turn")
}

func TestHandleInbound_401_DeactivatesLink_NotifiesUser(t *testing.T) {
	fx := newFixture(t)
	link := seedLink(fx, "telegram", "tg-401")
	fx.chat.createErr = mosesclient.ErrUnauthorized

	msg := provider.InboundMessage{
		Provider: "telegram", ProviderUserID: "tg-401", ProviderChatID: "tg-401",
		Text: "anything", ProviderMessageID: "401-1", ReceivedAt: time.Now(),
	}
	err := fx.relay.HandleInbound(context.Background(), msg)
	require.Error(t, err)
	require.ErrorIs(t, err, mosesclient.ErrUnauthorized)

	fx.store.mu.Lock()
	reason, ok := fx.store.deactivated[link.ID]
	fx.store.mu.Unlock()
	assert.True(t, ok)
	assert.Equal(t, "platform_401", reason)

	sent := fx.tg.Snapshot()
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0].Msg.Text, "revoked")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// seedLink creates a fake encrypted-payload link in the fake store and
// returns a copy. The "encrypted" payload is just the bearer string so
// the cryptoStub envelope decrypts it back transparently for the relay.
func seedLink(fx *inboundFixture, providerName, providerUserID string) db.ChatRelayLink {
	link := db.ChatRelayLink{
		ID:              uuid.New(),
		MosesUserID:     uuid.New(),
		TenantID:        uuid.New(),
		Provider:        providerName,
		ProviderUserID:  providerUserID,
		EncryptedAPIKey: []byte("bearer-" + providerUserID), // real envelope; see below
		EncryptionKeyID: "v1-test",
		IsActive:        true,
	}
	// We need ciphertext that decrypts under the real envelope to that
	// bearer. Easiest path: encrypt with the real envelope now using the
	// link's tenant id. The envelope is exposed via fx.relay.Envelope.
	ct, keyID, err := fx.relay.Envelope.Encrypt(link.TenantID, []byte("bearer-"+providerUserID))
	if err != nil {
		panic(err)
	}
	link.EncryptedAPIKey = ct
	link.EncryptionKeyID = keyID
	fx.store.seedLink(&link)
	return link
}

// Ensure mismatched test infra surfaces clearly.
func init() {
	_ = errors.New("force import")
}

// TestBuildRelayPrompt pins the relay-context contract: Moses Manager must
// receive the user's text, the chat link id, and an unambiguous instruction
// that it MUST deliver its reply itself via the notifyLink workspace tool
// (the relay no longer auto-delivers the turn reply).
func TestBuildRelayPrompt(t *testing.T) {
	link := &db.ChatRelayLink{ID: uuid.New()}
	msg := provider.InboundMessage{Provider: "telegram", Text: "deploy my app please"}

	got := buildRelayPrompt(link, msg)

	assert.Contains(t, got, "deploy my app please", "user's actual text must be relayed")
	assert.Contains(t, got, link.ID.String(), "MM must know which chat (link id) to address")
	assert.Contains(t, got, "notifyLink", "MM must be pointed at the notifyLink workspace tool")
	assert.Contains(t, got, "MUST", "MM must be told it MUST deliver its reply via notifyLink")
	assert.Contains(t, got, "Telegram", "provider name should be surfaced, capitalized")
}
