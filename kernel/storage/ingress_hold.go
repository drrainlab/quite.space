// IngressHold (MD-0b): local durable custody for bytes a destructive
// transport has already handed over.
//
//	transport custody  →  LOCAL DURABLE CUSTODY  →  semantic admission
//
// The relay's Collect is destructive: once it yields an item, the relay has
// forgotten it while the sender counts it delivered. So from that instant the
// bytes are OURS until admission reaches a terminal state — applied, or
// finally refused. This is the middle stage that did not exist.
//
// DELIBERATELY NOT PART OF THE KEYSTORE. That file is rare identity and
// config writes, sealed and rewritten whole; this is potentially large raw
// frames created and deleted constantly. Sharing one file would make every
// ordinary message rewrite the identity store.
//
// KEYED BY THE HASH OF THE EXACT RAW BYTES, never by EventID. That keeps the
// law literal — first we took custody of opaque bytes, only afterwards do we
// interpret them — and it means storage never has to trust a field of an
// envelope that has not been admitted yet. The two dedups then answer
// different questions and neither pretends to be the other:
//
//	IngressHold dedup   the exact same transport bytes
//	eventlog dedup      the same protocol EventID
//
// PLAINTEXT, like the event log and the routing custody queue that hold the
// same material. What is stored is a signed envelope whose payload is already
// sealed per space; encrypting the frame here would protect nothing that the
// log it is about to enter does not expose anyway.
//
// v1 BOUNDS BY SLOTS, not bytes, and the bound is a PRE-COLLECT THRESHOLD
// rather than an on-disk maximum: capacity is admission control applied
// before custody is taken, never a validity condition on custody already
// taken. A conservative item count against the size ceiling the node already
// enforces per relay item is provable today, and a byte-aware budget can be
// added later without changing this model. There is
// no eviction and no TTL here BY DESIGN: an old held frame is a diagnostic
// (held_too_long), never a deletion, because deleting on age reproduces the
// exact loss this store exists to prevent, one day later.
package storage

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// THERE IS DELIBERATELY NO ErrHoldFull, and that was measured rather than
// assumed. By the time Put is called the relay has ALREADY forgotten the
// item, so a refusal here is not backpressure — it is the loss this store
// exists to prevent, wearing the word "full". Capacity is therefore a
// question asked BEFORE the destructive take (RemainingItems), never an
// answer given after it.
//
// The bound is consequently soft by exactly one drain: the client cannot ask
// the relay for "at most three items" (the byte budget is the server's, and
// a cap addresses a whole mailbox), so a single reply may overshoot. It
// cannot overshoot by more than one reply, which the relay itself bounds
// (CollectMaxBytes, 8 MiB) — so "slots + one reply" is a real ceiling, and
// OverCapacity reports when we are in that overshoot.
//
// A NON-DESTRUCTIVE transport is a different matter: a LAN sender still
// holds its own copy, so refusing it there loses nothing and the caller may
// decline before handing bytes to Put. That decision belongs to the caller
// which knows the transport; this store stays ignorant of custody semantics
// on purpose.

// ErrHeldBytesCorrupt means what is on disk is no longer what a transport
// yielded. Fail closed: the replay's promise is the SAME EventID and the
// SAME signature, and altered bytes can keep neither.
var ErrHeldBytesCorrupt = errors.New("storage: held ingress bytes do not match their hash")

// HoldID is the SHA-256 of the exact raw bytes — a STORAGE identity, and
// deliberately not the protocol's EventID (see the package comment).
type HoldID = id.Hash

// IngressSource is DIAGNOSTICS ONLY. Nothing about admission may depend on
// it: a frame is judged by what it says, not by which wire brought it.
// Serialized; values must not move.
type IngressSource uint8

const (
	IngressUnknown IngressSource = 0
	IngressRelay   IngressSource = 1
	IngressLAN     IngressSource = 2
	IngressRadio   IngressSource = 3
)

func (s IngressSource) String() string {
	switch s {
	case IngressRelay:
		return "relay"
	case IngressLAN:
		return "lan"
	case IngressRadio:
		return "radio"
	}
	return "unknown"
}

// HeldIngressMeta is the little that is worth storing BESIDE the bytes.
//
// What is absent is the point: no principal, no device, no space, no
// authoritative reason. All of that is re-derived from `raw` at every
// re-judgement, so a change to the decoder or to the admission rules cannot
// leave a second, stale source of truth on disk. A cached reason for the UI
// may live in memory; correctness must never read it.
type HeldIngressMeta struct {
	// ReceivedAt is when custody began (unix seconds) — the clock
	// held_too_long is measured against.
	ReceivedAt int64
	Source     IngressSource
}

// heldMetaFields is the record arity, NAMED so appending a field is a
// deliberate act. Only ever append.
const heldMetaFields = 2

// HeldIngress is one frame in custody.
type HeldIngress struct {
	ID   HoldID
	Raw  []byte
	Meta HeldIngressMeta
}

// IngressHold is the on-disk hold: one directory, two files per frame.
//
//	ingress-hold/<hash>.frame   the exact bytes
//	ingress-hold/<hash>.meta    diagnostics
type IngressHold struct {
	dir         string
	targetItems int
}

// OpenIngressHold opens (or creates) the hold under the data root.
func (r *Root) OpenIngressHold(targetItems int) (*IngressHold, error) {
	if targetItems <= 0 {
		return nil, errors.New("storage: ingress hold needs a positive item bound")
	}
	dir := filepath.Join(r.dir, "ingress-hold")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &IngressHold{dir: dir, targetItems: targetItems}, nil
}

const (
	frameExt = ".frame"
	metaExt  = ".meta"
)

func (h *IngressHold) framePath(hid HoldID) string {
	return filepath.Join(h.dir, hid.Hex()+frameExt)
}

func (h *IngressHold) metaPath(hid HoldID) string {
	return filepath.Join(h.dir, hid.Hex()+metaExt)
}

// Put takes durable custody of raw bytes and returns their hold id.
//
// IDEMPOTENT BY CONTENT ADDRESSING. Relay and LAN may both yield the same
// frame; a repeat consumes no slot and does NOT refresh ReceivedAt, so a peer
// that re-sends cannot reset the age of what it is making us keep.
//
// ORDER: meta first, then the frame. The frame's presence IS the record of
// custody, so a crash between the two writes leaves at worst an orphan
// diagnostic (ignored, and reclaimed by the next Put or Delete of the same
// bytes) and never a frame whose diagnostics are missing.
//
// IT DOES NOT REFUSE A FULL HOLD. See the ErrHoldFull comment above: these
// bytes are already ours, and the only thing a refusal could achieve here is
// to drop them.
func (h *IngressHold) Put(raw []byte, meta HeldIngressMeta) (HoldID, error) {
	hid := id.HashOf(raw)
	if _, err := os.Stat(h.framePath(hid)); err == nil {
		return hid, nil // already ours; content-addressing dedups
	}
	if err := writeFileSynced(h.metaPath(hid), appendHeldMeta(nil, meta)); err != nil {
		return hid, err
	}
	if err := writeFileSynced(h.framePath(hid), raw); err != nil {
		return hid, err
	}
	return hid, nil
}

// Delete drops one held frame. Missing is not an error: the caller is
// deleting, and already-gone is the desired state — the crash boundary
// replays a delete after a restart on purpose.
func (h *IngressHold) Delete(hid HoldID) error {
	// Frame first: while it exists the bytes are held, so removing the
	// diagnostics first could leave a held frame without them.
	if err := removeIfPresent(h.framePath(hid)); err != nil {
		return err
	}
	return removeIfPresent(h.metaPath(hid))
}

// List returns every held frame, oldest custody first (ties broken by id so
// the order is total and a re-judgement pass is reproducible).
//
// Re-verifies every frame against its hash and FAILS CLOSED on a mismatch:
// serving altered bytes would break the one promise the replay makes.
func (h *IngressHold) List() ([]HeldIngress, error) {
	names, err := h.frameNames()
	if err != nil {
		return nil, err
	}
	out := make([]HeldIngress, 0, len(names))
	for _, name := range names {
		hid, ok := parseHoldID(name)
		if !ok {
			continue // not ours to interpret
		}
		raw, err := os.ReadFile(filepath.Join(h.dir, name))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // deleted underneath us; nothing was lost
			}
			return nil, err
		}
		if id.HashOf(raw) != hid {
			return nil, fmt.Errorf("%w: %s", ErrHeldBytesCorrupt, hid.Hex())
		}
		// Meta is diagnostics, so its absence or corruption costs the
		// diagnostics and never the bytes.
		meta, _ := h.readMeta(hid)
		out = append(out, HeldIngress{ID: hid, Raw: raw, Meta: meta})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Meta.ReceivedAt != out[j].Meta.ReceivedAt {
			return out[i].Meta.ReceivedAt < out[j].Meta.ReceivedAt
		}
		return out[i].ID.Hex() < out[j].ID.Hex()
	})
	return out, nil
}

// Count is how many frames are in custody. An unreadable directory counts as
// FULL rather than empty: the safe direction is to stop collecting, since
// collecting on a wrong "there is room" is what loses frames.
func (h *IngressHold) Count() int {
	names, err := h.frameNames()
	if err != nil {
		return h.targetItems
	}
	return len(names)
}

// RemainingItems is how much more destructive ingress this node may take
// custody of. This is the number the sync tick must consult BEFORE it
// collects — capacity is flow control, not cleanup.
func (h *IngressHold) RemainingItems() int {
	n := h.targetItems - h.Count()
	if n < 0 {
		return 0
	}
	return n
}

// TargetItems is the PRE-COLLECT THRESHOLD, and deliberately not called a
// maximum: it is not a hard on-disk cardinality bound, because one drain may
// legitimately overshoot it (see above). Anyone tempted to assert
// `Count() <= TargetItems()` would be asserting something this store must not
// promise — and would "fix" the overshoot by throwing frames away, which is
// the loss the whole file exists to prevent.
func (h *IngressHold) TargetItems() int { return h.targetItems }

// OverCapacity reports the overshoot state: one drain carried more than there
// was room for, and we kept it because dropping it was the alternative. It is
// a DIAGNOSTIC and a reason to stop collecting — never a reason to delete.
func (h *IngressHold) OverCapacity() bool { return h.Count() > h.targetItems }

func (h *IngressHold) frameNames() ([]string, error) {
	ents, err := os.ReadDir(h.dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), frameExt) {
			continue
		}
		names = append(names, e.Name())
	}
	return names, nil
}

func (h *IngressHold) readMeta(hid HoldID) (HeldIngressMeta, error) {
	data, err := os.ReadFile(h.metaPath(hid))
	if err != nil {
		return HeldIngressMeta{}, err
	}
	return readHeldMeta(data)
}

// parseHoldID reads a hold id back out of a file name. A name that is not a
// full 32-byte hex hash is not ours: it is skipped rather than guessed at,
// because a hold id that cannot be re-derived cannot verify its own bytes.
func parseHoldID(frameName string) (HoldID, bool) {
	raw, err := hex.DecodeString(strings.TrimSuffix(frameName, frameExt))
	if err != nil || len(raw) != len(HoldID{}) {
		return HoldID{}, false
	}
	var hid HoldID
	copy(hid[:], raw)
	return hid, true
}

func appendHeldMeta(buf []byte, m HeldIngressMeta) []byte {
	buf = codec.AppendArray(buf, heldMetaFields)
	buf = codec.AppendUint(buf, uint64(m.ReceivedAt))
	buf = codec.AppendUint(buf, uint64(m.Source))
	return buf
}

func readHeldMeta(data []byte) (HeldIngressMeta, error) {
	var m HeldIngressMeta
	d := codec.NewDecoder(data)
	acount, err := d.ReadArray()
	if err != nil {
		return m, err
	}
	if acount >= 1 {
		v, er := d.ReadUint()
		if er != nil {
			return m, er
		}
		m.ReceivedAt = int64(v)
	}
	if acount >= 2 {
		v, er := d.ReadUint()
		if er != nil {
			return m, er
		}
		m.Source = IngressSource(v)
	}
	for i := heldMetaFields; i < acount; i++ {
		if er := d.SkipItem(); er != nil {
			return m, er
		}
	}
	return m, nil
}

// writeFileSynced writes atomically AND durably: tmp → fsync file → rename →
// fsync directory.
//
// The blob store next door skips the fsyncs, and correctly so — a lost blob
// can be fetched again. HELD INGRESS CANNOT: the relay deleted it on
// Collect. So this follows the routing custody queue instead, which fsyncs
// before it dares acknowledge custody, for the same reason.
func writeFileSynced(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return syncDir(filepath.Dir(path))
}

// syncDir makes the rename itself durable — without it the file's contents
// are on disk but the name pointing at them may not be.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func removeIfPresent(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
