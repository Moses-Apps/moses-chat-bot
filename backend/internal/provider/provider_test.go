package provider_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"moses-chat-bot/backend/internal/provider"
	"moses-chat-bot/backend/internal/provider/providertest"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := provider.NewRegistry()
	p := providertest.NewInMemoryProvider("telegram")

	if err := r.Register(p); err != nil {
		t.Fatalf("Register: unexpected error %v", err)
	}

	got, ok := r.Get("telegram")
	if !ok {
		t.Fatal("Get(\"telegram\"): not found")
	}
	if got.Name() != "telegram" {
		t.Fatalf("Get returned provider with Name=%q, want telegram", got.Name())
	}
}

func TestRegistry_DuplicateRegistration(t *testing.T) {
	r := provider.NewRegistry()
	p1 := providertest.NewInMemoryProvider("telegram")
	p2 := providertest.NewInMemoryProvider("telegram")

	if err := r.Register(p1); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := r.Register(p2)
	if !errors.Is(err, provider.ErrDuplicateProvider) {
		t.Fatalf("second Register: got %v, want ErrDuplicateProvider", err)
	}
}

func TestRegistry_RegisterNil(t *testing.T) {
	r := provider.NewRegistry()
	err := r.Register(nil)
	if !errors.Is(err, provider.ErrUnknownProvider) {
		t.Fatalf("Register(nil): got %v, want ErrUnknownProvider", err)
	}
}

func TestRegistry_GetMissing(t *testing.T) {
	r := provider.NewRegistry()
	if _, ok := r.Get("nope"); ok {
		t.Fatal("Get on empty registry returned ok=true")
	}
}

func TestRegistry_Names_Sorted(t *testing.T) {
	r := provider.NewRegistry()
	for _, n := range []string{"slack", "telegram", "discord"} {
		if err := r.Register(providertest.NewInMemoryProvider(n)); err != nil {
			t.Fatalf("Register(%q): %v", n, err)
		}
	}
	names := r.Names()
	want := []string{"discord", "slack", "telegram"}
	if len(names) != len(want) {
		t.Fatalf("Names len=%d, want %d", len(names), len(want))
	}
	for i := range names {
		if names[i] != want[i] {
			t.Fatalf("Names[%d]=%q, want %q", i, names[i], want[i])
		}
	}
}

func TestInMemoryProvider_HandleWebhook_DrainsInbound(t *testing.T) {
	p := providertest.NewInMemoryProvider("telegram")
	now := time.Now()
	p.QueueInbound(provider.InboundMessage{
		Provider:          "telegram",
		ProviderUserID:    "42",
		ProviderChatID:    "42",
		Text:              "hello",
		ReceivedAt:        now,
		ProviderMessageID: "1",
	})

	msgs, err := p.HandleWebhook(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Text != "hello" {
		t.Fatalf("HandleWebhook returned %+v", msgs)
	}

	// Second call drains to empty.
	msgs, err = p.HandleWebhook(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("HandleWebhook (2): %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("HandleWebhook (2): want empty, got %+v", msgs)
	}
}

func TestInMemoryProvider_SendMessage_RecordsCall(t *testing.T) {
	p := providertest.NewInMemoryProvider("telegram")
	chat := provider.ChatRef{Provider: "telegram", ProviderChatID: "42"}
	msg := provider.OutboundMessage{Text: "hi", Markdown: true}

	if err := p.SendMessage(context.Background(), chat, msg); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	got := p.Snapshot()
	if len(got) != 1 {
		t.Fatalf("Snapshot len=%d, want 1", len(got))
	}
	if got[0].Chat != chat || got[0].Msg.Text != "hi" || !got[0].Msg.Markdown {
		t.Fatalf("Snapshot[0]=%+v", got[0])
	}
}

func TestInMemoryProvider_SendMessage_PropagatesError(t *testing.T) {
	p := providertest.NewInMemoryProvider("telegram")
	p.SendErr = provider.ErrRateLimited

	err := p.SendMessage(context.Background(), provider.ChatRef{Provider: "telegram", ProviderChatID: "1"}, provider.OutboundMessage{Text: "x"})
	if !errors.Is(err, provider.ErrRateLimited) {
		t.Fatalf("SendMessage error=%v, want ErrRateLimited", err)
	}
	if len(p.Snapshot()) != 0 {
		t.Fatal("failing SendMessage must not record")
	}
}

func TestInMemoryProvider_VerifySignature_PropagatesError(t *testing.T) {
	p := providertest.NewInMemoryProvider("telegram")
	p.SignErr = provider.ErrSignatureInvalid

	err := p.VerifyWebhookSignature(nil, nil)
	if !errors.Is(err, provider.ErrSignatureInvalid) {
		t.Fatalf("VerifyWebhookSignature error=%v, want ErrSignatureInvalid", err)
	}
}
