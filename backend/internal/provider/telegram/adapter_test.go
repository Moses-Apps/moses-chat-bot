package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"moses-chat-bot/backend/internal/provider"
)

// Compile-time assertion mirroring adapter.go to make breakage in this file
// surface in the test report alongside the unit tests.
var _ provider.Provider = (*Adapter)(nil)

// newTestAdapter wires an Adapter to a httptest.Server impersonating the
// Telegram Bot API. handler may be nil if the test will not make HTTP calls.
func newTestAdapter(t *testing.T, cfg Config, handler http.HandlerFunc) (*Adapter, *httptest.Server) {
	t.Helper()
	var srv *httptest.Server
	if handler != nil {
		srv = httptest.NewServer(handler)
		t.Cleanup(srv.Close)
		cfg.BaseURL = srv.URL
		if cfg.HTTPClient == nil {
			cfg.HTTPClient = srv.Client()
		}
	}
	if cfg.BotToken == "" {
		cfg.BotToken = "test-token"
	}
	if cfg.WebhookSecret == "" {
		cfg.WebhookSecret = "test-secret"
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a, srv
}

func TestNew_RejectsEmptyToken(t *testing.T) {
	_, err := New(Config{BotToken: "", WebhookSecret: "x"})
	if err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestNew_RejectsEmptySecret(t *testing.T) {
	_, err := New(Config{BotToken: "x", WebhookSecret: "  "})
	if err == nil {
		t.Fatal("expected error for empty secret")
	}
}

func TestVerifyWebhookSignature_Valid(t *testing.T) {
	a, _ := newTestAdapter(t, Config{WebhookSecret: "expected"}, nil)
	h := http.Header{}
	h.Set(telegramSecretHeader, "expected")
	if err := a.VerifyWebhookSignature(h, []byte("body")); err != nil {
		t.Fatalf("VerifyWebhookSignature: %v", err)
	}
}

func TestVerifyWebhookSignature_Invalid(t *testing.T) {
	a, _ := newTestAdapter(t, Config{WebhookSecret: "expected"}, nil)
	h := http.Header{}
	h.Set(telegramSecretHeader, "wrong")
	err := a.VerifyWebhookSignature(h, []byte("body"))
	if !errors.Is(err, provider.ErrSignatureInvalid) {
		t.Fatalf("got %v, want ErrSignatureInvalid", err)
	}
}

func TestVerifyWebhookSignature_Missing(t *testing.T) {
	a, _ := newTestAdapter(t, Config{WebhookSecret: "expected"}, nil)
	err := a.VerifyWebhookSignature(http.Header{}, []byte("body"))
	if !errors.Is(err, provider.ErrSignatureInvalid) {
		t.Fatalf("got %v, want ErrSignatureInvalid", err)
	}
}

func TestHandleWebhook_TextMessage(t *testing.T) {
	a, _ := newTestAdapter(t, Config{}, nil)
	body := []byte(`{
		"update_id": 9876,
		"message": {
			"message_id": 1,
			"from": {"id": 4242, "is_bot": false, "first_name": "Phil"},
			"chat": {"id": 4242, "type": "private"},
			"date": 1700000000,
			"text": "hello bot"
		}
	}`)
	msgs, err := a.HandleWebhook(context.Background(), body, nil)
	if err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	m := msgs[0]
	if m.Provider != ProviderName {
		t.Errorf("Provider=%q", m.Provider)
	}
	if m.ProviderUserID != "4242" {
		t.Errorf("ProviderUserID=%q", m.ProviderUserID)
	}
	if m.ProviderChatID != "4242" {
		t.Errorf("ProviderChatID=%q", m.ProviderChatID)
	}
	if m.Text != "hello bot" {
		t.Errorf("Text=%q", m.Text)
	}
	if m.ProviderMessageID != "9876" {
		t.Errorf("ProviderMessageID=%q (should be update_id, not message_id)", m.ProviderMessageID)
	}
	if len(m.RawJSON) == 0 {
		t.Error("RawJSON empty")
	}
	if m.ReceivedAt.IsZero() {
		t.Error("ReceivedAt not set")
	}
}

func TestHandleWebhook_PhotoAttachment(t *testing.T) {
	a, _ := newTestAdapter(t, Config{}, nil)
	body := []byte(`{
		"update_id": 1,
		"message": {
			"message_id": 1,
			"from": {"id": 1, "is_bot": false},
			"chat": {"id": 1, "type": "private"},
			"date": 0,
			"caption": "look",
			"photo": [
				{"file_id": "small", "file_unique_id": "u1", "width": 90, "height": 90},
				{"file_id": "medium", "file_unique_id": "u2", "width": 320, "height": 320},
				{"file_id": "large", "file_unique_id": "u3", "width": 1280, "height": 1280}
			]
		}
	}`)
	msgs, err := a.HandleWebhook(context.Background(), body, nil)
	if err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Text != "look" {
		t.Errorf("Text=%q (expected caption fallback)", msgs[0].Text)
	}
	if len(msgs[0].Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(msgs[0].Attachments))
	}
	att := msgs[0].Attachments[0]
	if att.Kind != "photo" {
		t.Errorf("Kind=%q", att.Kind)
	}
	if att.Caption != "large" {
		t.Errorf("Caption=%q (should hold largest file_id)", att.Caption)
	}
}

func TestHandleWebhook_NoMessage_ReturnsEmpty(t *testing.T) {
	a, _ := newTestAdapter(t, Config{}, nil)
	body := []byte(`{"update_id": 1}`)
	msgs, err := a.HandleWebhook(context.Background(), body, nil)
	if err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected empty, got %+v", msgs)
	}
}

func TestSetupWebhook_AutoSetupFalse_NoOp(t *testing.T) {
	var hits int32
	a, _ := newTestAdapter(t, Config{AutoSetup: false}, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprint(w, `{"ok": true, "result": true}`)
	})
	if err := a.SetupWebhook(context.Background(), "https://example.com"); err != nil {
		t.Fatalf("SetupWebhook: %v", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("expected 0 HTTP calls, got %d", hits)
	}
}

func TestSetupWebhook_AutoSetupTrue_RegistersWithSecret(t *testing.T) {
	var setWebhookBody map[string]any
	var setWebhookHit, getWebhookHit int32

	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getWebhookInfo"):
			atomic.AddInt32(&getWebhookHit, 1)
			fmt.Fprint(w, `{"ok": true, "result": {"url": "", "has_custom_certificate": false, "pending_update_count": 0}}`)
		case strings.HasSuffix(r.URL.Path, "/setWebhook"):
			atomic.AddInt32(&setWebhookHit, 1)
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &setWebhookBody)
			fmt.Fprint(w, `{"ok": true, "result": true}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}

	a, _ := newTestAdapter(t, Config{AutoSetup: true, WebhookSecret: "topsekrit"}, handler)
	if err := a.SetupWebhook(context.Background(), "https://moses.example.com"); err != nil {
		t.Fatalf("SetupWebhook: %v", err)
	}
	if atomic.LoadInt32(&getWebhookHit) == 0 {
		t.Error("expected getWebhookInfo call")
	}
	if atomic.LoadInt32(&setWebhookHit) == 0 {
		t.Error("expected setWebhook call")
	}
	wantURL := "https://moses.example.com" + webhookPath
	if setWebhookBody["url"] != wantURL {
		t.Errorf("url=%v, want %s", setWebhookBody["url"], wantURL)
	}
	if setWebhookBody["secret_token"] != "topsekrit" {
		t.Errorf("secret_token=%v", setWebhookBody["secret_token"])
	}
}

func TestSetupWebhook_AutoSetupTrue_SkipsIfAlreadyRegistered(t *testing.T) {
	var setWebhookHit int32
	wantURL := "https://moses.example.com" + webhookPath

	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getWebhookInfo"):
			fmt.Fprintf(w, `{"ok": true, "result": {"url": %q, "has_custom_certificate": false, "pending_update_count": 0}}`, wantURL)
		case strings.HasSuffix(r.URL.Path, "/setWebhook"):
			atomic.AddInt32(&setWebhookHit, 1)
			fmt.Fprint(w, `{"ok": true, "result": true}`)
		}
	}
	a, _ := newTestAdapter(t, Config{AutoSetup: true}, handler)
	if err := a.SetupWebhook(context.Background(), "https://moses.example.com"); err != nil {
		t.Fatalf("SetupWebhook: %v", err)
	}
	if atomic.LoadInt32(&setWebhookHit) != 0 {
		t.Fatalf("expected 0 setWebhook calls when URL already matches, got %d", setWebhookHit)
	}
}

func TestSendMessage_Chunks_OverLimit(t *testing.T) {
	var calls int32
	var lastLen int

	handler := func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		body, _ := io.ReadAll(r.Body)
		var p SendMessageParams
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		lastLen = len(p.Text)
		if len(p.Text) > maxMessageLength {
			t.Errorf("chunk len %d exceeds max %d", len(p.Text), maxMessageLength)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok": true, "result": {"message_id": 1}}`)
	}

	a, _ := newTestAdapter(t, Config{}, handler)

	text := strings.Repeat("a", maxMessageLength*2+200) // ~3 chunks
	chat := provider.ChatRef{Provider: "telegram", ProviderChatID: "42"}
	if err := a.SendMessage(context.Background(), chat, provider.OutboundMessage{Text: text}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if atomic.LoadInt32(&calls) < 3 {
		t.Fatalf("expected >=3 chunk sends, got %d", calls)
	}
	if lastLen == 0 {
		t.Error("never captured chunk length")
	}
}

func TestSendMessage_RateLimited_RespectsRetryAfter(t *testing.T) {
	var calls int32
	handler := func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"ok": false, "error_code": 429, "parameters": {"retry_after": 1}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok": true, "result": {"message_id": 1}}`)
	}

	a, _ := newTestAdapter(t, Config{}, handler)
	start := time.Now()
	err := a.SendMessage(context.Background(), provider.ChatRef{ProviderChatID: "1"}, provider.OutboundMessage{Text: "x"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected 2 calls (rate-limit + retry), got %d", calls)
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("expected to wait ~1s, only waited %s", elapsed)
	}
}

func TestSendMessage_RateLimited_ExceedsBudget(t *testing.T) {
	// Always 429 with retry_after=40s, exceeding 30s budget on the first hit.
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "40")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"ok": false, "error_code": 429, "parameters": {"retry_after": 40}}`)
	}
	a, _ := newTestAdapter(t, Config{}, handler)
	err := a.SendMessage(context.Background(), provider.ChatRef{ProviderChatID: "1"}, provider.OutboundMessage{Text: "x"})
	if !errors.Is(err, provider.ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
}

func TestSendMessage_Network_Error_Wrapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	defer srv.Close()

	a, err := New(Config{
		BotToken:      "tok",
		WebhookSecret: "s",
		BaseURL:       srv.URL,
		HTTPClient:    srv.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = a.SendMessage(context.Background(), provider.ChatRef{ProviderChatID: "1"}, provider.OutboundMessage{Text: "hi"})
	if !errors.Is(err, provider.ErrProviderUnavailable) {
		t.Fatalf("expected ErrProviderUnavailable, got %v", err)
	}
}

func TestSendMessage_EmptyChatID_Errors(t *testing.T) {
	a, _ := newTestAdapter(t, Config{}, nil)
	err := a.SendMessage(context.Background(), provider.ChatRef{}, provider.OutboundMessage{Text: "hi"})
	if err == nil {
		t.Fatal("expected error for empty chat id")
	}
}

func TestSendMessage_EmptyText_NoOp(t *testing.T) {
	var hit int32
	a, _ := newTestAdapter(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		fmt.Fprint(w, `{"ok": true, "result": {}}`)
	})
	err := a.SendMessage(context.Background(), provider.ChatRef{ProviderChatID: "1"}, provider.OutboundMessage{Text: ""})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if atomic.LoadInt32(&hit) != 0 {
		t.Fatalf("empty text should not hit API, got %d calls", hit)
	}
}

func TestImplementsProviderInterface(t *testing.T) {
	// Sentinel test: if Adapter ever stops satisfying provider.Provider the
	// compile-time assertion at the top of this file (and adapter.go) fails.
	var _ provider.Provider = (*Adapter)(nil)
}
