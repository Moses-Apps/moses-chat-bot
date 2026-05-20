// Package providertest contains test doubles for the provider package.
// It lives in a separate package so production code never imports it.
package providertest

import (
	"context"
	"net/http"
	"sync"
	"time"

	"moses-chat-bot/backend/internal/provider"
)

// SentRecord captures a single SendMessage invocation for assertion.
type SentRecord struct {
	Chat provider.ChatRef
	Msg  provider.OutboundMessage
	At   time.Time
}

// InMemoryProvider is a deterministic in-process Provider. Tests prepopulate
// Inbound to drive HandleWebhook and inspect Sent to assert SendMessage was
// called with the expected payload. The zero value is not usable; construct
// via NewInMemoryProvider.
type InMemoryProvider struct {
	NameValue string

	mu      sync.Mutex
	Sent    []SentRecord
	Inbound []provider.InboundMessage
	SignErr error
	SendErr error
	SetupErr error
}

func NewInMemoryProvider(name string) *InMemoryProvider {
	return &InMemoryProvider{NameValue: name}
}

func (p *InMemoryProvider) Name() string { return p.NameValue }

// HandleWebhook drains the prepopulated Inbound queue and returns the
// messages. body and headers are ignored; tests that need to assert on
// raw payloads should drive the real adapter instead.
func (p *InMemoryProvider) HandleWebhook(_ context.Context, _ []byte, _ http.Header) ([]provider.InboundMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.Inbound
	p.Inbound = nil
	return out, nil
}

func (p *InMemoryProvider) SendMessage(_ context.Context, chat provider.ChatRef, msg provider.OutboundMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.SendErr != nil {
		return p.SendErr
	}
	p.Sent = append(p.Sent, SentRecord{Chat: chat, Msg: msg, At: time.Now()})
	return nil
}

func (p *InMemoryProvider) SetupWebhook(_ context.Context, _ string) error {
	return p.SetupErr
}

func (p *InMemoryProvider) VerifyWebhookSignature(_ http.Header, _ []byte) error {
	return p.SignErr
}

// Snapshot returns a copy of the recorded SendMessage calls so tests can
// assert without holding the provider's lock.
func (p *InMemoryProvider) Snapshot() []SentRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]SentRecord, len(p.Sent))
	copy(out, p.Sent)
	return out
}

// QueueInbound appends messages that the next HandleWebhook call will return.
func (p *InMemoryProvider) QueueInbound(msgs ...provider.InboundMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Inbound = append(p.Inbound, msgs...)
}

// Compile-time check that we satisfy the interface.
var _ provider.Provider = (*InMemoryProvider)(nil)
