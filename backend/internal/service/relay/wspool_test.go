package relay

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"moses-chat-bot/backend/internal/mosesclient"
)

type stubSub struct {
	mu       sync.Mutex
	closed   bool
	subErrs  []error
	subbed   []string
	events   chan mosesclient.WSEvent
}

func newStubSub() *stubSub {
	return &stubSub{events: make(chan mosesclient.WSEvent, 4)}
}

func (s *stubSub) Subscribe(_, topicID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subbed = append(s.subbed, topicID)
	if len(s.subErrs) > 0 {
		err := s.subErrs[0]
		s.subErrs = s.subErrs[1:]
		return err
	}
	return nil
}

func (s *stubSub) Events() <-chan mosesclient.WSEvent { return s.events }
func (s *stubSub) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.events)
	}
	return nil
}

func TestWSPool_Get_ReusesExistingConnection(t *testing.T) {
	var dialCount atomic.Int32
	sub := newStubSub()
	pool := NewWSConnPool(WSPoolConfig{
		BaseWS: "http://x",
		Dialer: func(_ context.Context, _, _ string, _ mosesclient.WSConfig) (Subscriber, error) {
			dialCount.Add(1)
			return sub, nil
		},
	})

	linkID := uuid.New()
	convA := uuid.New()
	convB := uuid.New()
	ctx := context.Background()

	_, err := pool.Get(ctx, linkID, "bearer", convA)
	require.NoError(t, err)
	_, err = pool.Get(ctx, linkID, "bearer", convB)
	require.NoError(t, err)

	assert.Equal(t, int32(1), dialCount.Load(), "second Get reuses the conn")
	sub.mu.Lock()
	assert.Equal(t, []string{convA.String(), convB.String()}, sub.subbed)
	sub.mu.Unlock()
}

func TestWSPool_Get_DistinctLinksGetDistinctConns(t *testing.T) {
	var dialCount atomic.Int32
	pool := NewWSConnPool(WSPoolConfig{
		BaseWS: "http://x",
		Dialer: func(_ context.Context, _, _ string, _ mosesclient.WSConfig) (Subscriber, error) {
			dialCount.Add(1)
			return newStubSub(), nil
		},
	})
	linkA := uuid.New()
	linkB := uuid.New()
	conv := uuid.New()
	_, _ = pool.Get(context.Background(), linkA, "ba", conv)
	_, _ = pool.Get(context.Background(), linkB, "bb", conv)
	assert.Equal(t, int32(2), dialCount.Load())
}

func TestWSPool_Sweep_RemovesIdleConns(t *testing.T) {
	subs := []*stubSub{newStubSub(), newStubSub()}
	idx := 0
	now := time.Now()
	clock := &atomic.Int64{}
	clock.Store(now.UnixNano())

	pool := NewWSConnPool(WSPoolConfig{
		BaseWS:  "http://x",
		IdleTTL: 100 * time.Millisecond,
		Clock:   func() time.Time { return time.Unix(0, clock.Load()) },
		Dialer: func(_ context.Context, _, _ string, _ mosesclient.WSConfig) (Subscriber, error) {
			s := subs[idx]
			idx++
			return s, nil
		},
	})
	linkA := uuid.New()
	linkB := uuid.New()
	conv := uuid.New()

	_, err := pool.Get(context.Background(), linkA, "ba", conv)
	require.NoError(t, err)
	_, err = pool.Get(context.Background(), linkB, "bb", conv)
	require.NoError(t, err)
	require.Equal(t, 2, pool.size())

	// Advance clock past IdleTTL.
	clock.Store(now.Add(time.Second).UnixNano())
	n := pool.Sweep()
	assert.Equal(t, 2, n)
	assert.Equal(t, 0, pool.size())
	assert.True(t, subs[0].closed)
	assert.True(t, subs[1].closed)
}

func TestWSPool_Touch_KeepsAlive(t *testing.T) {
	sub := newStubSub()
	now := time.Now()
	clock := &atomic.Int64{}
	clock.Store(now.UnixNano())
	pool := NewWSConnPool(WSPoolConfig{
		BaseWS:  "http://x",
		IdleTTL: 100 * time.Millisecond,
		Clock:   func() time.Time { return time.Unix(0, clock.Load()) },
		Dialer:  func(_ context.Context, _, _ string, _ mosesclient.WSConfig) (Subscriber, error) { return sub, nil },
	})
	linkID := uuid.New()
	conv := uuid.New()
	_, _ = pool.Get(context.Background(), linkID, "b", conv)

	// Just before TTL would expire, touch — must reset lastUsedAt.
	clock.Store(now.Add(90 * time.Millisecond).UnixNano())
	pool.Touch(linkID)
	clock.Store(now.Add(150 * time.Millisecond).UnixNano()) // 60ms after touch — still fresh
	assert.Equal(t, 0, pool.Sweep(), "touch should have refreshed lastUsedAt")
	assert.Equal(t, 1, pool.size())
}

func TestWSPool_Stop_ClosesAll(t *testing.T) {
	subs := []*stubSub{newStubSub(), newStubSub()}
	idx := 0
	pool := NewWSConnPool(WSPoolConfig{
		BaseWS: "http://x",
		Dialer: func(_ context.Context, _, _ string, _ mosesclient.WSConfig) (Subscriber, error) {
			s := subs[idx]
			idx++
			return s, nil
		},
	})
	_, _ = pool.Get(context.Background(), uuid.New(), "b1", uuid.New())
	_, _ = pool.Get(context.Background(), uuid.New(), "b2", uuid.New())
	pool.Stop()
	assert.True(t, subs[0].closed)
	assert.True(t, subs[1].closed)

	// Subsequent Get returns ErrWSPoolClosed.
	_, err := pool.Get(context.Background(), uuid.New(), "b3", uuid.New())
	assert.ErrorIs(t, err, ErrWSPoolClosed)
}

func TestWSPool_Get_BearerPropagates(t *testing.T) {
	var got string
	pool := NewWSConnPool(WSPoolConfig{
		BaseWS: "http://x",
		Dialer: func(_ context.Context, _, token string, _ mosesclient.WSConfig) (Subscriber, error) {
			got = token
			return newStubSub(), nil
		},
	})
	_, _ = pool.Get(context.Background(), uuid.New(), "user-specific-bearer", uuid.New())
	assert.Equal(t, "user-specific-bearer", got)
}
