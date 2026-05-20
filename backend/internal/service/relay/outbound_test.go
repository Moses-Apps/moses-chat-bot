package relay

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/provider"
	"moses-chat-bot/backend/internal/provider/providertest"
)

// fakeStore is an in-memory db.Store substitute satisfying the relay.Store
// interface. It captures every InsertMessage call so tests can assert on the
// persisted audit row without touching Postgres.
type fakeStore struct {
	mu       sync.Mutex
	links    []db.ChatRelayLink
	listErr  error
	insertErr error
	inserts  []insertedRow
}

type insertedRow struct {
	ID                  uuid.UUID
	LinkID              uuid.UUID
	Direction           string
	ProviderMessageID   *string
	MosesConversationID *uuid.UUID
	Text                string
	Metadata            []byte
	Error               *string
}

func (s *fakeStore) ListActiveLinksByMosesUser(_ context.Context, _, _ uuid.UUID) ([]db.ChatRelayLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]db.ChatRelayLink, len(s.links))
	copy(out, s.links)
	return out, nil
}

func (s *fakeStore) InsertMessage(
	_ context.Context,
	linkID uuid.UUID,
	direction string,
	providerMessageID *string,
	mosesConversationID *uuid.UUID,
	text string,
	metadata []byte,
	errMsg *string,
) (uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.insertErr != nil {
		return uuid.Nil, s.insertErr
	}
	id := uuid.New()
	s.inserts = append(s.inserts, insertedRow{
		ID:                  id,
		LinkID:              linkID,
		Direction:           direction,
		ProviderMessageID:   providerMessageID,
		MosesConversationID: mosesConversationID,
		Text:                text,
		Metadata:            metadata,
		Error:               errMsg,
	})
	return id, nil
}

func (s *fakeStore) snapshot() []insertedRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]insertedRow, len(s.inserts))
	copy(out, s.inserts)
	return out
}

func newLink(t *testing.T, providerName, providerUserID string) db.ChatRelayLink {
	t.Helper()
	return db.ChatRelayLink{
		ID:             uuid.New(),
		MosesUserID:    uuid.New(),
		TenantID:       uuid.New(),
		Provider:       providerName,
		ProviderUserID: providerUserID,
		IsActive:       true,
	}
}

func registryWith(t *testing.T, providers ...provider.Provider) *provider.Registry {
	t.Helper()
	r := provider.NewRegistry()
	for _, p := range providers {
		require.NoError(t, r.Register(p))
	}
	return r
}

// ---------------------------------------------------------------------------
// SendToLink
// ---------------------------------------------------------------------------

func TestSendToLink_HappyPath(t *testing.T) {
	store := &fakeStore{}
	p := providertest.NewInMemoryProvider("telegram")
	sender := NewSender(store, registryWith(t, p), SenderOpts{})

	link := newLink(t, "telegram", "tg-1")
	conv := uuid.New()
	msg := provider.OutboundMessage{Text: "hello", Markdown: true}

	rowID, err := sender.SendToLink(context.Background(), &link, msg, &conv)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, rowID)

	rows := store.snapshot()
	require.Len(t, rows, 1)
	require.Equal(t, "out", rows[0].Direction)
	require.Equal(t, "hello", rows[0].Text)
	require.Nil(t, rows[0].Error)
	require.NotNil(t, rows[0].MosesConversationID)
	require.Equal(t, conv, *rows[0].MosesConversationID)

	sent := p.Snapshot()
	require.Len(t, sent, 1)
	require.Equal(t, "tg-1", sent[0].Chat.ProviderChatID)
	require.Equal(t, "hello", sent[0].Msg.Text)
}

func TestSendToLink_ProviderError_RowPersistsError(t *testing.T) {
	store := &fakeStore{}
	sendErr := errors.New("telegram down")
	p := providertest.NewInMemoryProvider("telegram")
	p.SendErr = sendErr
	sender := NewSender(store, registryWith(t, p), SenderOpts{})

	link := newLink(t, "telegram", "tg-2")
	_, err := sender.SendToLink(context.Background(), &link, provider.OutboundMessage{Text: "x"}, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, sendErr)

	rows := store.snapshot()
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].Error)
	require.Contains(t, *rows[0].Error, "telegram down")
}

func TestSendToLink_RateLimited_AfterNAllow(t *testing.T) {
	store := &fakeStore{}
	p := providertest.NewInMemoryProvider("telegram")

	// Pin the clock so refill cannot happen between calls.
	fixed := time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC)
	sender := NewSender(store, registryWith(t, p), SenderOpts{
		PerLinkPerMinute: 2,
		Clock:            func() time.Time { return fixed },
	})

	link := newLink(t, "telegram", "tg-3")
	for i := 0; i < 2; i++ {
		_, err := sender.SendToLink(context.Background(), &link, provider.OutboundMessage{Text: "ok"}, nil)
		require.NoError(t, err, "call %d should succeed", i)
	}

	_, err := sender.SendToLink(context.Background(), &link, provider.OutboundMessage{Text: "denied"}, nil)
	require.ErrorIs(t, err, ErrRateLimited)

	rows := store.snapshot()
	require.Len(t, rows, 3, "two successes + one rate-limited audit row")
	require.NotNil(t, rows[2].Error)
	require.Equal(t, "rate_limited", *rows[2].Error)
	require.Equal(t, "denied", rows[2].Text)

	// Provider must NOT have been called for the rate-limited message.
	sent := p.Snapshot()
	require.Len(t, sent, 2)
}

func TestSendToLink_UnknownProvider_RowPersistsError(t *testing.T) {
	store := &fakeStore{}
	sender := NewSender(store, provider.NewRegistry(), SenderOpts{})

	link := newLink(t, "discord", "discord-user-1")
	_, err := sender.SendToLink(context.Background(), &link, provider.OutboundMessage{Text: "x"}, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUnknownProvider)

	rows := store.snapshot()
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].Error)
	require.Equal(t, "unknown_provider", *rows[0].Error)
}

// ---------------------------------------------------------------------------
// SendToMosesUser
// ---------------------------------------------------------------------------

func TestSendToMosesUser_FanOutToAllActive(t *testing.T) {
	store := &fakeStore{}
	p := providertest.NewInMemoryProvider("telegram")
	tenantID := uuid.New()
	mosesUserID := uuid.New()
	store.links = []db.ChatRelayLink{
		{ID: uuid.New(), TenantID: tenantID, MosesUserID: mosesUserID, Provider: "telegram", ProviderUserID: "tg-a", IsActive: true},
		{ID: uuid.New(), TenantID: tenantID, MosesUserID: mosesUserID, Provider: "telegram", ProviderUserID: "tg-b", IsActive: true},
		{ID: uuid.New(), TenantID: tenantID, MosesUserID: mosesUserID, Provider: "telegram", ProviderUserID: "tg-c", IsActive: true},
	}
	sender := NewSender(store, registryWith(t, p), SenderOpts{})

	results, err := sender.SendToMosesUser(context.Background(), tenantID, mosesUserID, provider.OutboundMessage{Text: "broadcast"}, ProviderFilter{})
	require.NoError(t, err)
	require.Len(t, results, 3)
	for _, r := range results {
		require.True(t, r.Sent, "link %s should have sent", r.LinkID)
		require.Empty(t, r.Error)
		require.NotEqual(t, uuid.Nil, r.MessageRowID)
	}
	require.Len(t, p.Snapshot(), 3)
}

func TestSendToMosesUser_FilterByProvider(t *testing.T) {
	store := &fakeStore{}
	tg := providertest.NewInMemoryProvider("telegram")
	dc := providertest.NewInMemoryProvider("discord")
	tenantID := uuid.New()
	mosesUserID := uuid.New()
	store.links = []db.ChatRelayLink{
		{ID: uuid.New(), TenantID: tenantID, MosesUserID: mosesUserID, Provider: "telegram", ProviderUserID: "tg-1", IsActive: true},
		{ID: uuid.New(), TenantID: tenantID, MosesUserID: mosesUserID, Provider: "discord", ProviderUserID: "dc-1", IsActive: true},
	}
	sender := NewSender(store, registryWith(t, tg, dc), SenderOpts{})

	results, err := sender.SendToMosesUser(
		context.Background(),
		tenantID, mosesUserID,
		provider.OutboundMessage{Text: "tg only"},
		ProviderFilter{Providers: []string{"telegram"}},
	)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "telegram", results[0].Provider)
	require.Len(t, tg.Snapshot(), 1)
	require.Len(t, dc.Snapshot(), 0)
}

func TestSendToMosesUser_FilterByChatID(t *testing.T) {
	store := &fakeStore{}
	tg := providertest.NewInMemoryProvider("telegram")
	tenantID := uuid.New()
	mosesUserID := uuid.New()
	store.links = []db.ChatRelayLink{
		{ID: uuid.New(), TenantID: tenantID, MosesUserID: mosesUserID, Provider: "telegram", ProviderUserID: "tg-1", IsActive: true},
		{ID: uuid.New(), TenantID: tenantID, MosesUserID: mosesUserID, Provider: "telegram", ProviderUserID: "tg-2", IsActive: true},
	}
	sender := NewSender(store, registryWith(t, tg), SenderOpts{})

	results, err := sender.SendToMosesUser(
		context.Background(),
		tenantID, mosesUserID,
		provider.OutboundMessage{Text: "one"},
		ProviderFilter{ChatIDs: []string{"tg-2"}},
	)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "tg-2", results[0].ChatID)
}

func TestSendToMosesUser_PartialFailure_ReturnsAllResults(t *testing.T) {
	store := &fakeStore{}
	good := providertest.NewInMemoryProvider("telegram")
	bad := providertest.NewInMemoryProvider("discord")
	bad.SendErr = errors.New("discord 500")

	tenantID := uuid.New()
	mosesUserID := uuid.New()
	store.links = []db.ChatRelayLink{
		{ID: uuid.New(), TenantID: tenantID, MosesUserID: mosesUserID, Provider: "telegram", ProviderUserID: "tg-1", IsActive: true},
		{ID: uuid.New(), TenantID: tenantID, MosesUserID: mosesUserID, Provider: "discord", ProviderUserID: "dc-1", IsActive: true},
		{ID: uuid.New(), TenantID: tenantID, MosesUserID: mosesUserID, Provider: "telegram", ProviderUserID: "tg-2", IsActive: true},
	}
	sender := NewSender(store, registryWith(t, good, bad), SenderOpts{})

	results, err := sender.SendToMosesUser(context.Background(), tenantID, mosesUserID, provider.OutboundMessage{Text: "x"}, ProviderFilter{})
	require.NoError(t, err)
	require.Len(t, results, 3)

	var sentCount, failCount int
	for _, r := range results {
		if r.Sent {
			sentCount++
		} else {
			failCount++
			require.Equal(t, "discord", r.Provider)
			require.Contains(t, r.Error, "discord 500")
		}
	}
	require.Equal(t, 2, sentCount)
	require.Equal(t, 1, failCount)
}

func TestSendToMosesUser_ListErrorBubblesUp(t *testing.T) {
	store := &fakeStore{listErr: errors.New("db down")}
	sender := NewSender(store, provider.NewRegistry(), SenderOpts{})
	_, err := sender.SendToMosesUser(context.Background(), uuid.New(), uuid.New(), provider.OutboundMessage{}, ProviderFilter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "db down")
}

// ---------------------------------------------------------------------------
// Bucket
// ---------------------------------------------------------------------------

func TestBucket_RefillOverTime(t *testing.T) {
	var nowVal atomic.Int64
	start := time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC)
	nowVal.Store(start.UnixNano())
	clock := func() time.Time { return time.Unix(0, nowVal.Load()) }

	// Capacity 60 → 1 token/sec refill — easy arithmetic.
	b := NewBucket(60, clock)
	id := uuid.New()

	for i := 0; i < 60; i++ {
		require.True(t, b.Allow(id), "burst token %d", i)
	}
	require.False(t, b.Allow(id), "bucket should now be empty")

	// Advance 10s → 10 tokens regenerated.
	nowVal.Store(start.Add(10 * time.Second).UnixNano())
	for i := 0; i < 10; i++ {
		require.True(t, b.Allow(id), "refilled token %d", i)
	}
	require.False(t, b.Allow(id), "11th token after 10s refill must be denied")

	// Advance 1h → must clamp at capacity, not accumulate unboundedly.
	nowVal.Store(start.Add(time.Hour).UnixNano())
	for i := 0; i < 60; i++ {
		require.True(t, b.Allow(id), "post-clamp token %d", i)
	}
	require.False(t, b.Allow(id), "tokens must clamp at capacity even after long idle")
}

func TestBucket_ConcurrentAccess_NoRace(t *testing.T) {
	// High capacity so we don't need to count exactly — we only care about
	// the race detector. capacity = 10_000 over 1m = ~166/sec; the test
	// finishes well before refill matters.
	b := NewBucket(10000, time.Now)

	const workers = 32
	const perWorker = 200
	ids := make([]uuid.UUID, 8)
	for i := range ids {
		ids[i] = uuid.New()
	}

	var wg sync.WaitGroup
	var allowed atomic.Int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if b.Allow(ids[(seed+i)%len(ids)]) {
					allowed.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()
	// All requests must have been admitted under capacity.
	require.Equal(t, int64(workers*perWorker), allowed.Load())
}

func TestBucket_IdleCleanup_RemovesStale(t *testing.T) {
	var nowVal atomic.Int64
	start := time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC)
	nowVal.Store(start.UnixNano())
	clock := func() time.Time { return time.Unix(0, nowVal.Load()) }

	b := NewBucket(30, clock)

	// Touch 3 distinct buckets.
	for i := 0; i < 3; i++ {
		require.True(t, b.Allow(uuid.New()))
	}
	require.Equal(t, 3, b.size())

	// Advance past idleTTL (1h) and sweep.
	nowVal.Store(start.Add(2 * time.Hour).UnixNano())
	b.sweepIdle()
	require.Equal(t, 0, b.size(), "all idle buckets should be reaped")

	// New activity creates a fresh bucket; sweep at same moment must keep it.
	require.True(t, b.Allow(uuid.New()))
	b.sweepIdle()
	require.Equal(t, 1, b.size())
}
