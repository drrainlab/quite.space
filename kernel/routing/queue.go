// Durable custody queue (TN-1): append-only segment records in the
// `length ‖ crc32c ‖ record` discipline (the eventlog pattern; no new
// dependencies). What needs durability is BRIDGE CUSTODY — frames accepted
// from one segment awaiting another — including their INGRESS ORIGIN, so
// split-horizon survives a restart (rev 2.1). Enqueue fsyncs before
// returning: a custody ACK may only be sent after the record is on disk.
package routing

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// CustodyRecord is one frame held in custody. The frame stays opaque —
// only cleartext header metadata is stored beside it.
type CustodyRecord struct {
	ID            uint64
	Frame         []byte
	Destination   id.TerminalID
	Priority      signal.Priority
	ExpiresAt     uint64
	IngressLink   LinkID
	IngressDomain LoopDomainID
	EnqueuedAt    int64
	Attempts      uint32

	// NextEligibleAt is when this record may be sent again (unix seconds,
	// 0 = now). Backoff lives IN THE RECORD rather than in a side table of
	// "when did I last send this", because a side table can only answer
	// "is the record I just picked ready?" — and the only thing to do with
	// a no is to stop the whole pass. One waiting record then blocked every
	// other destination behind it.
	NextEligibleAt int64

	// Guaranteed marks a record whose custody has been ACKNOWLEDGED to its
	// sender. The sender was told "I hold this" and may have stopped
	// retrying, so this record can no longer be quietly evicted to make
	// room for a newer one. It leaves only by delivery or by expiry — and
	// expiry is announced, not silent.
	Guaranteed bool

	// Attempt is the SENDER's responsibility token, copied from the frames
	// message that carried this frame and written in the same fsynced
	// append as the frame itself. It has to be durable here rather than in
	// memory: a withdrawal may be issued hours later, possibly after a
	// restart, and it must name the hand-off it belongs to or the sender
	// cannot tell it apart from a receipt for a newer attempt.
	Attempt []byte
}

// QueueCaps bound the store (Pi-readiness).
type QueueCaps struct {
	MaxTotalBytes   int64
	MaxPerDestBytes int64
	OperatorTTL     time.Duration // custody TTL cap; per-item = min(this, ExpiresAt)
}

// DefaultQueueCaps are conservative bridge defaults.
func DefaultQueueCaps() QueueCaps {
	return QueueCaps{
		MaxTotalBytes:   512 << 20,
		MaxPerDestBytes: 64 << 20,
		OperatorTTL:     48 * time.Hour,
	}
}

// ErrNoCustody is returned for frames whose author declared NoCustody.
var ErrNoCustody = errors.New("routing: frame declares NoCustody — refusing store-and-forward")

// ErrExpired is returned for frames already past their custody expiry.
var ErrExpired = errors.New("routing: frame expired — custody refused")

// Queue is a durable priority queue of custody records.
type Queue struct {
	mu     sync.Mutex
	dir    string
	f      *os.File
	nextID uint64
	live   map[uint64]*CustodyRecord
	// byEvent indexes live records by frame EventID. Custody already knows
	// WHEN it accepted a frame — EnqueuedAt, written in the same fsynced
	// append — so anything that needs to answer "did I take this, and when"
	// should ask here rather than keep a second copy that can disagree.
	byEvent map[id.EventID]uint64
	dead    int64 // bytes of tombstoned records in the segment
	total   int64 // bytes of live frames
	perDst  map[id.TerminalID]int64
	caps    QueueCaps
}

const (
	recPut  = 1
	recTomb = 2
	// recDefer updates the retry schedule of a record already in the
	// segment. A full recPut would work but would copy the frame again on
	// every attempt; this is a few bytes.
	recDefer = 3
	// record wire keys (append-only)
	qKeyKind      = 1
	qKeyID        = 2
	qKeyFrame     = 3
	qKeyDest      = 4
	qKeyPriority  = 5
	qKeyExpires   = 6
	qKeyLink      = 7
	qKeyDomain    = 8
	qKeyEnqueued  = 9
	qKeyAttempts  = 10
	qKeyEligible  = 11
	qKeyGuarantee = 12
	qKeyAttempt   = 13
)

// ErrNoRoom is returned when custody cannot be taken without evicting a
// record whose custody was already acknowledged. Refusing here is the
// honest answer: the sender keeps responsibility and will retry, which is
// strictly better than accepting a frame and dropping someone else's.
var ErrNoRoom = errors.New("routing: custody store full of guaranteed records")

// OpenQueue opens (or creates) the queue at dir, replaying the segment.
func OpenQueue(dir string, caps QueueCaps) (*Queue, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	q := &Queue{dir: dir, live: map[uint64]*CustodyRecord{},
		byEvent: map[id.EventID]uint64{},
		perDst:  map[id.TerminalID]int64{}, caps: caps}
	path := filepath.Join(dir, "custody.seg")
	if err := q.replay(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	q.f = f
	return q, nil
}

func (q *Queue) replay(path string) error {
	f, err := os.Open(path)
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
			break // clean EOF or torn tail — truncate below
		}
		n := binary.BigEndian.Uint32(hdr[:4])
		want := binary.BigEndian.Uint32(hdr[4:])
		if n == 0 || n > 64<<20 {
			break
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(f, body); err != nil {
			break
		}
		if crc32.Checksum(body, crcTable) != want {
			break // torn record — everything after is suspect
		}
		valid += 8 + int64(n)
		q.applyRecord(body)
	}
	// Truncate a torn tail so the appender continues from a clean edge.
	return os.Truncate(path, valid)
}

func (q *Queue) applyRecord(body []byte) {
	d := codec.NewDecoder(body)
	m, err := d.ReadMapHeader()
	if err != nil {
		return
	}
	var kind, rid, prio, expires, enq, attempts, eligible, guarantee uint64
	rec := &CustodyRecord{}
	for {
		k, ok, er := m.Next()
		if er != nil || !ok {
			break
		}
		switch k {
		case qKeyKind:
			kind, er = d.ReadUint()
		case qKeyID:
			rid, er = d.ReadUint()
		case qKeyFrame:
			var b []byte
			b, er = d.ReadBytes()
			rec.Frame = append([]byte(nil), b...)
		case qKeyDest:
			var b []byte
			b, er = d.ReadBytes()
			if er == nil && len(b) == id.Size {
				copy(rec.Destination[:], b)
			}
		case qKeyPriority:
			prio, er = d.ReadUint()
		case qKeyExpires:
			expires, er = d.ReadUint()
		case qKeyLink:
			var s string
			s, er = d.ReadText()
			rec.IngressLink = LinkID(s)
		case qKeyDomain:
			var s string
			s, er = d.ReadText()
			rec.IngressDomain = LoopDomainID(s)
		case qKeyEnqueued:
			enq, er = d.ReadUint()
		case qKeyAttempts:
			attempts, er = d.ReadUint()
		case qKeyEligible:
			eligible, er = d.ReadUint()
		case qKeyGuarantee:
			guarantee, er = d.ReadUint()
		case qKeyAttempt:
			var b []byte
			b, er = d.ReadBytes()
			rec.Attempt = append([]byte(nil), b...)
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return
		}
	}
	if rid >= q.nextID {
		q.nextID = rid + 1
	}
	switch kind {
	case recPut:
		rec.ID = rid
		rec.Priority = signal.Priority(prio)
		rec.ExpiresAt = expires
		rec.EnqueuedAt = int64(enq)
		rec.Attempts = uint32(attempts)
		rec.NextEligibleAt = int64(eligible)
		rec.Guaranteed = guarantee != 0
		q.live[rid] = rec
		q.byEvent[id.EventIDOf(rec.Frame)] = rid
		q.total += int64(len(rec.Frame))
		q.perDst[rec.Destination] += int64(len(rec.Frame))
	case recDefer:
		if rec, ok := q.live[rid]; ok {
			rec.Attempts = uint32(attempts)
			rec.NextEligibleAt = int64(eligible)
		}
	case recTomb:
		if old, ok := q.live[rid]; ok {
			q.total -= int64(len(old.Frame))
			q.perDst[old.Destination] -= int64(len(old.Frame))
			q.dead += int64(len(old.Frame))
			delete(q.live, rid)
		}
	}
}

func (q *Queue) appendRecord(body []byte, sync bool) error {
	var buf []byte
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(body)))
	buf = binary.BigEndian.AppendUint32(buf, crc32.Checksum(body, crcTable))
	buf = append(buf, body...)
	if _, err := q.f.Write(buf); err != nil {
		return err
	}
	if sync {
		return q.f.Sync()
	}
	return nil
}

func encodePut(rec *CustodyRecord) []byte {
	buf := codec.AppendMap(nil, 12)
	buf = codec.AppendUint(buf, qKeyKind)
	buf = codec.AppendUint(buf, recPut)
	buf = codec.AppendUint(buf, qKeyID)
	buf = codec.AppendUint(buf, rec.ID)
	buf = codec.AppendUint(buf, qKeyFrame)
	buf = codec.AppendBytes(buf, rec.Frame)
	buf = codec.AppendUint(buf, qKeyDest)
	buf = codec.AppendBytes(buf, rec.Destination[:])
	buf = codec.AppendUint(buf, qKeyPriority)
	buf = codec.AppendUint(buf, uint64(rec.Priority))
	buf = codec.AppendUint(buf, qKeyExpires)
	buf = codec.AppendUint(buf, rec.ExpiresAt)
	buf = codec.AppendUint(buf, qKeyLink)
	buf = codec.AppendText(buf, string(rec.IngressLink))
	buf = codec.AppendUint(buf, qKeyDomain)
	buf = codec.AppendText(buf, string(rec.IngressDomain))
	buf = codec.AppendUint(buf, qKeyEnqueued)
	buf = codec.AppendUint(buf, uint64(rec.EnqueuedAt))
	buf = codec.AppendUint(buf, qKeyAttempts)
	buf = codec.AppendUint(buf, uint64(rec.Attempts))
	buf = codec.AppendUint(buf, qKeyEligible)
	buf = codec.AppendUint(buf, uint64(max(rec.NextEligibleAt, 0)))
	buf = codec.AppendUint(buf, qKeyGuarantee)
	buf = codec.AppendUint(buf, b2u(rec.Guaranteed))
	if len(rec.Attempt) > 0 {
		buf = codec.AppendUint(buf, qKeyAttempt)
		buf = codec.AppendBytes(buf, rec.Attempt)
	}
	return buf
}

func b2u(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

func encodeDefer(rid uint64, attempts uint32, eligible int64) []byte {
	buf := codec.AppendMap(nil, 4)
	buf = codec.AppendUint(buf, qKeyKind)
	buf = codec.AppendUint(buf, recDefer)
	buf = codec.AppendUint(buf, qKeyID)
	buf = codec.AppendUint(buf, rid)
	buf = codec.AppendUint(buf, qKeyAttempts)
	buf = codec.AppendUint(buf, uint64(attempts))
	buf = codec.AppendUint(buf, qKeyEligible)
	buf = codec.AppendUint(buf, uint64(max(eligible, 0)))
	return buf
}

func encodeTomb(rid uint64) []byte {
	buf := codec.AppendMap(nil, 2)
	buf = codec.AppendUint(buf, qKeyKind)
	buf = codec.AppendUint(buf, recTomb)
	buf = codec.AppendUint(buf, qKeyID)
	buf = codec.AppendUint(buf, rid)
	return buf
}

// Enqueue takes custody of a frame: scope/expiry checks, caps (evicting
// the oldest lowest-lane records when over), durable append + fsync.
func (q *Queue) Enqueue(meta FrameMeta, frame []byte, domain LoopDomainID, now time.Time) (uint64, error) {
	return q.enqueue(meta, frame, domain, now, false, nil)
}

// EnqueueGuaranteed takes custody of a frame the caller intends to
// ACKNOWLEDGE. Room is checked BEFORE the promise is made: the record is
// written already marked, in one fsynced append, so there is no window in
// which an acknowledged frame is evictable. If space can only be found by
// dropping another guaranteed record, this returns ErrNoRoom and NOTHING is
// accepted — the sender keeps responsibility and retries, which is the one
// honest outcome. A bridge that accepted the frame and quietly dropped
// someone else's would have turned a signed promise into a lie.
// attempt is the sender's responsibility token; it is written in the same
// fsynced append as the frame, so a withdrawal issued later — possibly
// after a restart — can still name the hand-off it belongs to.
func (q *Queue) EnqueueGuaranteed(meta FrameMeta, frame []byte, domain LoopDomainID,
	now time.Time, attempt []byte) (uint64, error) {
	return q.enqueue(meta, frame, domain, now, true, attempt)
}

func (q *Queue) enqueue(meta FrameMeta, frame []byte, domain LoopDomainID,
	now time.Time, guaranteed bool, attempt []byte) (uint64, error) {

	if meta.Scope == signal.NoCustody {
		return 0, ErrNoCustody
	}
	if meta.ExpiresAt != 0 && uint64(now.Unix()) >= meta.ExpiresAt {
		return 0, ErrExpired
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.caps.MaxPerDestBytes > 0 &&
		q.perDst[meta.Destination]+int64(len(frame)) > q.caps.MaxPerDestBytes {
		return 0, fmt.Errorf("routing: destination custody quota exceeded")
	}
	if q.caps.MaxTotalBytes > 0 {
		if err := q.evictUntilLocked(int64(len(frame))); err != nil {
			return 0, err
		}
	}
	rec := &CustodyRecord{
		ID: q.nextID, Frame: append([]byte(nil), frame...),
		Destination: meta.Destination, Priority: meta.Priority,
		ExpiresAt: meta.ExpiresAt, IngressLink: meta.IngressLink,
		IngressDomain: domain, EnqueuedAt: now.Unix(),
		Guaranteed: guaranteed,
		Attempt:    append([]byte(nil), attempt...),
	}
	q.nextID++
	if err := q.appendRecord(encodePut(rec), true); err != nil {
		return 0, err
	}
	q.live[rec.ID] = rec
	q.byEvent[id.EventIDOf(rec.Frame)] = rec.ID
	q.total += int64(len(rec.Frame))
	q.perDst[rec.Destination] += int64(len(rec.Frame))
	return rec.ID, nil
}

// Held returns a copy of the live record holding this frame, if any. The
// EnqueuedAt it carries is the durable acceptance time: it was written and
// fsynced with the record itself, so it survives a crash between taking
// custody and acknowledging it.
func (q *Queue) Held(eid id.EventID) (*CustodyRecord, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	rid, ok := q.byEvent[eid]
	if !ok {
		return nil, false
	}
	rec, ok := q.live[rid]
	if !ok {
		return nil, false
	}
	cp := *rec
	return &cp, true
}

// evictUntilLocked frees room for want bytes: oldest records in the LOWEST
// priority lanes go first. Caller holds the lock.
func (q *Queue) evictUntilLocked(want int64) error {
	if q.total+want <= q.caps.MaxTotalBytes {
		return nil
	}
	recs := make([]*CustodyRecord, 0, len(q.live))
	for _, r := range q.live {
		if r.Guaranteed {
			continue // acknowledged: not ours to drop any more
		}
		recs = append(recs, r)
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].Priority != recs[j].Priority {
			return recs[i].Priority > recs[j].Priority // lowest lane first
		}
		return recs[i].EnqueuedAt < recs[j].EnqueuedAt
	})
	for _, r := range recs {
		if q.total+want <= q.caps.MaxTotalBytes {
			return nil
		}
		q.tombLocked(r.ID)
	}
	if q.total+want > q.caps.MaxTotalBytes {
		return ErrNoRoom
	}
	return nil
}

func (q *Queue) tombLocked(rid uint64) {
	rec, ok := q.live[rid]
	if !ok {
		return
	}
	_ = q.appendRecord(encodeTomb(rid), false)
	delete(q.byEvent, id.EventIDOf(rec.Frame))
	q.total -= int64(len(rec.Frame))
	q.perDst[rec.Destination] -= int64(len(rec.Frame))
	q.dead += int64(len(rec.Frame))
	delete(q.live, rid)
}

// compactThreshold is how much superseded weight the segment tolerates
// before it is rewritten.
const compactThreshold = 1 << 20

// agingStep promotes a waiting record one lane per interval so low lanes
// eventually drain even under sustained high-priority load.
const agingStep = 2 * time.Minute

// Next picks the record to send on an egress link: strict priority with
// aging, oldest-first within a lane, skipping split-horizon-suppressed,
// expired, and destination-filtered records. ok=false when nothing sendable.
func (q *Queue) Next(egressLink LinkID, egressDomain LoopDomainID,
	allow func(dest id.TerminalID) bool, now time.Time) (*CustodyRecord, bool) {

	batch := q.NextBatch(egressLink, egressDomain, allow, now, 1)
	if len(batch) == 0 {
		return nil, false
	}
	return batch[0], true
}

// NextBatch returns up to max eligible records for an egress, in lane +
// aging order, WITHOUT mutating the queue. A record in backoff is skipped,
// not treated as the end of the queue: the whole point is that one waiting
// destination must not stall every other one behind it.
func (q *Queue) NextBatch(egressLink LinkID, egressDomain LoopDomainID,
	allow func(dest id.TerminalID) bool, now time.Time, max int) []*CustodyRecord {

	if max <= 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	nowU := uint64(now.Unix())
	nowSec := now.Unix()
	type scored struct {
		rec  *CustodyRecord
		lane float64
	}
	var eligible []scored
	for _, r := range q.live {
		if r.ExpiresAt != 0 && nowU >= r.ExpiresAt {
			continue // swept later
		}
		if r.NextEligibleAt > nowSec {
			continue // in backoff; the next record still gets its turn
		}
		if SuppressForward(r.IngressLink, egressLink, r.IngressDomain, egressDomain) {
			continue
		}
		if allow != nil && !allow(r.Destination) {
			continue
		}
		lane := float64(r.Priority) -
			float64(nowSec-r.EnqueuedAt)/agingStep.Seconds()
		eligible = append(eligible, scored{r, lane})
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].lane != eligible[j].lane {
			return eligible[i].lane < eligible[j].lane
		}
		if eligible[i].rec.EnqueuedAt != eligible[j].rec.EnqueuedAt {
			return eligible[i].rec.EnqueuedAt < eligible[j].rec.EnqueuedAt
		}
		return eligible[i].rec.ID < eligible[j].rec.ID
	})
	if len(eligible) > max {
		eligible = eligible[:max]
	}
	out := make([]*CustodyRecord, 0, len(eligible))
	for _, e := range eligible {
		cp := *e.rec
		out = append(out, &cp)
	}
	return out
}

// Ack marks a record delivered (tombstone) and compacts opportunistically.
func (q *Queue) Ack(rid uint64) {
	q.mu.Lock()
	q.tombLocked(rid)
	needCompact := q.dead > compactThreshold && q.dead > q.total
	q.mu.Unlock()
	if needCompact {
		_ = q.Compact()
	}
}

// Requeue bumps the attempt counter and holds the record back until
// notBefore. A zero notBefore leaves it immediately eligible.
func (q *Queue) Requeue(rid uint64, notBefore time.Time) {
	q.mu.Lock()
	rec, ok := q.live[rid]
	if !ok {
		q.mu.Unlock()
		return
	}
	rec.Attempts++
	if !notBefore.IsZero() {
		rec.NextEligibleAt = notBefore.Unix()
	}
	// Durable, but not fsynced: losing the last few deferrals in a crash
	// costs a duplicate broadcast, never a lost frame — and paying for an
	// fsync on every attempt would tax the common path for nothing.
	body := encodeDefer(rid, rec.Attempts, rec.NextEligibleAt)
	_ = q.appendRecord(body, false)
	// Every deferral supersedes the one before it, so those bytes are dead
	// weight the moment they are written. Counting them is what makes
	// compaction eventually fire: a gateway retrying one stuck record every
	// 45 seconds for a month would otherwise grow its segment without limit
	// — the same unbounded growth the old last-sent map had, moved to disk.
	q.dead += int64(len(body))
	needCompact := q.dead > compactThreshold && q.dead > q.total
	q.mu.Unlock()
	if needCompact {
		_ = q.Compact()
	}
}

// Sweep tombstones records past custody expiry (min of operator TTL and
// the author's ExpiresAt). Returns how many were dropped, and copies of any
// GUARANTEED records among them: their sender was told the frame was held,
// so the end of that custody has to be announced rather than discovered.
func (q *Queue) Sweep(now time.Time) (dropped int, lapsed []*CustodyRecord) {
	q.mu.Lock()
	defer q.mu.Unlock()
	nowU := uint64(now.Unix())
	for rid, rec := range q.live {
		expired := rec.ExpiresAt != 0 && nowU >= rec.ExpiresAt
		if q.caps.OperatorTTL > 0 &&
			now.Sub(time.Unix(rec.EnqueuedAt, 0)) > q.caps.OperatorTTL {
			expired = true
		}
		if !expired {
			continue
		}
		if rec.Guaranteed {
			cp := *rec
			lapsed = append(lapsed, &cp)
		}
		q.tombLocked(rid)
		dropped++
	}
	return dropped, lapsed
}

// Compact rewrites the segment with live records only.
func (q *Queue) Compact() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	path := filepath.Join(q.dir, "custody.seg")
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	ids := make([]uint64, 0, len(q.live))
	for rid := range q.live {
		ids = append(ids, rid)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, rid := range ids {
		body := encodePut(q.live[rid])
		var buf []byte
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(body)))
		buf = binary.BigEndian.AppendUint32(buf, crc32.Checksum(body, crcTable))
		buf = append(buf, body...)
		if _, err := f.Write(buf); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if err := q.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	nf, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	q.f = nf
	q.dead = 0
	return nil
}

// Len reports live records; TotalBytes the live frame bytes.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.live)
}

// TotalBytes reports live custody bytes.
func (q *Queue) TotalBytes() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.total
}

// Close releases the segment file.
func (q *Queue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.f.Close()
}
