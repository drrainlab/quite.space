// Package relay is the T3 blind relay store (plan §19, §5.8): it holds
// opaque items addressed by rotating destination hints and deletes them at
// TTL, unconditionally. It has no decoder — this package does not even
// import the envelope types, which is the point.
//
// Honesty note (M0): envelope payloads are not yet encrypted (ADR-005 lands
// in M1), so blindness is currently architectural (this code cannot parse
// items) rather than cryptographic. Nothing here claims otherwise.
package relayserver

import (
	"bytes"
	"sort"
	"sync"
)

// Item is what a relay holds: a hint, an expiry, bytes. Nothing else.
type Item struct {
	DestinationHint string
	ExpiresAt       uint64
	Ciphertext      []byte
}

// Store is a blind mailbox with per-hint and global quotas. Safe for
// concurrent use (the networked relay serves many peers).
//
// RR-7: quotas are BOTH item-counted and byte-counted, and both per-hint
// and global — before this the only memory bound was an item count, whose
// theoretical ceiling (65 536 × 16 MiB) was a terabyte of RSS, and one
// door/outbox/reply-box could crowd out the whole relay.
type Store struct {
	mu          sync.Mutex
	maxPerHint  int
	maxItemSize int
	maxTotal    int
	total       int
	// Byte budgets (0 = derived defaults in NewStore).
	maxPerHintBytes int
	maxTotalBytes   int64
	totalBytes      int64
	perHintBytes    map[string]int
	items           map[string][]Item
}

// NewStore creates a relay with abuse limits (plan §26: storage exhaustion).
// maxTotal bounds items across all hints; 0 means maxPerHint*1024.
func NewStore(maxPerHint, maxItemSize int) *Store {
	return &Store{maxPerHint: maxPerHint, maxItemSize: maxItemSize,
		maxTotal: maxPerHint * 1024,
		// Defaults: one hint may hold at most 32× the item cap; the whole
		// store at most 2 GiB. Overridable via SetByteBudgets.
		maxPerHintBytes: 32 * maxItemSize,
		maxTotalBytes:   2 << 30,
		perHintBytes:    map[string]int{},
		items:           map[string][]Item{}}
}

// SetByteBudgets overrides the byte quotas (operator configuration).
func (s *Store) SetByteBudgets(perHint int, total int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if perHint > 0 {
		s.maxPerHintBytes = perHint
	}
	if total > 0 {
		s.maxTotalBytes = total
	}
}

func (s *Store) addLocked(hint string, n int) {
	s.perHintBytes[hint] += n
	s.totalBytes += int64(n)
	if s.perHintBytes[hint] <= 0 {
		delete(s.perHintBytes, hint)
	}
	if s.totalBytes < 0 {
		s.totalBytes = 0
	}
}

// Put accepts an item if quota allows. The relay never inspects the bytes.
// Idempotent by content within a hint (PA-0): re-putting byte-identical
// ciphertext does not consume another slot — a contributor retrying its
// cumulative pending bundle while the consumer is offline cannot jam the
// mailbox. After a Collect removes it, the same bytes may be inserted again.
func (s *Store) Put(it Item) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(it.Ciphertext)
	if n == 0 || n > s.maxItemSize {
		return false
	}
	for _, held := range s.items[it.DestinationHint] {
		if bytes.Equal(held.Ciphertext, it.Ciphertext) {
			return true // identical retry: one slot, already accepted
		}
	}
	if len(s.items[it.DestinationHint]) >= s.maxPerHint || s.total >= s.maxTotal {
		return false
	}
	if s.perHintBytes[it.DestinationHint]+n > s.maxPerHintBytes ||
		s.totalBytes+int64(n) > s.maxTotalBytes {
		return false
	}
	s.items[it.DestinationHint] = append(s.items[it.DestinationHint], it)
	s.total++
	s.addLocked(it.DestinationHint, n)
	return true
}

// Replace ATOMICALLY swaps everything under the item's hint with this one
// item (I5): validation happens before any mutation, so a refused Replace
// leaves the previous mailbox untouched, and no reader can observe a
// half-replaced state. This is the projection-mailbox verb — the hint holds
// exactly the latest projection.
func (s *Store) Replace(it Item) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(it.Ciphertext)
	if n == 0 || n > s.maxItemSize || n > s.maxPerHintBytes {
		return false
	}
	oldBytes := s.perHintBytes[it.DestinationHint]
	if old := len(s.items[it.DestinationHint]); old == 0 && s.total >= s.maxTotal {
		return false
	}
	if s.totalBytes-int64(oldBytes)+int64(n) > s.maxTotalBytes {
		return false
	}
	s.total -= len(s.items[it.DestinationHint])
	s.addLocked(it.DestinationHint, -oldBytes)
	s.items[it.DestinationHint] = []Item{it}
	s.total++
	s.addLocked(it.DestinationHint, n)
	return true
}

// Fetch returns copies of a hint's live items WITHOUT removing them —
// the many-reader verb for public mailboxes. Newest first, bounded by
// maxBytes across the returned set (0 = unbounded).
func (s *Store) Fetch(hint string, now uint64, maxBytes int) [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.items[hint]
	var out [][]byte
	total := 0
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.ExpiresAt != 0 && now >= it.ExpiresAt {
			continue
		}
		if maxBytes > 0 && total+len(it.Ciphertext) > maxBytes {
			break
		}
		out = append(out, append([]byte(nil), it.Ciphertext...))
		total += len(it.Ciphertext)
	}
	return out
}

// Collect hands over everything for a hint and forgets it (store-and-forward).
func (s *Store) Collect(hint string, now uint64) [][]byte {
	return s.CollectBudget(hint, now, 0)
}

// CollectBudget is Collect bounded by maxBytes (0 = unbounded). What does
// not fit STAYS — a drain that cannot carry everything must not destroy the
// remainder, or a byte cap would become a deletion tool. Oldest first, so a
// bounded drain still makes forward progress across calls.
func (s *Store) CollectBudget(hint string, now uint64, maxBytes int) [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.items[hint]
	var out [][]byte
	var kept []Item
	total := 0
	removed := 0
	for _, it := range items {
		if it.ExpiresAt != 0 && now >= it.ExpiresAt {
			removed += len(it.Ciphertext)
			continue // expired in place; never delivered, never kept
		}
		if maxBytes > 0 && total+len(it.Ciphertext) > maxBytes && len(out) > 0 {
			kept = append(kept, it)
			continue
		}
		out = append(out, it.Ciphertext)
		total += len(it.Ciphertext)
		removed += len(it.Ciphertext)
	}
	if len(kept) == 0 {
		delete(s.items, hint)
	} else {
		s.items[hint] = kept
	}
	s.total -= len(items) - len(kept)
	s.addLocked(hint, -removed)
	return out
}

// Expire drops everything past TTL. Deletion at expiry is unconditional
// (ADR-010).
func (s *Store) Expire(now uint64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	dropped := 0
	for hint, items := range s.items {
		var kept []Item
		removed := 0
		for _, it := range items {
			if it.ExpiresAt != 0 && now >= it.ExpiresAt {
				dropped++
				removed += len(it.Ciphertext)
				continue
			}
			kept = append(kept, it)
		}
		if len(kept) == 0 {
			delete(s.items, hint)
		} else {
			s.items[hint] = kept
		}
		s.addLocked(hint, -removed)
	}
	s.total -= dropped
	return dropped
}

// PendingBytes reports held ciphertext bytes (diagnostics/metrics).
func (s *Store) PendingBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totalBytes
}

// Hints lists hints with pending items (diagnostics only), sorted.
func (s *Store) Hints() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.items))
	for h := range s.items {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// Pending returns the total item count (diagnostics).
func (s *Store) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

// FillRatio reports how full the store is against its item quota (RR-3
// load-class input). Byte-based accounting arrives with RR-7.
func (s *Store) FillRatio() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.maxTotal <= 0 {
		return 0
	}
	return float64(s.total) / float64(s.maxTotal)
}
