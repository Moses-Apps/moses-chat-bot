package relay

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"moses-chat-bot/backend/internal/mosesclient"
)

// WSDialer abstracts NewWSSubscriber so tests can substitute fakes that do
// not actually open TCP sockets. The signature mirrors mosesclient.NewWSSubscriber
// so production callers can pass it directly.
type WSDialer func(ctx context.Context, baseWS, token string, cfg mosesclient.WSConfig) (Subscriber, error)

// Subscriber is the narrow subset of *mosesclient.WSSubscriber the relay
// needs at runtime. Keeping it abstract lets the WS pool tests run without
// a live websocket.
type Subscriber interface {
	Subscribe(topic, topicID string) error
	Events() <-chan mosesclient.WSEvent
	Close() error
}

// DefaultWSDialer adapts mosesclient.NewWSSubscriber to the Subscriber
// interface — the concrete type already implements every method.
func DefaultWSDialer(ctx context.Context, baseWS, token string, cfg mosesclient.WSConfig) (Subscriber, error) {
	return mosesclient.NewWSSubscriber(ctx, baseWS, token, cfg)
}

// ErrWSPoolClosed signals a Get against a pool that has already been Stopped.
var ErrWSPoolClosed = errors.New("relay: ws pool closed")

// pooledConn carries one persistent subscriber for a single chat_relay_link.
// One WS per link is required because the WS handshake URL embeds the user's
// MCP API key as a query parameter — different users cannot share a socket.
// The pool reuses the same subscriber across many conversations belonging to
// the same link by tracking subscribed conversation IDs in a set.
type pooledConn struct {
	mu          sync.Mutex
	sub         Subscriber
	lastUsedAt  time.Time
	convs       map[uuid.UUID]bool
}

// WSPoolConfig configures a wsConnPool.
type WSPoolConfig struct {
	// BaseWS is the moses-backend WS root (typically http://moses-backend...).
	// mosesclient.NewWSSubscriber rewrites http(s) to ws(s) internally.
	BaseWS string

	// IdleTTL is how long a connection may go without activity before
	// Sweep() will close it. Default 10m.
	IdleTTL time.Duration

	// Dialer overrides DefaultWSDialer; tests inject fakes here.
	Dialer WSDialer

	// SubscriberConfig is forwarded to the dialer on every connect. The
	// defaults inside WSConfig.withDefaults() are production-safe; tests
	// shrink timeouts here for speed.
	SubscriberConfig mosesclient.WSConfig

	// Clock is overridable for deterministic sweeper tests.
	Clock func() time.Time
}

// wsConnPool keeps one persistent WS subscriber per link.ID. Each call to
// Get returns the existing connection (and ensures the conversation is
// subscribed) or lazily opens a new one. Callers must invoke Sweep
// periodically to release idle connections.
type wsConnPool struct {
	mu      sync.Mutex
	conns   map[uuid.UUID]*pooledConn
	cfg     WSPoolConfig
	dialer  WSDialer
	clock   func() time.Time
	closed  bool
}

// NewWSConnPool builds an empty pool. The pool is safe for concurrent use.
func NewWSConnPool(cfg WSPoolConfig) *wsConnPool {
	if cfg.IdleTTL <= 0 {
		cfg.IdleTTL = 10 * time.Minute
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	dialer := cfg.Dialer
	if dialer == nil {
		dialer = DefaultWSDialer
	}
	return &wsConnPool{
		conns:  make(map[uuid.UUID]*pooledConn),
		cfg:    cfg,
		dialer: dialer,
		clock:  cfg.Clock,
	}
}

// Get returns a subscriber for linkID, lazily dialing if absent and
// subscribing to convID if not already done. The returned Subscriber is
// owned by the pool — callers must NOT call Close on it directly.
//
// bearer is the plaintext MCP API key for this link (already decrypted by
// the caller). It is used only for the dial and is not retained on the
// pooledConn so future leaks via memory dump are minimal — though the
// underlying *mosesclient.WSSubscriber does retain it internally for
// reconnects, which is unavoidable.
func (p *wsConnPool) Get(ctx context.Context, linkID uuid.UUID, bearer string, convID uuid.UUID) (Subscriber, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrWSPoolClosed
	}
	pc, ok := p.conns[linkID]
	p.mu.Unlock()

	if !ok {
		sub, err := p.dialer(ctx, p.cfg.BaseWS, bearer, p.cfg.SubscriberConfig)
		if err != nil {
			return nil, err
		}
		pc = &pooledConn{
			sub:        sub,
			lastUsedAt: p.clock(),
			convs:      make(map[uuid.UUID]bool),
		}
		p.mu.Lock()
		// Race: another goroutine could have raced us; if so, close ours
		// and use theirs. We always end up with exactly one active conn.
		if existing, ok := p.conns[linkID]; ok {
			p.mu.Unlock()
			_ = sub.Close()
			pc = existing
		} else if p.closed {
			p.mu.Unlock()
			_ = sub.Close()
			return nil, ErrWSPoolClosed
		} else {
			p.conns[linkID] = pc
			p.mu.Unlock()
		}
	}

	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.lastUsedAt = p.clock()
	if !pc.convs[convID] {
		if err := pc.sub.Subscribe("conversation", convID.String()); err != nil {
			return nil, err
		}
		pc.convs[convID] = true
	}
	return pc.sub, nil
}

// Touch refreshes the lastUsedAt timestamp without subscribing. Used by
// callers that are about to read from Events() so the sweeper doesn't
// reap a connection that's actively being consumed.
func (p *wsConnPool) Touch(linkID uuid.UUID) {
	p.mu.Lock()
	pc, ok := p.conns[linkID]
	p.mu.Unlock()
	if !ok {
		return
	}
	pc.mu.Lock()
	pc.lastUsedAt = p.clock()
	pc.mu.Unlock()
}

// Close releases the connection for linkID. Safe to call when no
// connection exists.
func (p *wsConnPool) Close(linkID uuid.UUID) {
	p.mu.Lock()
	pc, ok := p.conns[linkID]
	if ok {
		delete(p.conns, linkID)
	}
	p.mu.Unlock()
	if ok && pc.sub != nil {
		_ = pc.sub.Close()
	}
}

// Sweep closes every connection whose lastUsedAt is older than IdleTTL.
// Intended to be called by a periodic background ticker (e.g. every minute).
func (p *wsConnPool) Sweep() int {
	cutoff := p.clock().Add(-p.cfg.IdleTTL)
	var toClose []*pooledConn

	p.mu.Lock()
	for id, pc := range p.conns {
		pc.mu.Lock()
		stale := pc.lastUsedAt.Before(cutoff)
		pc.mu.Unlock()
		if stale {
			toClose = append(toClose, pc)
			delete(p.conns, id)
		}
	}
	p.mu.Unlock()

	for _, pc := range toClose {
		_ = pc.sub.Close()
	}
	return len(toClose)
}

// Stop closes every pooled connection and refuses future Get calls.
// Safe to call multiple times.
func (p *wsConnPool) Stop() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	all := make([]*pooledConn, 0, len(p.conns))
	for _, pc := range p.conns {
		all = append(all, pc)
	}
	p.conns = map[uuid.UUID]*pooledConn{}
	p.mu.Unlock()
	for _, pc := range all {
		_ = pc.sub.Close()
	}
}

// RunSweeper starts a periodic Sweep loop until ctx is cancelled. Tests
// drive Sweep() directly and skip this.
func (p *wsConnPool) RunSweeper(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Sweep()
		}
	}
}

// size reports the live connection count. Used by tests.
func (p *wsConnPool) size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}
