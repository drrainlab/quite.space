package node

// THE DURABLE OFFER BOOK (LT-4 S5) — what this node has already put into
// which mailbox, and therefore what the next push may leave out.
//
// It replaces the in-memory book in offers.go, which cost this on every
// process restart: nothing remembered what any peer's mailbox held, so the
// first push re-offered each space's WHOLE history to every member at every
// endpoint — and a word said in that minute waited behind it. Measured on
// the owner's phone (1.0.26): "sent" for minutes on a fresh process.
//
// Three rules, each the answer to a way the book could lie:
//
//   THE CURSOR IS A LOG INDEX. The log's order is append-only; a count of
//   custody-bearing frames is not (one expires and the count shrinks), and
//   a shrinking count hid a newer frame under the mark.
//
//   THE CURSOR MOVES ONLY AFTER PutOK, per mailbox, and only when EVERY body
//   of that mailbox's group was accepted. A crash between PutOK and the
//   book's write repeats a copy (event-id dedup at the receiver); it never
//   skips one. The book is written debounced, always AFTER the acceptance.
//
//   A MARK EXPIRES A DAY AFTER THE LAST FULL OFFER, whatever was put since.
//   The relay keeps items in memory for 48 h and restarts forget them; a
//   receipt for my own chain proves nothing about other authors' frames in
//   that box. So once a day every mailbox is re-laid from the start — one
//   deduped re-offer, and the honest price of no signal from the relay.
//
// Keyed per (space, recipient, endpoint): the mailbox itself. A device
// guessed at three relays has three marks; a stated route and a guess at
// the same endpoint are the same box and share one.
//
// Sealed with the root key (peers' device ids and relay endpoints are a
// picture of who talks to whom — ADR-020 keeps the route book sealed for
// the same reason). The document's name carries no space prefix on
// purpose: forgetting a space deletes every sealed name that contains its
// prefix (forget.go), and this book is about every space.

import (
	"log"
	"sort"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
)

const (
	offerBookName = "offers"
	// offerBookMax bounds the document; the oldest-touched marks go first.
	// Eviction costs one re-offer, nothing else.
	offerBookMax = 4096
	// offerFlushAfter is the write debounce (the notification ledger's).
	offerFlushAfter = 2 * time.Second
	// offerFullReofferAfter is how long a full offer is trusted to still be
	// in the mailbox: half the relay TTL, counted from the last FULL offer.
	offerFullReofferAfter = 24 * time.Hour
)

type offerKey struct {
	space    id.TerminalID
	dev      id.DeviceID
	endpoint string
}

type offerMark struct {
	cursor int
	guess  bool
	legacy bool
	at     time.Time
	fullAt time.Time
}

type offerBook struct {
	mu      sync.Mutex
	marks   map[offerKey]offerMark
	dirty   bool
	timer   *time.Timer
	closed  bool
	save    func([]byte) error
	now     func() time.Time
	written int // documents written, for tests
}

func newOfferBook(save func([]byte) error) *offerBook {
	return &offerBook{marks: map[offerKey]offerMark{}, save: save, now: time.Now}
}

// load decodes a document into the book; a document that does not decode
// is an empty book and one log line. Marks for spaces this node no longer
// has are dropped.
func (b *offerBook) load(data []byte, known func(id.TerminalID) bool) {
	if len(data) == 0 {
		return
	}
	recs, err := storage.DecodeOfferBook(data)
	if err != nil {
		log.Printf("offer book: not read (%v) — starting empty, one full re-offer follows", err)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, r := range recs {
		if known != nil && !known(r.Space) {
			continue
		}
		b.marks[offerKey{r.Space, r.Device, r.Endpoint}] = offerMark{
			cursor: int(r.Cursor), guess: r.Guess, legacy: r.Legacy,
			at: time.Unix(r.At, 0), fullAt: time.Unix(r.FullAt, 0),
		}
	}
}

// cursor is the log index the next push to this mailbox may start from.
// n is the log's length now.
func (b *offerBook) cursor(k offerKey, n int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, ok := b.marks[k]
	if !ok {
		return 0
	}
	if b.now().Sub(m.fullAt) > offerFullReofferAfter {
		return 0 // the relay may have forgotten this box: lay it all again
	}
	if m.cursor > n {
		return 0 // the log is shorter than the mark: it was reset
	}
	return m.cursor
}

// mark records that frames with index < n are now in this mailbox — called
// only after every body of the push was accepted by the relay. from is
// where that push started: a push from 0 is a FULL offer.
func (b *offerBook) mark(k offerKey, from, n int, guess, legacy bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	prev, had := b.marks[k]
	full := prev.fullAt
	if from == 0 || !had {
		full = now
	}
	b.marks[k] = offerMark{cursor: n, guess: guess, legacy: legacy, at: now, fullAt: full}
	if len(b.marks) > offerBookMax {
		b.evictLocked()
	}
	b.dirtyLocked()
}

func (b *offerBook) forgetDevice(dev id.DeviceID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k := range b.marks {
		if k.dev == dev {
			delete(b.marks, k)
		}
	}
	b.dirtyLocked()
}

func (b *offerBook) forgetSpace(tid id.TerminalID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k := range b.marks {
		if k.space == tid {
			delete(b.marks, k)
		}
	}
	b.dirtyLocked()
}

func (b *offerBook) forgetAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.marks = map[offerKey]offerMark{}
	b.dirtyLocked()
}

// staleSpaces names the spaces with at least one mailbox whose last full
// offer is older than offerFullReofferAfter — the cycle re-enters those
// spaces so the daily re-offer actually happens (the per-space "nothing
// new" short-circuit would otherwise never consult the cursor).
func (b *offerBook) staleSpaces() map[id.TerminalID]struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	var out map[id.TerminalID]struct{}
	for k, m := range b.marks {
		if now.Sub(m.fullAt) > offerFullReofferAfter {
			if out == nil {
				out = map[id.TerminalID]struct{}{}
			}
			out[k.space] = struct{}{}
		}
	}
	return out
}

// setFullAt is a test seam: age a mailbox's last full offer.
func (b *offerBook) setFullAt(k offerKey, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if m, ok := b.marks[k]; ok {
		m.fullAt = at
		b.marks[k] = m
	}
}

func (b *offerBook) evictLocked() {
	type kv struct {
		k offerKey
		m offerMark
	}
	all := make([]kv, 0, len(b.marks))
	for k, m := range b.marks {
		all = append(all, kv{k, m})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].m.at.Before(all[j].m.at) })
	for _, e := range all[:len(all)-offerBookMax] {
		delete(b.marks, e.k)
	}
}

func (b *offerBook) dirtyLocked() {
	b.dirty = true
	if b.closed || b.save == nil {
		return
	}
	if b.timer == nil {
		b.timer = time.AfterFunc(offerFlushAfter, b.flush)
	}
}

// flush writes the book now if it changed. Safe to call any time; called
// from Close before the runtime stops.
func (b *offerBook) flush() {
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	if !b.dirty || b.save == nil {
		b.mu.Unlock()
		return
	}
	b.dirty = false
	data := b.encodeLocked()
	b.written++
	b.mu.Unlock()
	if err := b.save(data); err != nil {
		log.Printf("offer book: not written: %v", err)
	}
}

// close flushes and stops the timer for good.
func (b *offerBook) close() {
	b.flush()
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
}

func (b *offerBook) encodeLocked() []byte {
	recs := make([]storage.OfferRecord, 0, len(b.marks))
	for k, m := range b.marks {
		recs = append(recs, storage.OfferRecord{
			Space: k.space, Device: k.dev, Endpoint: k.endpoint,
			Cursor: uint64(m.cursor), Guess: m.guess, Legacy: m.legacy,
			At: m.at.Unix(), FullAt: m.fullAt.Unix(),
		})
	}
	// Deterministic order, so two identical books are identical bytes.
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].Space != recs[j].Space {
			return recs[i].Space.Hex() < recs[j].Space.Hex()
		}
		if recs[i].Device != recs[j].Device {
			return string(recs[i].Device[:]) < string(recs[j].Device[:])
		}
		return recs[i].Endpoint < recs[j].Endpoint
	})
	return storage.AppendOfferBook(nil, recs)
}

// size is the number of marks, for tests and diagnostics.
func (b *offerBook) size() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.marks)
}
