// Package relay is the T3 blind relay store (plan §19, §5.8): it holds
// opaque items addressed by rotating destination hints and deletes them at
// TTL, unconditionally. It has no decoder — this package does not even
// import the envelope types, which is the point.
//
// Honesty note (M0): envelope payloads are not yet encrypted (ADR-005 lands
// in M1), so blindness is currently architectural (this code cannot parse
// items) rather than cryptographic. Nothing here claims otherwise.
package relay

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
type Store struct {
	mu          sync.Mutex
	maxPerHint  int
	maxItemSize int
	maxTotal    int
	total       int
	items       map[string][]Item
}

// NewStore creates a relay with abuse limits (plan §26: storage exhaustion).
// maxTotal bounds items across all hints; 0 means maxPerHint*1024.
func NewStore(maxPerHint, maxItemSize int) *Store {
	return &Store{maxPerHint: maxPerHint, maxItemSize: maxItemSize,
		maxTotal: maxPerHint * 1024, items: map[string][]Item{}}
}

// Put accepts an item if quota allows. The relay never inspects the bytes.
// Idempotent by content within a hint (PA-0): re-putting byte-identical
// ciphertext does not consume another slot — a contributor retrying its
// cumulative pending bundle while the consumer is offline cannot jam the
// mailbox. After a Collect removes it, the same bytes may be inserted again.
func (s *Store) Put(it Item) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(it.Ciphertext) == 0 || len(it.Ciphertext) > s.maxItemSize {
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
	s.items[it.DestinationHint] = append(s.items[it.DestinationHint], it)
	s.total++
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
	if len(it.Ciphertext) == 0 || len(it.Ciphertext) > s.maxItemSize {
		return false
	}
	if old := len(s.items[it.DestinationHint]); old == 0 && s.total >= s.maxTotal {
		return false
	}
	s.total -= len(s.items[it.DestinationHint])
	s.items[it.DestinationHint] = []Item{it}
	s.total++
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
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.items[hint]
	delete(s.items, hint)
	s.total -= len(items)
	var out [][]byte
	for _, it := range items {
		if it.ExpiresAt != 0 && now >= it.ExpiresAt {
			continue // expired in place; never delivered
		}
		out = append(out, it.Ciphertext)
	}
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
		for _, it := range items {
			if it.ExpiresAt != 0 && now >= it.ExpiresAt {
				dropped++
				continue
			}
			kept = append(kept, it)
		}
		if len(kept) == 0 {
			delete(s.items, hint)
		} else {
			s.items[hint] = kept
		}
	}
	s.total -= dropped
	return dropped
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
