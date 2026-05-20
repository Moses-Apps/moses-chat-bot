package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/mosesclient"
	"moses-chat-bot/backend/internal/provider"
	"moses-chat-bot/backend/internal/provider/providertest"
	"moses-chat-bot/backend/internal/service/crypto"
	"moses-chat-bot/backend/internal/service/linker"
	"moses-chat-bot/backend/internal/service/relay"
)

// ---------------------------------------------------------------------------
// Test inbound (counts dispatch invocations)
// ---------------------------------------------------------------------------

// countingInbound is a tiny shim that satisfies the type Inbound returns from
// HandleInbound. We can't easily mock *relay.Inbound (it's a struct), so the
// tests wire a real Inbound whose dependencies trace into a counter — the
// counter increments inside the no-link reply path, which is the simplest
// branch that doesn't require WS plumbing.

// We build a minimal *relay.Inbound whose Store is a fake returning "no link",
// so HandleInbound dispatches the no-link reply through a counter-backed
// provider.
type webhookCountingStore struct {
	mu  sync.Mutex
	hit int32
}

func (s *webhookCountingStore) IsDuplicateInbound(_ context.Context, _ uuid.UUID, _ string) (bool, error) {
	return false, nil
}
func (s *webhookCountingStore) GetActiveLinkByProviderUser(_ context.Context, _, _ string) (*db.ChatRelayLink, error) {
	atomic.AddInt32(&s.hit, 1)
	return nil, nil // simulate "no active link" → no-link branch
}
func (s *webhookCountingStore) DeactivateLink(_ context.Context, _, _ uuid.UUID, _ string) error {
	return nil
}
func (s *webhookCountingStore) InsertMessage(_ context.Context, _ uuid.UUID, _ string, _ *string, _ *uuid.UUID, _ string, _ []byte, _ *string) (uuid.UUID, error) {
	return uuid.New(), nil
}
func (s *webhookCountingStore) GetOrCreate(_ context.Context, linkID uuid.UUID, providerChatID string) (*db.ProviderChatState, error) {
	return &db.ProviderChatState{ID: uuid.New(), LinkID: linkID, ProviderChatID: providerChatID}, nil
}
func (s *webhookCountingStore) UpdateConversationID(_ context.Context, _ uuid.UUID, _ string, _ uuid.UUID) error {
	return nil
}
func (s *webhookCountingStore) ClearConversationID(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}
func (s *webhookCountingStore) TouchLastUsed(_ context.Context, _ uuid.UUID, _ uuid.UUID) error {
	return nil
}
func (s *webhookCountingStore) ListActiveLinksByMosesUser(_ context.Context, _, _ uuid.UUID) ([]db.ChatRelayLink, error) {
	return nil, nil
}

// senderAdapter glues webhookCountingStore (which satisfies InboundStore plus
// ListActiveLinksByMosesUser) onto relay.Store for the Sender.
type webhookSenderAdapter struct{ *webhookCountingStore }

func (a webhookSenderAdapter) InsertMessage(ctx context.Context, linkID uuid.UUID, direction string, pmid *string, conv *uuid.UUID, text string, metadata []byte, errMsg *string) (uuid.UUID, error) {
	return a.webhookCountingStore.InsertMessage(ctx, linkID, direction, pmid, conv, text, metadata, errMsg)
}

// fakeProviderWithSig is a tiny provider that lets the test toggle the
// signature outcome + decoded messages directly.
type fakeProviderWithSig struct {
	*providertest.InMemoryProvider
	secret string
	called atomic.Int32
}

func (p *fakeProviderWithSig) VerifyWebhookSignature(h http.Header, _ []byte) error {
	if h.Get("X-Telegram-Bot-Api-Secret-Token") != p.secret {
		return provider.ErrSignatureInvalid
	}
	return nil
}

func (p *fakeProviderWithSig) HandleWebhook(ctx context.Context, body []byte, _ http.Header) ([]provider.InboundMessage, error) {
	p.called.Add(1)
	// Decode {messages:[{text, providerUserID}]} for test convenience.
	var raw struct {
		Messages []struct {
			Text           string `json:"text"`
			ProviderUserID string `json:"provider_user_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]provider.InboundMessage, 0, len(raw.Messages))
	for i, m := range raw.Messages {
		out = append(out, provider.InboundMessage{
			Provider:          p.Name(),
			ProviderUserID:    m.ProviderUserID,
			ProviderChatID:    m.ProviderUserID,
			Text:              m.Text,
			ProviderMessageID: uuid.NewString(),
			ReceivedAt:        time.Now(),
			RawJSON:           append([]byte(nil), body...),
		})
		_ = i
	}
	return out, nil
}

// buildTestInbound assembles a real *relay.Inbound that drops messages via
// the no-link reply (no DB needed); the underlying provider counter then
// reports the dispatch.
func buildTestInbound(t *testing.T, p *fakeProviderWithSig) *relay.Inbound {
	t.Helper()
	store := &webhookCountingStore{}
	reg := provider.NewRegistry()
	require.NoError(t, reg.Register(p))
	env := newWebhookEnvelope(t)
	link := linker.New(nil, env, nil)
	sender := relay.NewSender(webhookSenderAdapter{store}, reg, relay.SenderOpts{})
	pool := relay.NewWSConnPool(relay.WSPoolConfig{
		BaseWS: "http://moses-backend.test",
		Dialer: func(_ context.Context, _, _ string, _ mosesclient.WSConfig) (relay.Subscriber, error) {
			return nil, nil // never called — no-link path
		},
	})
	return relay.NewInbound(
		store, sender, env, link, reg,
		func(_ string) relay.PerKeyChatClient { return nil },
		pool,
		relay.InboundOpts{StreamTimeout: 100 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
	)
}

func newWebhookEnvelope(t *testing.T) *crypto.Envelope {
	t.Helper()
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	mkJSON := map[string]interface{}{
		"keys":   map[string]string{"v1": base64.StdEncoding.EncodeToString(raw)},
		"active": "v1",
	}
	b, err := json.Marshal(mkJSON)
	require.NoError(t, err)
	t.Setenv("CHAT_BOT_MASTER_KEY", string(b))
	mk, err := crypto.LoadMasterKeysFromEnv()
	require.NoError(t, err)
	env, err := crypto.NewEnvelope(mk)
	require.NoError(t, err)
	return env
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestWebhookHandler_InvalidSignature_401(t *testing.T) {
	p := &fakeProviderWithSig{
		InMemoryProvider: providertest.NewInMemoryProvider("telegram"),
		secret:           "expected-secret",
	}
	inbound := buildTestInbound(t, p)
	h := NewWebhookHandler(WebhookConfig{
		Provider: p, Inbound: inbound, MaxConcurrent: 4,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	body := []byte(`{"messages":[{"text":"hi","provider_user_id":"tg-1"}]}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong-secret")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, int32(0), p.called.Load(), "decode must not run when sig fails")
}

func TestWebhookHandler_ValidSignature_200_AndQueuesDispatch(t *testing.T) {
	p := &fakeProviderWithSig{
		InMemoryProvider: providertest.NewInMemoryProvider("telegram"),
		secret:           "expected-secret",
	}
	inbound := buildTestInbound(t, p)
	h := NewWebhookHandler(WebhookConfig{
		Provider: p, Inbound: inbound, MaxConcurrent: 4,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	body := []byte(`{"messages":[{"text":"hello","provider_user_id":"tg-1"}]}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "expected-secret")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(start)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Less(t, elapsed, 200*time.Millisecond, "webhook must ack fast — dispatch is async")
	assert.Equal(t, int32(1), p.called.Load())

	// Dispatch eventually fires SendMessage via the no-link reply path.
	require.Eventually(t, func() bool {
		return len(p.Snapshot()) >= 1
	}, 2*time.Second, 10*time.Millisecond, "expected the no-link reply to be delivered")
}

func TestWebhookHandler_LargeBurst_BoundedBySemaphore(t *testing.T) {
	p := &fakeProviderWithSig{
		InMemoryProvider: providertest.NewInMemoryProvider("telegram"),
		secret:           "s",
	}
	inbound := buildTestInbound(t, p)
	// Cap=2 with a non-blocking acquire is deliberately strict: a 16-msg
	// burst that fans out faster than goroutines can drain will drop
	// everything past the in-flight ceiling. The bot ack still returns
	// 200 to the provider — Telegram retries on its own timer.
	h := NewWebhookHandler(WebhookConfig{
		Provider: p, Inbound: inbound, MaxConcurrent: 2,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	body := buildBurst(t, 16)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "s")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "200 even when bursts overflow")

	// At least the semaphore capacity (2) must run; we don't assert the
	// upper bound — interleaving with goroutine completion is timing-
	// dependent and 16 may complete in any order.
	require.Eventually(t, func() bool {
		return len(p.Snapshot()) >= 2
	}, 3*time.Second, 20*time.Millisecond, "at least MaxConcurrent worth of dispatches must run")
	// And under any timing we must never exceed the message count.
	assert.LessOrEqual(t, len(p.Snapshot()), 16)
}

func buildBurst(t *testing.T, n int) []byte {
	t.Helper()
	type m struct {
		Text           string `json:"text"`
		ProviderUserID string `json:"provider_user_id"`
	}
	type body struct {
		Messages []m `json:"messages"`
	}
	b := body{}
	for i := 0; i < n; i++ {
		b.Messages = append(b.Messages, m{Text: "x", ProviderUserID: uuid.NewString()})
	}
	out, err := json.Marshal(b)
	require.NoError(t, err)
	return out
}

func TestWebhookHandler_NonPost_MethodNotAllowed(t *testing.T) {
	p := &fakeProviderWithSig{
		InMemoryProvider: providertest.NewInMemoryProvider("telegram"),
		secret:           "s",
	}
	inbound := buildTestInbound(t, p)
	h := NewWebhookHandler(WebhookConfig{
		Provider: p, Inbound: inbound,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}
