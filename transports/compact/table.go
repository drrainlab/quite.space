// TN-2B: a link-scoped id table mapping repeated 32-byte values
// (terminal/device/event ids that recur constantly on one link) to 2-byte
// indexes. DEFLATE cannot touch random 32-byte ids; this table can — a warm
// link shaves ~90–120 B off every frame.
//
// Correctness over a lossy AckNone link (review correction 1): the table
// carries a GENERATION. Definitions ride IN-BAND (idx → 32B), are idempotent
// and re-announced; short indexes are used only after the receiver has
// TABLE_ACKed the generation (on pure-AckNone links the sender front-loads
// redundant definitions until warm). A receiver reset bumps its generation;
// the sender detects the mismatch and re-defines. An undefined index makes
// the receiver drop the packet — the normal retry heals it.
//
// This layer sits INSIDE the compact body (flag bit), so it composes with
// deflate and the stateless framing; byte-exact reconstruction is preserved
// (the id substitution is fully reversible).
package compact

import (
	"encoding/binary"
	"errors"
	"sync"
)

const (
	// flagTable marks a compact body that used id-table substitution.
	flagTable = 0x02

	// tableMaxEntries bounds a link table (LRU beyond it).
	tableMaxEntries = 4096
)

// idKey is a 32-byte value tracked by the table.
type idKey [32]byte

// linkTable is one direction's sender/receiver state for a link.
type linkTable struct {
	mu sync.Mutex

	generation uint16
	// send side: value → index, and whether the receiver has confirmed it.
	sendIdx map[idKey]uint16
	defined map[uint16]bool // indexes the receiver has ACKed at this gen
	nextIdx uint16
	warm    bool // receiver TABLE_ACKed our generation

	// receive side: index → value, keyed by the sender's generation.
	recvGen uint16
	recvIdx map[uint16]idKey
}

// firstIdx starts at 256 so every index's HIGH byte is non-zero — the
// token 0x00‖idx can then never collide with the escaped literal 0x00 0x00.
const firstIdx = 256

func newLinkTable() *linkTable {
	return &linkTable{
		generation: 1,
		sendIdx:    map[idKey]uint16{},
		defined:    map[uint16]bool{},
		nextIdx:    firstIdx,
		recvGen:    0,
		recvIdx:    map[uint16]idKey{},
	}
}

// intern returns the index for a value, allocating one if new. The bool
// reports whether a definition (idx → value) must ride with this packet.
func (t *linkTable) intern(v idKey) (uint16, bool) {
	if idx, ok := t.sendIdx[v]; ok {
		// Re-announce until the receiver has confirmed the generation, so
		// AckNone links warm up without a lost definition stranding an idx.
		return idx, !t.warm || !t.defined[idx]
	}
	if len(t.sendIdx) >= tableMaxEntries {
		// Simple wrap: a fresh generation resets both sides deterministically.
		t.generation++
		t.sendIdx = map[idKey]uint16{}
		t.defined = map[uint16]bool{}
		t.nextIdx = firstIdx
		t.warm = false
	}
	idx := t.nextIdx
	t.nextIdx++
	t.sendIdx[v] = idx
	return idx, true
}

// ackGeneration marks the sender warm for gen (receiver confirmed it):
// every currently-interned index is now known to the far side.
func (t *linkTable) ackGeneration(gen uint16) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if gen == t.generation {
		t.warm = true
		for _, idx := range t.sendIdx {
			t.defined[idx] = true
		}
	}
}

// The compact body table section is:
//   gen:u16 ‖ nDefs:u16 ‖ (idx:u16 ‖ 32B)*nDefs ‖ substituted-payload
// where substituted-payload replaces each occurrence of a 32-byte run that
// matches a known id with a 3-byte token 0x00 ‖ idx:u16. A literal 0x00 in
// the payload is escaped 0x00 0x00 (idx 0 is never allocated).

func appendU16(b []byte, v uint16) []byte { return binary.BigEndian.AppendUint16(b, v) }

// encodeWithTable substitutes ids in pkt and prefixes definitions. It only
// substitutes when a value is already interned-and-defined OR being defined
// in this packet. ids come from the caller (the frame's header ids).
func (t *linkTable) encode(pkt []byte, ids []idKey) []byte {
	t.mu.Lock()
	defer t.mu.Unlock()

	defs := []byte{}
	nDefs := 0
	tok := map[idKey]uint16{}
	for _, v := range ids {
		idx, needDef := t.intern(v)
		tok[v] = idx
		if needDef {
			defs = appendU16(defs, idx)
			defs = append(defs, v[:]...)
			nDefs++
		}
	}

	// Substitute 32-byte windows that equal a known id with 0x00‖idx.
	var body []byte
	i := 0
	for i < len(pkt) {
		if i+32 <= len(pkt) {
			var w idKey
			copy(w[:], pkt[i:i+32])
			if idx, ok := tok[w]; ok {
				body = append(body, 0x00)
				body = appendU16(body, idx)
				i += 32
				continue
			}
		}
		if pkt[i] == 0x00 {
			body = append(body, 0x00, 0x00) // escape a literal 0x00
			i++
			continue
		}
		body = append(body, pkt[i])
		i++
	}

	out := appendU16(nil, t.generation)
	out = appendU16(out, uint16(nDefs))
	out = append(out, defs...)
	return append(out, body...)
}

// decode reverses encode, returning the reconstructed payload and the
// sender's generation (for TABLE_ACK). A generation change resets the
// receive map; an undefined index errors (the caller drops the packet —
// retry heals it).
func (t *linkTable) decode(sec []byte) ([]byte, uint16, error) {
	if len(sec) < 4 {
		return nil, 0, errors.New("compact: short table section")
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	gen := binary.BigEndian.Uint16(sec[0:2])
	nDefs := int(binary.BigEndian.Uint16(sec[2:4]))
	off := 4
	if gen != t.recvGen {
		t.recvGen = gen
		t.recvIdx = map[uint16]idKey{}
	}
	for d := 0; d < nDefs; d++ {
		if off+34 > len(sec) {
			return nil, 0, errors.New("compact: truncated definition")
		}
		idx := binary.BigEndian.Uint16(sec[off : off+2])
		var v idKey
		copy(v[:], sec[off+2:off+34])
		t.recvIdx[idx] = v
		off += 34
	}

	var out []byte
	body := sec[off:]
	i := 0
	for i < len(body) {
		if body[i] != 0x00 {
			out = append(out, body[i])
			i++
			continue
		}
		if i+1 >= len(body) {
			return nil, 0, errors.New("compact: dangling token")
		}
		if body[i+1] == 0x00 {
			out = append(out, 0x00) // unescape
			i += 2
			continue
		}
		if i+3 > len(body) {
			return nil, 0, errors.New("compact: truncated token")
		}
		idx := binary.BigEndian.Uint16(body[i+1 : i+3])
		v, ok := t.recvIdx[idx]
		if !ok {
			return nil, 0, errors.New("compact: undefined id index (retry heals)")
		}
		out = append(out, v[:]...)
		i += 3
	}
	return out, gen, nil
}
