// Persistent delivery ledger (RB-1 step 1). The signed event is already
// durable in the log — this is NOT a second journal of content. What it
// records is RESPONSIBILITY: for each event this device authored, whether
// anyone else has taken it on yet, and what has actually been proven.
//
// The two are kept apart on purpose. `State` is a local question — do I
// still have to retry this? `Proof` is the protocol ladder from ADR-007,
// unextended: it says what someone can demonstrate, and no ledger
// bookkeeping may push it higher than the evidence allows.
//
// Everything here is per ATTEMPT, not merely per event. A gateway ACK
// belongs to one specific hand-off, and an acknowledgement that arrives
// after its attempt was abandoned must not complete the attempt that
// replaced it — see the note on AttemptID.
package node

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// AttemptID identifies ONE hand-off of one event to one transport.
//
// It is minted by this node — never by a gateway — because the node is the
// only party that knows which attempt is current, and the alternatives both
// fail in ordinary conditions. Correlating on the gateway's clock breaks
// when the clocks disagree, which for an off-grid radio node is the normal
// case. Correlating on a gateway-minted lease alone breaks when the first
// attempt's acknowledgement is LOST and arrives after a second attempt has
// begun: the node never recorded that lease, so it cannot recognise it as
// retired, and would credit the new attempt with the old attempt's proof.
//
// A node-minted token that travels out with the frames and comes back in
// the receipt is immune to both.
type AttemptID [16]byte

// Zero reports the unset token.
func (a AttemptID) Zero() bool { return a == AttemptID{} }

// Hex renders the token for diagnostics.
func (a AttemptID) Hex() string { return hex.EncodeToString(a[:]) }

// NewAttemptID mints a fresh token.
func NewAttemptID() (AttemptID, error) {
	var a AttemptID
	if _, err := rand.Read(a[:]); err != nil {
		return a, err
	}
	return a, nil
}

// IntentState is the LOCAL responsibility question, not a claim about the
// world. It never appears on the wire.
type IntentState uint8

const (
	// IntentPending: ours to send, nothing handed over yet.
	IntentPending IntentState = iota
	// IntentInFlight: handed to a transport on the current attempt. Bytes
	// left the machine; nobody has taken responsibility.
	IntentInFlight
	// IntentCustody: a gateway signed for it. We may stop retrying until
	// the custody horizon passes or the gateway withdraws.
	IntentCustody
	// IntentRetryable: custody ended without delivery — withdrawn or
	// expired. Ours again, and the next attempt gets a new token.
	IntentRetryable
	// IntentSettled: terminal. Someone downstream proved receipt, or the
	// event aged out of our concern.
	IntentSettled
)

func (s IntentState) String() string {
	switch s {
	case IntentPending:
		return "pending"
	case IntentInFlight:
		return "in_flight"
	case IntentCustody:
		return "in_custody"
	case IntentRetryable:
		return "retryable"
	case IntentSettled:
		return "settled"
	}
	return "unknown"
}

// DeliveryIntent is one event's outstanding responsibility.
type DeliveryIntent struct {
	EventID id.EventID
	Space   id.TerminalID

	// Attempt is the current hand-off token; AttemptNo counts them.
	Attempt   AttemptID
	AttemptNo uint32

	// Transport names where the current attempt went ("relay", "radio").
	Transport string

	State IntentState
	// Proof is the protocol ladder (ADR-007), never extended here. The
	// ledger may not raise it beyond what was demonstrated.
	Proof claims.DeliveryLevel

	// Lease is the gateway's own id for the custody it granted, recorded
	// when its ACK arrives. Diagnostic: correlation runs on Attempt, which
	// this node controls. LeaseExpires is the horizon the gateway promised —
	// the backstop that returns an intent to retryable if a withdrawal is
	// lost on the air.
	Lease        string
	LeaseExpires int64

	NextAttemptAt int64
	UpdatedAt     int64
}

// Retryable reports whether this intent needs another attempt at `now`.
// Custody counts as covered only until the horizon the gateway promised: a
// withdrawal can be lost on the air, so the expiry is what guarantees
// responsibility cannot hang forever.
func (in DeliveryIntent) Retryable(now time.Time) bool {
	switch in.State {
	case IntentSettled:
		return false
	case IntentCustody:
		if in.LeaseExpires == 0 || now.Unix() < in.LeaseExpires {
			return false
		}
		return true
	}
	return in.NextAttemptAt == 0 || now.Unix() >= in.NextAttemptAt
}

// ---- durable store ----

const (
	lgKeyKind      = 1
	lgKeyEvent     = 2
	lgKeySpace     = 3
	lgKeyAttempt   = 4
	lgKeyAttemptNo = 5
	lgKeyTransport = 6
	lgKeyState     = 7
	lgKeyProof     = 8
	lgKeyLease     = 9
	lgKeyLeaseExp  = 10
	lgKeyNextAt    = 11
	lgKeyUpdated   = 12

	lgRecPut  = 1
	lgRecDrop = 2
)

// ledgerCrc matches the eventlog/custody-queue discipline.
var ledgerCrc = crc32.MakeTable(crc32.Castagnoli)

// ErrLedgerFull is returned when the quota refuses a new intent.
var ErrLedgerFull = errors.New("node: delivery ledger full")

// Ledger is the append-only intent store.
type Ledger struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	live    map[id.EventID]*DeliveryIntent
	dead    int
	maxLive int
}

// OpenLedger opens (or creates) the ledger, replaying and truncating a torn
// tail exactly as the event log and the custody queue do.
func OpenLedger(dir string, maxLive int) (*Ledger, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if maxLive <= 0 {
		maxLive = 50_000
	}
	l := &Ledger{
		path:    filepath.Join(dir, "delivery.ledger"),
		live:    map[id.EventID]*DeliveryIntent{},
		maxLive: maxLive,
	}
	if err := l.replay(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	l.f = f
	return l, nil
}

func (l *Ledger) replay() error {
	f, err := os.Open(l.path)
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
		if crc32.Checksum(body, ledgerCrc) != want {
			break // torn record: everything after it is suspect
		}
		valid += 8 + int64(n)
		l.apply(body)
	}
	return os.Truncate(l.path, valid)
}

func (l *Ledger) apply(body []byte) {
	d := codec.NewDecoder(body)
	m, err := d.ReadMapHeader()
	if err != nil {
		return
	}
	var kind, attemptNo, state, proof, leaseExp, nextAt, updated uint64
	in := &DeliveryIntent{}
	for {
		k, ok, er := m.Next()
		if er != nil || !ok {
			break
		}
		switch k {
		case lgKeyKind:
			kind, er = d.ReadUint()
		case lgKeyEvent:
			var b []byte
			if b, er = d.ReadBytes(); er == nil && len(b) == id.Size {
				copy(in.EventID[:], b)
			}
		case lgKeySpace:
			var b []byte
			if b, er = d.ReadBytes(); er == nil && len(b) == id.Size {
				copy(in.Space[:], b)
			}
		case lgKeyAttempt:
			var b []byte
			if b, er = d.ReadBytes(); er == nil && len(b) == len(in.Attempt) {
				copy(in.Attempt[:], b)
			}
		case lgKeyAttemptNo:
			attemptNo, er = d.ReadUint()
		case lgKeyTransport:
			in.Transport, er = d.ReadText()
		case lgKeyState:
			state, er = d.ReadUint()
		case lgKeyProof:
			proof, er = d.ReadUint()
		case lgKeyLease:
			in.Lease, er = d.ReadText()
		case lgKeyLeaseExp:
			leaseExp, er = d.ReadUint()
		case lgKeyNextAt:
			nextAt, er = d.ReadUint()
		case lgKeyUpdated:
			updated, er = d.ReadUint()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return
		}
	}
	switch kind {
	case lgRecPut:
		in.AttemptNo = uint32(attemptNo)
		in.State = IntentState(state)
		in.Proof = claims.DeliveryLevel(proof)
		in.LeaseExpires = int64(leaseExp)
		in.NextAttemptAt = int64(nextAt)
		in.UpdatedAt = int64(updated)
		if _, existed := l.live[in.EventID]; existed {
			l.dead++
		}
		l.live[in.EventID] = in
	case lgRecDrop:
		if _, ok := l.live[in.EventID]; ok {
			delete(l.live, in.EventID)
			l.dead++
		}
	}
}

func encodeIntent(kind uint64, in *DeliveryIntent) []byte {
	buf := codec.AppendMap(nil, 12)
	buf = codec.AppendUint(buf, lgKeyKind)
	buf = codec.AppendUint(buf, kind)
	buf = codec.AppendUint(buf, lgKeyEvent)
	buf = codec.AppendBytes(buf, in.EventID[:])
	buf = codec.AppendUint(buf, lgKeySpace)
	buf = codec.AppendBytes(buf, in.Space[:])
	buf = codec.AppendUint(buf, lgKeyAttempt)
	buf = codec.AppendBytes(buf, in.Attempt[:])
	buf = codec.AppendUint(buf, lgKeyAttemptNo)
	buf = codec.AppendUint(buf, uint64(in.AttemptNo))
	buf = codec.AppendUint(buf, lgKeyTransport)
	buf = codec.AppendText(buf, in.Transport)
	buf = codec.AppendUint(buf, lgKeyState)
	buf = codec.AppendUint(buf, uint64(in.State))
	buf = codec.AppendUint(buf, lgKeyProof)
	buf = codec.AppendUint(buf, uint64(in.Proof))
	buf = codec.AppendUint(buf, lgKeyLease)
	buf = codec.AppendText(buf, in.Lease)
	buf = codec.AppendUint(buf, lgKeyLeaseExp)
	buf = codec.AppendUint(buf, uint64(max(in.LeaseExpires, 0)))
	buf = codec.AppendUint(buf, lgKeyNextAt)
	buf = codec.AppendUint(buf, uint64(max(in.NextAttemptAt, 0)))
	buf = codec.AppendUint(buf, lgKeyUpdated)
	buf = codec.AppendUint(buf, uint64(max(in.UpdatedAt, 0)))
	return buf
}

// appendRecord writes one record; sync forces it to disk before returning.
func (l *Ledger) appendRecord(body []byte, sync bool) error {
	var buf []byte
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(body)))
	buf = binary.BigEndian.AppendUint32(buf, crc32.Checksum(body, ledgerCrc))
	buf = append(buf, body...)
	if _, err := l.f.Write(buf); err != nil {
		return err
	}
	if sync {
		return l.f.Sync()
	}
	return nil
}

// Enqueue records responsibility for an event. It is IDEMPOTENT on
// EventID: re-recording an event already tracked returns the existing
// intent untouched, so a restart that re-walks the log cannot multiply
// responsibility or reset an attempt that is still in flight.
func (l *Ledger) Enqueue(eid id.EventID, space id.TerminalID, now time.Time) (DeliveryIntent, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if cur, ok := l.live[eid]; ok {
		return *cur, nil
	}
	if len(l.live) >= l.maxLive {
		return DeliveryIntent{}, ErrLedgerFull
	}
	in := &DeliveryIntent{
		EventID:   eid,
		Space:     space,
		State:     IntentPending,
		Proof:     claims.DeliveryCreatedLocal,
		UpdatedAt: now.Unix(),
	}
	if err := l.appendRecord(encodeIntent(lgRecPut, in), true); err != nil {
		return DeliveryIntent{}, err
	}
	l.live[eid] = in
	return *in, nil
}

// Get returns a copy of the intent for an event.
func (l *Ledger) Get(eid id.EventID) (DeliveryIntent, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	in, ok := l.live[eid]
	if !ok {
		return DeliveryIntent{}, false
	}
	return *in, true
}

// Update applies a mutation under the lock and persists the result. The
// callback sees the live record; returning false leaves it untouched and
// writes nothing.
func (l *Ledger) Update(eid id.EventID, now time.Time,
	mutate func(*DeliveryIntent) bool) (DeliveryIntent, bool, error) {

	l.mu.Lock()
	defer l.mu.Unlock()
	in, ok := l.live[eid]
	if !ok {
		return DeliveryIntent{}, false, nil
	}
	before := *in
	if !mutate(in) {
		*in = before
		return before, false, nil
	}
	in.UpdatedAt = now.Unix()
	if err := l.appendRecord(encodeIntent(lgRecPut, in), true); err != nil {
		*in = before // the change did not reach disk; do not pretend it did
		return before, false, err
	}
	l.dead++
	return *in, true, nil
}

// Settle drops an intent: responsibility is over.
func (l *Ledger) Settle(eid id.EventID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	in, ok := l.live[eid]
	if !ok {
		return nil
	}
	rec := *in
	if err := l.appendRecord(encodeIntent(lgRecDrop, &rec), true); err != nil {
		return err
	}
	delete(l.live, eid)
	l.dead++
	return nil
}

// Due returns intents needing an attempt at `now`, oldest first.
func (l *Ledger) Due(now time.Time, limit int) []DeliveryIntent {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]DeliveryIntent, 0, min(limit, len(l.live)))
	for _, in := range l.live {
		if in.Retryable(now) {
			out = append(out, *in)
		}
	}
	sortIntents(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Len reports outstanding responsibility.
func (l *Ledger) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.live)
}

// Close releases the file.
func (l *Ledger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// Compact rewrites the ledger with only live intents.
func (l *Ledger) Compact() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	tmp := l.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	ordered := make([]*DeliveryIntent, 0, len(l.live))
	for _, in := range l.live {
		ordered = append(ordered, in)
	}
	sortIntentPtrs(ordered)
	for _, in := range ordered {
		body := encodeIntent(lgRecPut, in)
		var buf []byte
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(body)))
		buf = binary.BigEndian.AppendUint32(buf, crc32.Checksum(body, ledgerCrc))
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
	if l.f != nil {
		l.f.Close()
	}
	if err := os.Rename(tmp, l.path); err != nil {
		return err
	}
	nf, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	l.f = nf
	l.dead = 0
	return nil
}

// sortIntents orders by oldest update first, then by event id so the order
// is total and stable across runs (map iteration is not).
func sortIntents(in []DeliveryIntent) {
	sort.Slice(in, func(i, j int) bool {
		if in[i].UpdatedAt != in[j].UpdatedAt {
			return in[i].UpdatedAt < in[j].UpdatedAt
		}
		return string(in[i].EventID[:]) < string(in[j].EventID[:])
	})
}

func sortIntentPtrs(in []*DeliveryIntent) {
	sort.Slice(in, func(i, j int) bool {
		return string(in[i].EventID[:]) < string(in[j].EventID[:])
	})
}
