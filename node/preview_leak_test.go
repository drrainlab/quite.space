package node

import (
	"testing"
	"time"
)

// A session that leaves the table must take its fetcher with it.
//
// sessionFetcher.close says in its own doc comment that it is called from
// ALL the death paths — "explicit close, TTL expiry, cap eviction, and
// runtime shutdown" — and three of those four were not true: get's TTL
// branch, put's TTL sweep and put's cap eviction all deleted the map entry
// and left the goroutine running with its share of previewGlobalBudget held
// until the process exited.
//
// It mattered little while a session meant "somebody is reading one
// forwarded post". It matters now: browsing a directory opens a session per
// level, so eviction at previewCap becomes the ordinary path rather than
// the rare one.

// leakySession builds a session whose fetcher holds accounted bytes, so a
// test can watch both the goroutine flag and the budget.
func leakySession(t *testing.T, id string, born time.Time, bytes int) *previewSession {
	t.Helper()
	f := &sessionFetcher{
		store: newSessionStore(nil),
		graph: map[string]sessionAsset{},
		jobs:  map[string]*fetchJob{},
		stop:  make(chan struct{}),
		wake:  make(chan struct{}, 1),
	}
	if bytes > 0 {
		if !budgetAdmit(bytes) {
			t.Fatalf("budget refused %d bytes at the start of a test", bytes)
		}
		if _, err := f.store.PutBlob(make([]byte, bytes)); err != nil {
			t.Fatalf("PutBlob: %v", err)
		}
	}
	return &previewSession{id: id, born: born, fetcher: f}
}

func budgetSpent() int64 {
	previewBudget.mu.Lock()
	defer previewBudget.mu.Unlock()
	return previewBudget.spent
}

func TestAnEvictedSessionDoesNotLeakItsFetcher(t *testing.T) {
	base := budgetSpent()
	var ps previewStore
	now := time.Now()

	// The oldest is the one eviction takes.
	oldest := leakySession(t, "oldest", now.Add(-time.Minute), 4096)
	ps.put(oldest)
	for i := range previewCap {
		ps.put(leakySession(t, string(rune('a'+i)), now, 0))
	}

	if ps.get("oldest") != nil {
		t.Fatal("the oldest session survived the cap")
	}
	if !oldest.fetcher.stopped {
		t.Fatal("an evicted session left its fetcher running")
	}
	if got := budgetSpent(); got != base {
		t.Fatalf("an evicted session kept %d bytes of the shared budget", got-base)
	}
}

func TestAnExpiredSessionDoesNotLeakItsFetcher(t *testing.T) {
	base := budgetSpent()
	var ps previewStore

	s := leakySession(t, "old", time.Now().Add(-previewTTL-time.Second), 4096)
	ps.put(s)

	if ps.get("old") != nil {
		t.Fatal("an expired session was served")
	}
	if !s.fetcher.stopped {
		t.Fatal("an expired session left its fetcher running")
	}
	if got := budgetSpent(); got != base {
		t.Fatalf("an expired session kept %d bytes of the shared budget", got-base)
	}
}

// put sweeps expired sessions on its way in — a different code path from
// get, and it was the same omission.
func TestTheSweepOnInsertDoesNotLeakAFetcher(t *testing.T) {
	base := budgetSpent()
	var ps previewStore

	stale := leakySession(t, "stale", time.Now().Add(-previewTTL-time.Second), 4096)
	ps.put(stale)
	ps.put(leakySession(t, "fresh", time.Now(), 0))

	if !stale.fetcher.stopped {
		t.Fatal("the sweep on insert left a fetcher running")
	}
	if got := budgetSpent(); got != base {
		t.Fatalf("the sweep on insert kept %d bytes of the shared budget", got-base)
	}
}
