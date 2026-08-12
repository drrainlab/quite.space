// The connector ingress journal (TR-0c). One append-only file per
// connector, in the delivery ledger's exact discipline: length‖crc32c
// records, torn-tail truncation on open, fsync before anything is claimed,
// mutate-under-lock with rollback when the append fails.
//
// WHAT IS IN PLAINTEXT HERE IS DELIBERATE AND SMALL: hashes, states,
// binding generations and timestamps. Never a sender address, never a
// subject, never a Message-ID in the clear — the readable material lives in
// the sealed blob store, and this file carries only the hashes that name
// it. The notification ledger's rule (node/notifyledger.go) applies
// verbatim: a person's correspondence must not rest in cleartext beside a
// log that is sealed.
//
// THE BINDING IS A TEMPORAL BOUNDARY (plan rev 3). A route change closes
// generation N and opens N+1; every ingress record belongs to the
// generation that was active when it was observed, retry is only ever
// toward that generation's target, and closing a generation orphans its
// unfinished records — visibly, not silently. Nothing ever crosses.
package node

import (
	"encoding/binary"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// ConnState is one ingress record's place in the projection state machine.
type ConnState uint8

const (
	// ConnReceived: durably journaled, not yet projected anywhere.
	ConnReceived ConnState = 1
	// ConnEmitting: the projector is about to emit (or crashed mid-emit —
	// recovery reconciles against the space's own log, never re-emits
	// blind).
	ConnEmitting ConnState = 2
	// ConnPublished: exactly one space event exists for this ingress.
	ConnPublished ConnState = 3
	// ConnRefused: a terminal outcome with a name (policy, oversize, …) —
	// the ADR-012 rule: a settled refusal stays settled and reproducible.
	ConnRefused ConnState = 4
	// ConnOrphaned: its binding closed before projection finished. Terminal
	// by doctrine; the original stays reachable in the external system.
	ConnOrphaned ConnState = 5
)

// IngressRecord is one observed external message.
type IngressRecord struct {
	// Key = SHA256(connectorID ‖ 0x00 ‖ stable transport id). The dedup
	// identity, scoped per binding: (Binding, Key) is the journal key.
	Key [32]byte
	// Binding is the generation that was active at ingress.
	Binding uint64
	State   ConnState
	// Outcome names a terminal refusal ("oversize", "no_text", …).
	Outcome string
	// EventID is the ONE event this projected to (set at Published).
	EventID id.EventID
	// BodyBlob names the sealed envelope bytes in the blob store.
	BodyBlob id.Hash
	// RefHash/ThreadHash are SHA256 of the external provenance/thread refs
	// — enough to resolve reply edges without readable metadata on disk.
	RefHash    [32]byte
	ThreadHash [32]byte
	ReceivedAt int64
	UpdatedAt  int64
}

type connBindKey struct {
	gen uint64
	key [32]byte
}

// connJournal is the per-connector durable state: ingress records plus the
// binding history, one file.
type connJournal struct {
	mu   sync.Mutex
	path string
	f    *os.File
	live map[connBindKey]*IngressRecord
	// binding state, replayed: generation counter and the open target.
	gen       uint64
	target    id.TerminalID
	hasTarget bool
	dead      int
}

const (
	cjKeyKind       = 1
	cjKeyRecKey     = 2
	cjKeyBinding    = 3
	cjKeyState      = 4
	cjKeyOutcome    = 5
	cjKeyEvent      = 6
	cjKeyBlob       = 7
	cjKeyRefHash    = 8
	cjKeyThreadHash = 9
	cjKeyReceived   = 10
	cjKeyUpdated    = 11
	cjKeyTarget     = 12

	cjRecIngress = 1
	cjRecBinding = 2
)

var connCrc = crc32.MakeTable(crc32.Castagnoli)

func openConnJournal(dir string) (*connJournal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	j := &connJournal{
		path: filepath.Join(dir, "ingress.journal"),
		live: map[connBindKey]*IngressRecord{},
	}
	if err := j.replay(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	j.f = f
	return j, nil
}

func (j *connJournal) replay() error {
	f, err := os.Open(j.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	valid := int64(0)
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(f, hdr[:]); err != nil {
			break
		}
		n := binary.BigEndian.Uint32(hdr[:4])
		want := binary.BigEndian.Uint32(hdr[4:])
		if n == 0 || n > 1<<20 {
			break
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(f, body); err != nil {
			break
		}
		if crc32.Checksum(body, connCrc) != want {
			break // torn record: everything after it is suspect
		}
		valid += 8 + int64(n)
		j.apply(body)
	}
	return os.Truncate(j.path, valid)
}

func (j *connJournal) apply(body []byte) {
	d := codec.NewDecoder(body)
	m, err := d.ReadMapHeader()
	if err != nil {
		return
	}
	var kind, binding, state, received, updated uint64
	var outcome string
	var key, refHash, threadHash [32]byte
	var eid id.EventID
	var blob id.Hash
	var target id.TerminalID
	read32 := func(dst []byte) error {
		b, er := d.ReadBytes()
		if er != nil {
			return er
		}
		if len(b) == len(dst) {
			copy(dst, b)
		}
		return nil
	}
	for {
		k, ok, er := m.Next()
		if er != nil || !ok {
			break
		}
		switch k {
		case cjKeyKind:
			kind, er = d.ReadUint()
		case cjKeyRecKey:
			er = read32(key[:])
		case cjKeyBinding:
			binding, er = d.ReadUint()
		case cjKeyState:
			state, er = d.ReadUint()
		case cjKeyOutcome:
			outcome, er = d.ReadText()
		case cjKeyEvent:
			er = read32(eid[:])
		case cjKeyBlob:
			er = read32(blob[:])
		case cjKeyRefHash:
			er = read32(refHash[:])
		case cjKeyThreadHash:
			er = read32(threadHash[:])
		case cjKeyReceived:
			received, er = d.ReadUint()
		case cjKeyUpdated:
			updated, er = d.ReadUint()
		case cjKeyTarget:
			er = read32(target[:])
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return
		}
	}
	switch kind {
	case cjRecBinding:
		if binding > j.gen {
			j.gen = binding
			j.target = target
			j.hasTarget = target != (id.TerminalID{})
		}
	case cjRecIngress:
		rec := &IngressRecord{
			Key: key, Binding: binding, State: ConnState(state),
			Outcome: outcome, EventID: eid, BodyBlob: blob,
			RefHash: refHash, ThreadHash: threadHash,
			ReceivedAt: int64(received), UpdatedAt: int64(updated),
		}
		bk := connBindKey{gen: binding, key: key}
		if _, existed := j.live[bk]; existed {
			j.dead++
		}
		j.live[bk] = rec
	}
}

func encodeIngress(rec *IngressRecord) []byte {
	buf := codec.AppendMap(nil, 11)
	buf = codec.AppendUint(buf, cjKeyKind)
	buf = codec.AppendUint(buf, cjRecIngress)
	buf = codec.AppendUint(buf, cjKeyRecKey)
	buf = codec.AppendBytes(buf, rec.Key[:])
	buf = codec.AppendUint(buf, cjKeyBinding)
	buf = codec.AppendUint(buf, rec.Binding)
	buf = codec.AppendUint(buf, cjKeyState)
	buf = codec.AppendUint(buf, uint64(rec.State))
	buf = codec.AppendUint(buf, cjKeyOutcome)
	buf = codec.AppendText(buf, rec.Outcome)
	buf = codec.AppendUint(buf, cjKeyEvent)
	buf = codec.AppendBytes(buf, rec.EventID[:])
	buf = codec.AppendUint(buf, cjKeyBlob)
	buf = codec.AppendBytes(buf, rec.BodyBlob[:])
	buf = codec.AppendUint(buf, cjKeyRefHash)
	buf = codec.AppendBytes(buf, rec.RefHash[:])
	buf = codec.AppendUint(buf, cjKeyThreadHash)
	buf = codec.AppendBytes(buf, rec.ThreadHash[:])
	buf = codec.AppendUint(buf, cjKeyReceived)
	buf = codec.AppendUint(buf, uint64(max(rec.ReceivedAt, 0)))
	buf = codec.AppendUint(buf, cjKeyUpdated)
	buf = codec.AppendUint(buf, uint64(max(rec.UpdatedAt, 0)))
	return buf
}

func (j *connJournal) appendRecord(body []byte) error {
	var buf []byte
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(body)))
	buf = binary.BigEndian.AppendUint32(buf, crc32.Checksum(body, connCrc))
	buf = append(buf, body...)
	if _, err := j.f.Write(buf); err != nil {
		return err
	}
	return j.f.Sync()
}

// Binding reports the open route, if any.
func (j *connJournal) Binding() (gen uint64, target id.TerminalID, ok bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.gen, j.target, j.hasTarget
}

// OpenBinding closes the current generation and opens the next toward
// target. Closing ORPHANS every unfinished record of every earlier
// generation — written down one by one, because "visibly" means on disk.
func (j *connJournal) OpenBinding(target id.TerminalID, now int64) (uint64, int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	next := j.gen + 1
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, cjKeyKind)
	buf = codec.AppendUint(buf, cjRecBinding)
	buf = codec.AppendUint(buf, cjKeyBinding)
	buf = codec.AppendUint(buf, next)
	buf = codec.AppendUint(buf, cjKeyTarget)
	buf = codec.AppendBytes(buf, target[:])
	if err := j.appendRecord(buf); err != nil {
		return 0, 0, err
	}
	j.gen = next
	j.target = target
	j.hasTarget = target != (id.TerminalID{})
	orphaned := 0
	for bk, rec := range j.live {
		if bk.gen >= next {
			continue
		}
		if rec.State != ConnReceived && rec.State != ConnEmitting {
			continue
		}
		before := *rec
		rec.State = ConnOrphaned
		rec.Outcome = "closed_binding"
		rec.UpdatedAt = now
		if err := j.appendRecord(encodeIngress(rec)); err != nil {
			*rec = before
			return next, orphaned, err
		}
		j.dead++
		orphaned++
	}
	return next, orphaned, nil
}

// Ingest journals one observed message under the CURRENT binding.
// Idempotent on (binding, key): a transport re-delivery returns the
// existing record untouched. With no binding open the record still lands —
// under generation 0, a generation that never has a target — because an
// observed message must be durable before anything else is decided.
func (j *connJournal) Ingest(key [32]byte, blob id.Hash,
	refHash, threadHash [32]byte, now int64) (IngressRecord, bool, error) {

	j.mu.Lock()
	defer j.mu.Unlock()
	gen := uint64(0)
	if j.hasTarget {
		gen = j.gen
	}
	bk := connBindKey{gen: gen, key: key}
	if cur, ok := j.live[bk]; ok {
		return *cur, true, nil
	}
	rec := &IngressRecord{
		Key: key, Binding: gen, State: ConnReceived,
		BodyBlob: blob, RefHash: refHash, ThreadHash: threadHash,
		ReceivedAt: now, UpdatedAt: now,
	}
	if err := j.appendRecord(encodeIngress(rec)); err != nil {
		return IngressRecord{}, false, err
	}
	j.live[bk] = rec
	return *rec, false, nil
}

// Update mutates one record under the lock and persists it; returning
// false from mutate leaves it untouched and writes nothing.
func (j *connJournal) Update(gen uint64, key [32]byte, now int64,
	mutate func(*IngressRecord) bool) (IngressRecord, bool, error) {

	j.mu.Lock()
	defer j.mu.Unlock()
	rec, ok := j.live[connBindKey{gen: gen, key: key}]
	if !ok {
		return IngressRecord{}, false, nil
	}
	before := *rec
	if !mutate(rec) {
		*rec = before
		return before, false, nil
	}
	rec.UpdatedAt = now
	if err := j.appendRecord(encodeIngress(rec)); err != nil {
		*rec = before
		return before, false, err
	}
	j.dead++
	return *rec, true, nil
}

// Pending returns the unfinished records of the CURRENT generation, oldest
// first — the projector's work list, re-derived rather than remembered.
func (j *connJournal) Pending() []IngressRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.hasTarget {
		return nil
	}
	var out []IngressRecord
	for bk, rec := range j.live {
		if bk.gen != j.gen {
			continue
		}
		if rec.State == ConnReceived || rec.State == ConnEmitting {
			out = append(out, *rec)
		}
	}
	sortIngress(out)
	return out
}

// Published resolves an already-projected ingress by its ref hash — the
// reply-edge lookup (In-Reply-To → EventID) and the recovery index.
func (j *connJournal) PublishedByRef(refHash [32]byte) (id.EventID, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, rec := range j.live {
		if rec.State == ConnPublished && rec.RefHash == refHash {
			return rec.EventID, true
		}
	}
	return id.EventID{}, false
}

// PublishedEventIDs of ONE generation — the outbound watcher's authority
// set (plan rev 3: replies may egress only through the binding that
// imported their parent, and only while it is active).
func (j *connJournal) PublishedEventIDs(gen uint64) map[id.EventID]struct{} {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := map[id.EventID]struct{}{}
	for bk, rec := range j.live {
		if bk.gen == gen && rec.State == ConnPublished {
			out[rec.EventID] = struct{}{}
		}
	}
	return out
}

// Counts is the honest one-line summary for status surfaces.
func (j *connJournal) Counts() (pending, published, refused, orphaned int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, rec := range j.live {
		switch rec.State {
		case ConnReceived, ConnEmitting:
			pending++
		case ConnPublished:
			published++
		case ConnRefused:
			refused++
		case ConnOrphaned:
			orphaned++
		}
	}
	return
}

func (j *connJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return nil
	}
	err := j.f.Close()
	j.f = nil
	return err
}

func sortIngress(recs []IngressRecord) {
	for i := 1; i < len(recs); i++ {
		for k := i; k > 0 && recs[k-1].ReceivedAt > recs[k].ReceivedAt; k-- {
			recs[k-1], recs[k] = recs[k], recs[k-1]
		}
	}
}
