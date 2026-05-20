package linker

import (
	"sync"
	"time"
)

// lockoutTable is the in-memory brute-force defense for /link.
//
// Contract: per (provider, provider_user_id), if there are ≥
// lockoutThreshold failed attempts within lockoutWindow, the user is
// locked out for lockoutDuration. A successful link clears the bucket.
//
// In-memory only (v1). BOT_LOCKOUT_PERSIST=true is reserved for v1.1:
// the operational complexity of a DB-backed lockout (cleanup, race
// windows on multi-replica deploys) outweighs the value at our scale.
// The Telegram code space is 24 bits — at 3 strikes / 15 min the
// theoretical guess rate is ~12/hour per attacker, far below what
// would brute force a 6-hex space in any realistic timeframe.
type lockoutTable struct{ m sync.Map } // key string → *bucket

type bucket struct {
	mu             sync.Mutex
	count          int
	firstFailureAt time.Time
	lockedUntil    time.Time
}

func newLockoutTable() *lockoutTable { return &lockoutTable{} }

func (l *lockoutTable) getOrCreate(provider, providerUserID string) *bucket {
	key := keyOf(provider, providerUserID)
	if v, ok := l.m.Load(key); ok {
		return v.(*bucket)
	}
	created := &bucket{}
	actual, _ := l.m.LoadOrStore(key, created)
	return actual.(*bucket)
}

// isLocked reports whether the user is currently locked out at `now`.
// Reading isLocked has the side effect of expiring stale lockouts — if
// the lockedUntil window has passed we leave the bucket in place but
// it stops gating new attempts.
func (l *lockoutTable) isLocked(provider, providerUserID string, now time.Time) bool {
	b := l.getOrCreate(provider, providerUserID)
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.lockedUntil.IsZero() && now.Before(b.lockedUntil)
}

// recordFailure increments the failure counter and trips a lockout when
// the threshold is reached inside the sliding window. Calling
// recordFailure on an already-locked bucket is a no-op (the lockout
// duration is not extended on each attempt — otherwise an attacker
// could keep the legitimate user locked out forever).
func (l *lockoutTable) recordFailure(provider, providerUserID string, now time.Time) {
	b := l.getOrCreate(provider, providerUserID)
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.lockedUntil.IsZero() && now.Before(b.lockedUntil) {
		return
	}

	if b.count == 0 || now.Sub(b.firstFailureAt) > lockoutWindow {
		b.count = 1
		b.firstFailureAt = now
		b.lockedUntil = time.Time{}
		return
	}

	b.count++
	if b.count >= lockoutThreshold {
		b.lockedUntil = now.Add(lockoutDuration)
	}
}

// clear resets the bucket on successful link.
func (l *lockoutTable) clear(provider, providerUserID string) {
	l.m.Delete(keyOf(provider, providerUserID))
}
