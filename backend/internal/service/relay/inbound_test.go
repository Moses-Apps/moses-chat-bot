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
// invokes MM via StreamChatMessage and then polls GetConversationMessages
// for the persisted assistant reply, so the fake records the stream call
// and serves a scriptable message slice.
type fakeChatClient struct {
	mu sync.Mutex

	createErr error
	streamErr error

	createCalls int
	streamCalls int
	getMsgCalls int

	lastBearer    string
	lastConvID    string
	lastStreamMsg string

	// messages is the conversation history GetConversationMessages serves
	// (chronological order). On StreamChatMessage, the fake optionally
	// appends streamReply as a fresh assistant message so the relay's
	// poll observes the turn completing — see the test setups.
	messages []mosesclient.ChatMessage
	// streamReply, when non-empty, is appended as an assistant message
	// when StreamChatMessage fires (simulating the platform persisting
	// the turn synchronously enough for the next poll to see it).
	streamReply string
	// getMsgErrUntil makes the first N GetConversationMessages calls
	// return getMsgErr (transient-error simulation); 0 = never.
	getMsgErrUntil int
	getMsgErr      error
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
	if c.streamReply != "" {
		c.messages = append(c.messages, mosesclient.ChatMessage{
			ID:        uuid.New(),
			Role:      "assistant",
			Content:   c.streamReply,
			CreatedAt: time.Now().Add(time.Duration(len(c.messages)+1) * time.Millisecond),
		})
	}
	return &mosesclient.ChatStreamAck{Status: "processing", ConversationID: opts.ConversationID}, nil
}

func (c *fakeChatClient) GetConversationMessages(_ context.Context, _ uuid.UUID, limit int) ([]mosesclient.ChatMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getMsgCalls++
	if c.getMsgErr != nil && c.getMsgCalls <= c.getMsgErrUntil {
		return nil, c.getMsgErr
	}
	msgs := c.messages
	if limit > 0 && len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
	}
	out := make([]mosesclient.ChatMessage, len(msgs))
	copy(out, msgs)
	return out, nil
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
			// Tight poll cadence so tests that exercise the harvest path
			// finish fast; PollTimeout stays short enough that the
			// timeout-path test does not stall the suite.
			PollInterval: 5 * time.Millisecond,
			PollTimeout:  200 * time.Millisecond,
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

// TestHandleInbound_RegularMessage_FiresStreamThenPollsAndDelivers pins the
// reworked delivery model (supersedes the notifyLink-load-bearing model of
// commit 9f64861): a regular message fires the streaming MM invocation, then
// the relay OBTAINS the turn reply itself by polling the conversation for the
// persisted assistant message and delivers it via the provider adapter.
func TestHandleInbound_RegularMessage_FiresStreamThenPollsAndDelivers(t *testing.T) {
	fx := newFixture(t)
	seedLink(fx, "telegram", "tg-mm")
	// The fake appends this assistant reply when StreamChatMessage fires,
	// so the relay's poll observes the turn completing.
	fx.chat.streamReply = "hi there from Moses Manager"

	msg := provider.InboundMessage{
		Provider: "telegram", ProviderUserID: "tg-mm", ProviderChatID: "tg-mm",
		Text: "say hi", ProviderMessageID: "mm-1", ReceivedAt: time.Now(),
	}
	require.NoError(t, fx.relay.HandleInbound(context.Background(), msg))

	// The streaming turn was fired and the conversation was polled.
	fx.chat.mu.Lock()
	assert.GreaterOrEqual(t, fx.chat.streamCalls, 1, "expected the streaming chat path to fire")
	assert.NotEmpty(t, fx.chat.lastConvID, "stream call must carry a conversation id")
	assert.Contains(t, fx.chat.lastStreamMsg, "say hi", "the user's text must reach MM")
	assert.GreaterOrEqual(t, fx.chat.getMsgCalls, 1, "the relay must poll the conversation for the reply")
	fx.chat.mu.Unlock()

	// The relay delivers the harvested turn reply itself.
	sent := fx.tg.Snapshot()
	require.Len(t, sent, 1, "relay must deliver exactly one turn reply")
	assert.Equal(t, "hi there from Moses Manager", sent[0].Msg.Text)
	require.Len(t, fx.store.outbound(), 1, "the delivered reply must be persisted as an outbound row")
	assert.Equal(t, "hi there from Moses Manager", fx.store.outbound()[0].Text)
}

// TestHandleInbound_RegularMessage_PollTimeout pins the timeout-path UX: when
// the assistant reply does not land before PollTimeout, the relay sends the
// user a brief "still working" message rather than going silent. The turn is
// not abandoned server-side — only the relay's wait ends.
func TestHandleInbound_RegularMessage_PollTimeout(t *testing.T) {
	fx := newFixture(t)
	seedLink(fx, "telegram", "tg-slow")
	// streamReply left empty → the poll never finds an assistant message.

	msg := provider.InboundMessage{
		Provider: "telegram", ProviderUserID: "tg-slow", ProviderChatID: "tg-slow",
		Text: "do a long thing", ProviderMessageID: "slow-1", ReceivedAt: time.Now(),
	}
	require.NoError(t, fx.relay.HandleInbound(context.Background(), msg))

	sent := fx.tg.Snapshot()
	require.Len(t, sent, 1, "timeout must still produce a user-facing message")
	assert.Contains(t, sent[0].Msg.Text, "still working")
}

// TestHandleInbound_RegularMessage_PollSurvivesTransientError pins that a
// transient GetConversationMessages failure is retried, not fatal: the reply
// still gets delivered once the conversation poll succeeds.
func TestHandleInbound_RegularMessage_PollSurvivesTransientError(t *testing.T) {
	fx := newFixture(t)
	seedLink(fx, "telegram", "tg-flaky")
	fx.chat.streamReply = "recovered reply"
	// First two polls (the baseline read + the first harvest poll) 5xx;
	// after that the poll succeeds and finds the reply.
	fx.chat.getMsgErr = mosesclient.ErrServerError
	fx.chat.getMsgErrUntil = 2

	msg := provider.InboundMessage{
		Provider: "telegram", ProviderUserID: "tg-flaky", ProviderChatID: "tg-flaky",
		Text: "hello", ProviderMessageID: "flaky-1", ReceivedAt: time.Now(),
	}
	require.NoError(t, fx.relay.HandleInbound(context.Background(), msg))

	sent := fx.tg.Snapshot()
	require.Len(t, sent, 1, "transient poll errors must be retried, not abandon the turn")
	assert.Equal(t, "recovered reply", sent[0].Msg.Text)
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
// receive the user's text, the chat link id, and the split instruction —
// answer the turn normally (the relay delivers that reply itself by polling
// the conversation), and use notifyLink ONLY for async follow-ups.
func TestBuildRelayPrompt(t *testing.T) {
	link := &db.ChatRelayLink{ID: uuid.New()}
	msg := provider.InboundMessage{Provider: "telegram", Text: "deploy my app please"}

	got := buildRelayPrompt(link, msg)

	assert.Contains(t, got, "deploy my app please", "user's actual text must be relayed")
	assert.Contains(t, got, link.ID.String(), "MM must know which chat (link id) to address for follow-ups")
	assert.Contains(t, got, "notifyLink", "MM must be pointed at the notifyLink workspace tool for async follow-ups")
	assert.Contains(t, got, "automatically", "MM must be told the turn reply is delivered automatically")
	assert.Contains(t, got, "Telegram", "provider name should be surfaced, capitalized")
}
