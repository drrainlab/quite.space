// Exported fragment grammar (TN-2A): there is exactly ONE wire fragment
// format — the sync engine and any transport-layer wrapper (the compact
// profile) speak it identically, so a receiver needs one reassembly
// vocabulary regardless of who fragmented.
package sync

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

// FragmentStream splits msg into fragment packets for the given MTU under
// the canonical grammar. mtu<=0 yields a single unfragmented packet.
func FragmentStream(stream uint64, msg []byte, mtu int) ([][]byte, error) {
	if mtu <= 0 {
		pkt := fragHeader(stream, 0, 1)
		pkt = codec.AppendUint(pkt, fragKeyChunk)
		pkt = codec.AppendBytes(pkt, msg)
		return [][]byte{pkt}, nil
	}
	if mtu < minMTU {
		return nil, fmt.Errorf("sync: MTU %d below minimum %d", mtu, minMTU)
	}
	overhead := len(fragHeader(stream, 0, 1)) + 4
	chunkSize := mtu - overhead
	total := (len(msg) + chunkSize - 1) / chunkSize
	var out [][]byte
	for i := 0; i < total; i++ {
		start := i * chunkSize
		end := min(start+chunkSize, len(msg))
		pkt := fragHeader(stream, i, total)
		pkt = codec.AppendUint(pkt, fragKeyChunk)
		pkt = codec.AppendBytes(pkt, msg[start:end])
		out = append(out, pkt)
	}
	return out, nil
}

// ErrNotFragment reports input that is not a fragment packet at all — the
// caller may treat it as an already-whole message (a compact wrapper
// delivers reassembled messages upward as-is).
var ErrNotFragment = errors.New("sync: not a fragment packet")

// Reassembler collects fragment packets into whole messages. Duplicate
// fragments are no-ops; holes error only on completion checks.
type Reassembler struct {
	reasm map[uint64]*reassembly
}

// NewReassembler creates an empty reassembler.
func NewReassembler() *Reassembler {
	return &Reassembler{reasm: map[uint64]*reassembly{}}
}

// Feed consumes one fragment packet; returns the whole message when the
// stream completes, nil while incomplete.
func (r *Reassembler) Feed(pkt []byte) ([]byte, error) {
	d := codec.NewDecoder(pkt)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, ErrNotFragment
	}
	var stream, index, total uint64
	var chunk []byte
	for {
		k, ok, er := m.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case fragKeyStream:
			stream, er = d.ReadUint()
		case fragKeyIndex:
			index, er = d.ReadUint()
		case fragKeyTotal:
			total, er = d.ReadUint()
		case fragKeyChunk:
			chunk, er = d.ReadBytes()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if total == 0 && chunk == nil && stream == 0 {
		return nil, ErrNotFragment // a whole message, not a fragment
	}
	if total == 0 || index >= total || chunk == nil {
		return nil, errors.New("sync: malformed fragment")
	}
	if total == 1 {
		return append([]byte(nil), chunk...), nil
	}
	rs, ok := r.reasm[stream]
	if !ok {
		rs = &reassembly{total: int(total), chunks: map[int][]byte{}}
		r.reasm[stream] = rs
	}
	if _, dup := rs.chunks[int(index)]; !dup {
		rs.chunks[int(index)] = append([]byte(nil), chunk...)
	}
	if len(rs.chunks) < rs.total {
		return nil, nil
	}
	delete(r.reasm, stream)
	var msg []byte
	for i := 0; i < rs.total; i++ {
		part, ok := rs.chunks[i]
		if !ok {
			return nil, errors.New("sync: reassembly hole")
		}
		msg = append(msg, part...)
	}
	return msg, nil
}

// streamSeq allocates fragment stream ids: a random starting point drawn
// once per process, then monotonic.
//
// A stream id names a reassembly in progress on a link, and TWO things
// share links. Inside a process, several terminals sync over one wire, each
// with its own engine — per-engine counters all started at zero, so two
// engines would hand a receiver two different messages under one id.
// ACROSS processes it is worse: a broadcast radio segment delivers every
// node's fragments to every other node's reassembler, and nothing
// coordinates numbering between machines. Counters starting at zero
// everywhere made collision the DEFAULT for the first messages after boot,
// on exactly the medium where messages are large enough to fragment.
//
// A random base makes both cases vanishingly unlikely without a wire
// change: collision needs two processes' [base, base+sent) ranges to
// overlap in a 2^64 space.
//
// What this does NOT do is stop a deliberate attack: anyone on the segment
// can transmit a chosen stream id and corrupt a reassembly in progress.
// Source-scoping the key would not fix that either, because a Meshtastic
// source address is unauthenticated. The damage is bounded by what
// reassembly feeds: a spliced buffer fails to decode, or decodes to an
// envelope whose signature does not verify. Corruption is a denial vector,
// never a forgery one.
var streamSeq atomic.Uint64

// processStreamBase is the base drawn at startup, kept so a test can assert
// it is not a constant.
var processStreamBase uint64

func init() {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("sync: no entropy for fragment stream ids: " + err.Error())
	}
	processStreamBase = binary.BigEndian.Uint64(b[:])
	streamSeq.Store(processStreamBase)
}

// NextStreamID returns a fragment stream id that will not collide with
// another process's, nor with another engine's in this one.
func NextStreamID() uint64 { return streamSeq.Add(1) }
