package relay

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Bucket is a per-link token-bucket rate limiter. Each link.id gets its own
// bucket lazily on first Allow call. Buckets refill continuously at
// capacity-tokens-per-minute (i.e. capacity/60 tokens/second), so the long-run
// average rate is capped while short bursts up to capacity are permitted.
//
// Idle buckets are reaped by a background goroutine started via Run; tests
// that don't need cleanup can skip Run.
type Bucket struct {
	capacity     float64
	refillPerSec float64
	clock        func() time.Time
	buckets      sync.Map // map[uuid.UUID]*linkBucket
	idleTTL      time.Duration
	sweepEvery   time.Duration
}

type linkBucket struct {
	mu         sync.Mutex
	tokens     float64
	lastRefill time.Time
}

// NewBucket builds a token-bucket allowing perMinute messages per link.id
// over rolling time. clock is overridable for deterministic tests.
func NewBucket(perMinute int, clock func() time.Time) *Bucket {
	if perMinute <= 0 {
		perMinute = 30
	}
	if clock == nil {
		clock = time.Now
	}
	return &Bucket{
		capacity:     float64(perMinute),
		refillPerSec: float64(perMinute) / 60.0,
		clock:        clock,
		idleTTL:      time.Hour,
		sweepEvery:   5 * time.Minute,
	}
}

// Allow atomically refills then attempts to consume one token for linkID.
// Returns true if a token was consumed; false if the bucket is empty.
//
// Bucket math (the non-obvious part): we lazy-refill at use time using
// elapsed = now - lastRefill, adding elapsed * refillPerSec tokens (capped at
// capacity). This avoids a global ticker goroutine; cost is O(1) per Allow.
func (b *Bucket) Allow(linkID uuid.UUID) bool {
	if linkID == uuid.Nil {
		return false
	}
	now := b.clock()

	v, _ := b.buckets.LoadOrStore(linkID, &linkBucket{
		tokens:     b.capacity,
		lastRefill: now,
	})
	lb := v.(*linkBucket)

	lb.mu.Lock()
	defer lb.mu.Unlock()

	elapsed := now.Sub(lb.lastRefill).Seconds()
	if elapsed > 0 {
		lb.tokens += elapsed * b.refillPerSec
		if lb.tokens > b.capacity {
			lb.tokens = b.capacity
		}
		lb.lastRefill = now
	}

	if lb.tokens < 1.0 {
		return false
	}
	lb.tokens -= 1.0
	return true
}

// Run starts the idle-bucket sweeper. It returns when ctx is cancelled.
// Safe to call at most once per Bucket; multiple concurrent sweepers would
// just duplicate work, not corrupt state.
func (b *Bucket) Run(ctx context.Context) {
	ticker := time.NewTicker(b.sweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.sweepIdle()
		}
	}
}

// sweepIdle removes buckets whose lastRefill is older than idleTTL. Exposed
// (lowercase, package-internal) so tests can drive it deterministically
// without standing up the ticker goroutine.
func (b *Bucket) sweepIdle() {
	cutoff := b.clock().Add(-b.idleTTL)
	b.buckets.Range(func(key, value any) bool {
		lb := value.(*linkBucket)
		lb.mu.Lock()
		stale := lb.lastRefill.Before(cutoff)
		lb.mu.Unlock()
		if stale {
			b.buckets.Delete(key)
		}
		return true
	})
}

// size reports the number of live buckets. Used by tests to assert sweeping.
func (b *Bucket) size() int {
	n := 0
	b.buckets.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}
