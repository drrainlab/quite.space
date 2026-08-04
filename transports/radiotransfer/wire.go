// The Radio Transfer frame grammar.
//
// Five frame kinds, one encoding, deterministic CBOR through the project's
// own codec — the same grammar rules the rest of the tree follows: computed
// map arity, strictly ascending keys, unknown keys skipped rather than
// refused.
//
// Two things that a single "payload_crc" would have conflated are separate
// here and named separately, because they answer different questions:
//
//	fragment_crc     cheap per-fragment corruption detection, so a mangled
//	                 fragment is discarded rather than assembled
//	message_digest   proof that the REASSEMBLY is the message that was sent,
//	                 which no amount of per-fragment checking can establish
//
// Control frames carry a MAC. DATA payloads are already protected by Quiet's
// epoch keys, but SACK, COMMIT, CANCEL and REPAIR sit BELOW the envelope: on
// Meshtastic the channel PSK happens to cover them, and on RNode nothing
// would. A neighbour able to forge a CANCEL could stop any transfer on the
// segment at will. The MAC closes that without pretending to be
// confidentiality — see keys.go.
package radiotransfer

import (
	"errors"
	"fmt"
	"hash/crc32"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

// FrameKind is what a frame is for.
type FrameKind uint8

const (
	// KindData carries one fragment of a transfer.
	KindData FrameKind = 1
	// KindSACK reports which fragments of a window arrived, so the sender
	// resends the gaps and nothing else.
	KindSACK FrameKind = 2
	// KindCommit says the receiver reassembled the message AND its digest
	// matched. It means exactly that and nothing more: whether the EVENT is
	// then accepted stays the sync engine's decision, through its own
	// summary.
	KindCommit FrameKind = 3
	// KindCancel abandons a transfer.
	KindCancel FrameKind = 4
	// KindRepair asks for a transfer to be offered again from the start.
	KindRepair FrameKind = 5
	// KindPoll asks the receiver to say, right now, what it holds of a
	// transfer. One short frame, and the answer is a cumulative SACK — or a
	// complete-SACK from the tombstone when the transfer already assembled.
	//
	// It exists so that a lost answer costs a POLL and never a burst: before
	// this, a sender that heard nothing had exactly one move, resending
	// sixteen large DATA frames to provoke sixteen small answers.
	KindPoll FrameKind = 6
)

func (k FrameKind) String() string {
	switch k {
	case KindData:
		return "DATA"
	case KindSACK:
		return "SACK"
	case KindCommit:
		return "COMMIT"
	case KindCancel:
		return "CANCEL"
	case KindRepair:
		return "REPAIR"
	case KindPoll:
		return "POLL"
	}
	return fmt.Sprintf("UNKNOWN(%d)", uint8(k))
}

// Wire keys. Append-only: a key that has meant one thing must never mean
// another, and an unknown key is skipped so a newer sender can add one.
const (
	keyVersion  = 1
	keyKind     = 2
	keyTransfer = 3
	keyIndex    = 4
	keyCount    = 5
	keyTotal    = 6
	keyDigest   = 7
	keyCRC      = 8
	keyChunk    = 9
	keyBase     = 10
	keyBitmap   = 11
	keyReason   = 12
	keyStream   = 13
	keyGen      = 14
	keyDone     = 15
	keyEOB      = 16
)

// Streams separate kinds of traffic that must not be confused on one carrier.
//
// The sync engine's messages and a node's own control traffic — announcing a
// device, offering an invite — travel the same radio and must not be handed
// to each other's parsers. A stream tag is one varint, omitted at its default,
// so ordinary sync traffic costs nothing for the distinction.
const (
	// StreamSync is the default: whatever the layer above hands down.
	StreamSync uint64 = 0
	// StreamControl is the node's own traffic about the segment itself.
	StreamControl uint64 = 1
)

// ProtocolVersion is the grammar this build speaks. A frame announcing a
// different one is refused rather than guessed at: the whole point of the
// layer is that a receiver knows what it is holding.
const ProtocolVersion = 1

// Frame is one Radio Transfer frame, decoded.
type Frame struct {
	Version  uint64
	Kind     FrameKind
	Transfer TransferID

	// DATA
	Index uint64
	Count uint64 // fragments in the whole transfer
	Total uint64 // bytes in the whole message
	Chunk []byte
	CRC   uint32

	// DATA and COMMIT
	Digest [DigestLen]byte

	// SACK
	Base   uint64
	Bitmap []byte

	// CANCEL and REPAIR
	Reason uint64

	// Stream separates sync traffic from a node's own control traffic. It is
	// carried on DATA and echoed nowhere else: SACK and COMMIT name a
	// transfer, and a transfer already knows which stream it is.
	Stream uint64

	// Generation is which burst of a transfer this frame belongs to.
	//
	// A DATA frame carries the sender's current burst number; a SACK echoes
	// the highest one the receiver has HEARD. That one echo is what lets the
	// sender tell a report from a history: on real boards fourteen SACKs
	// arrived intact and every one described a window the sender had left a
	// minute earlier, and repairing against them pressed 94 duplicate frames
	// on a peer that already held the message.
	//
	// Zero means "not speaking generations" — an older build — and is why
	// this is an optional key rather than a version bump: an old reader
	// skips it (still MAC-covered), an old sender never writes it, and the
	// sender falls back to the old behaviour for a peer that never echoes
	// one.
	Generation uint64

	// Reassembled, on a SACK, is completion folded into the report: the
	// receiver has every fragment, has assembled the message, and its digest
	// matched — the same claim a COMMIT makes, carried by a frame that can
	// be REPEATED. A COMMIT is sent once and never again; measured live,
	// seventy-three of them were sent and one was heard, and every resend of
	// the last fragment bought only another unheard COMMIT. A duplicate DATA
	// frame now buys this instead, for as long as the tombstone lives.
	//
	// When set, the SACK also carries the message digest, so the claim is
	// checked exactly as a COMMIT's is.
	Reassembled bool

	// EOB, on a DATA frame, marks the end of a burst: the sender is about to
	// fall silent and listen. It is the receiver's cue to answer with ONE
	// cumulative SACK — the explicit turnaround a half-duplex link needs,
	// instead of the per-frame drip that reported one window twenty-nine
	// times.
	EOB bool
}

// DigestLen is how much of the message digest travels.
//
// Sixteen bytes, not thirty-two: this proves a reassembly is the message that
// was sent, against accident and against a neighbour splicing fragments — not
// against an adversary with the segment key, who can simply send their own
// message. Every byte here is airtime on a carrier where one packet costs two
// seconds, and the honest security claim for the CONTENT lives in the epoch
// signature inside it, not out here.
const DigestLen = 16

// MACLen is the truncated authentication tag on every frame.
const MACLen = 8

// Bounds. Each is a real memory-filling surface on a broadcast segment where
// every neighbour's frames reach every reassembler, so each is checked on
// DECODE, before anything is allocated.
const (
	// MaxFragments bounds one transfer. At a ~200-byte carrier MTU this is a
	// little over 12 KiB, which is far more than any Quiet message that has
	// any business crossing a LoRa link.
	MaxFragments = 64
	// MaxMessageBytes bounds the reassembled message.
	MaxMessageBytes = 64 << 10
	// MaxBitmapBytes bounds a SACK bitmap: one bit per fragment in a window.
	MaxBitmapBytes = MaxFragments / 8
)

var (
	// ErrNotTransfer is a frame that is not one of ours at all. It is not an
	// error worth reporting upward — on a shared channel, most traffic
	// belongs to somebody else.
	ErrNotTransfer = errors.New("radiotransfer: not a transfer frame")
	// ErrBadFrame is a frame that claims to be ours and is malformed.
	ErrBadFrame = errors.New("radiotransfer: malformed frame")
	// ErrVersion is a frame from a grammar this build does not speak.
	ErrVersion = errors.New("radiotransfer: unsupported protocol version")
)

// Encode renders a frame and appends its MAC.
//
// Arity is COMPUTED, never a literal. A hard-coded count that drifts from the
// fields actually written produces bytes that decode to the wrong thing or
// fail at the very end, and this project has had that bug before.
func (f *Frame) Encode(key *TransferKey) ([]byte, error) {
	if f.Version == 0 {
		f.Version = ProtocolVersion
	}
	type kv struct {
		k   uint64
		put func([]byte) []byte
	}
	var fields []kv
	add := func(k uint64, put func([]byte) []byte) { fields = append(fields, kv{k, put}) }

	add(keyVersion, func(b []byte) []byte { return codec.AppendUint(b, f.Version) })
	add(keyKind, func(b []byte) []byte { return codec.AppendUint(b, uint64(f.Kind)) })
	add(keyTransfer, func(b []byte) []byte { return codec.AppendBytes(b, f.Transfer[:]) })

	switch f.Kind {
	case KindData:
		if len(f.Chunk) == 0 {
			return nil, fmt.Errorf("%w: a DATA frame with no chunk", ErrBadFrame)
		}
		add(keyIndex, func(b []byte) []byte { return codec.AppendUint(b, f.Index) })
		add(keyCount, func(b []byte) []byte { return codec.AppendUint(b, f.Count) })
		add(keyTotal, func(b []byte) []byte { return codec.AppendUint(b, f.Total) })
		add(keyDigest, func(b []byte) []byte { return codec.AppendBytes(b, f.Digest[:]) })
		add(keyCRC, func(b []byte) []byte { return codec.AppendUint(b, uint64(ChunkCRC(f.Chunk))) })
		add(keyChunk, func(b []byte) []byte { return codec.AppendBytes(b, f.Chunk) })
		if f.Stream != StreamSync {
			add(keyStream, func(b []byte) []byte { return codec.AppendUint(b, f.Stream) })
		}
		if f.Generation != 0 {
			add(keyGen, func(b []byte) []byte { return codec.AppendUint(b, f.Generation) })
		}
		if f.EOB {
			add(keyEOB, func(b []byte) []byte { return codec.AppendUint(b, 1) })
		}
	case KindSACK:
		add(keyCount, func(b []byte) []byte { return codec.AppendUint(b, f.Count) })
		if f.Reassembled {
			add(keyDigest, func(b []byte) []byte { return codec.AppendBytes(b, f.Digest[:]) })
		}
		add(keyBase, func(b []byte) []byte { return codec.AppendUint(b, f.Base) })
		add(keyBitmap, func(b []byte) []byte { return codec.AppendBytes(b, f.Bitmap) })
		if f.Generation != 0 {
			add(keyGen, func(b []byte) []byte { return codec.AppendUint(b, f.Generation) })
		}
		if f.Reassembled {
			add(keyDone, func(b []byte) []byte { return codec.AppendUint(b, 1) })
		}
	case KindCommit:
		add(keyDigest, func(b []byte) []byte { return codec.AppendBytes(b, f.Digest[:]) })
	case KindCancel, KindRepair:
		add(keyReason, func(b []byte) []byte { return codec.AppendUint(b, f.Reason) })
	case KindPoll:
		if f.Generation != 0 {
			add(keyGen, func(b []byte) []byte { return codec.AppendUint(b, f.Generation) })
		}
	default:
		return nil, fmt.Errorf("%w: kind %s cannot be encoded by this build",
			ErrBadFrame, f.Kind)
	}

	// The MAC is a fixed-size TRAILER after the map, not a key inside it.
	//
	// Inside, it would have to be the last key to be verifiable without
	// re-encoding — and "last" is exactly what a future version adding a
	// higher-numbered key would quietly break, turning forward compatibility
	// into an authentication failure. Outside, the split is unambiguous at
	// every version: the map is everything but the final MACLen bytes, and
	// unknown keys inside it stay covered.
	body := codec.AppendMap(nil, len(fields))
	for _, f := range fields {
		body = codec.AppendUint(body, f.k)
		body = f.put(body)
	}
	return append(body, key.Tag(body)...), nil
}

// Decode reads a frame and checks its MAC.
//
// A frame is refused, in this order: not a map at all, a version we do not
// speak, a bound exceeded, a MAC that does not verify. The order matters —
// bounds are checked before anything is copied out, so a hostile neighbour
// cannot make this allocate by lying about a length.
func Decode(b []byte, key *TransferKey) (*Frame, error) {
	if len(b) <= MACLen {
		return nil, ErrNotTransfer
	}
	body, mac := b[:len(b)-MACLen], b[len(b)-MACLen:]
	d := codec.NewDecoder(body)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, ErrNotTransfer
	}
	f := &Frame{}
	seen := map[uint64]bool{}
	for {
		k, ok, er := m.Next()
		if er != nil {
			return nil, ErrNotTransfer
		}
		if !ok {
			break
		}
		if seen[k] {
			return nil, fmt.Errorf("%w: key %d twice", ErrBadFrame, k)
		}
		seen[k] = true
		switch k {
		case keyVersion:
			f.Version, er = d.ReadUint()
		case keyKind:
			var v uint64
			v, er = d.ReadUint()
			f.Kind = FrameKind(v)
		case keyTransfer:
			var raw []byte
			raw, er = d.ReadBytes()
			if er == nil {
				if len(raw) != TransferIDLen {
					return nil, fmt.Errorf("%w: transfer id is %d bytes, want %d",
						ErrBadFrame, len(raw), TransferIDLen)
				}
				copy(f.Transfer[:], raw)
			}
		case keyIndex:
			f.Index, er = d.ReadUint()
		case keyCount:
			f.Count, er = d.ReadUint()
		case keyTotal:
			f.Total, er = d.ReadUint()
		case keyDigest:
			var raw []byte
			raw, er = d.ReadBytes()
			if er == nil {
				if len(raw) != DigestLen {
					return nil, fmt.Errorf("%w: digest is %d bytes, want %d",
						ErrBadFrame, len(raw), DigestLen)
				}
				copy(f.Digest[:], raw)
			}
		case keyCRC:
			var v uint64
			v, er = d.ReadUint()
			f.CRC = uint32(v)
		case keyChunk:
			f.Chunk, er = d.ReadBytes()
		case keyBase:
			f.Base, er = d.ReadUint()
		case keyBitmap:
			f.Bitmap, er = d.ReadBytes()
			if er == nil && len(f.Bitmap) > MaxBitmapBytes {
				return nil, fmt.Errorf("%w: bitmap of %d bytes, cap is %d",
					ErrBadFrame, len(f.Bitmap), MaxBitmapBytes)
			}
		case keyReason:
			f.Reason, er = d.ReadUint()
		case keyStream:
			f.Stream, er = d.ReadUint()
		case keyGen:
			f.Generation, er = d.ReadUint()
		case keyDone:
			var v uint64
			v, er = d.ReadUint()
			f.Reassembled = v != 0
		case keyEOB:
			var v uint64
			v, er = d.ReadUint()
			f.EOB = v != 0
		default:
			// Unknown key: skipped, so a newer sender can add one without
			// this build refusing the frame. It is still covered by the MAC,
			// which is what stops "skipped" from meaning "unprotected".
			er = d.SkipItem()
		}
		if er != nil {
			return nil, fmt.Errorf("%w: key %d: %v", ErrBadFrame, k, er)
		}
	}
	if err := d.Done(); err != nil {
		return nil, fmt.Errorf("%w: trailing bytes", ErrBadFrame)
	}
	if f.Version != ProtocolVersion {
		return nil, fmt.Errorf("%w: %d, this build speaks %d",
			ErrVersion, f.Version, ProtocolVersion)
	}
	if !key.Verify(body, mac) {
		return nil, fmt.Errorf("%w: the tag does not verify — this frame was "+
			"not written by anyone holding this segment's key", ErrBadFrame)
	}
	if err := f.check(); err != nil {
		return nil, err
	}
	return f, nil
}

// check enforces the bounds that make a frame usable, after it is known to be
// authentic. Doing this before the MAC would let a stranger choose which
// error a node spends time on.
func (f *Frame) check() error {
	switch f.Kind {
	case KindData:
		switch {
		case f.Count == 0 || f.Count > MaxFragments:
			return fmt.Errorf("%w: %d fragments, cap is %d", ErrBadFrame,
				f.Count, MaxFragments)
		case f.Index >= f.Count:
			return fmt.Errorf("%w: fragment %d of %d", ErrBadFrame, f.Index, f.Count)
		case f.Total == 0 || f.Total > MaxMessageBytes:
			return fmt.Errorf("%w: message of %d bytes, cap is %d", ErrBadFrame,
				f.Total, MaxMessageBytes)
		case len(f.Chunk) == 0:
			return fmt.Errorf("%w: DATA with no chunk", ErrBadFrame)
		case uint64(len(f.Chunk)) > f.Total:
			return fmt.Errorf("%w: a fragment larger than the whole message",
				ErrBadFrame)
		case ChunkCRC(f.Chunk) != f.CRC:
			// Corrupt rather than forged: the MAC already verified, so this
			// is the carrier mangling bytes under it. Cheaper to catch here
			// than to discover at the digest, after the airtime is spent.
			return fmt.Errorf("%w: fragment %d failed its checksum", ErrBadFrame, f.Index)
		}
	case KindSACK:
		if f.Count == 0 || f.Count > MaxFragments {
			return fmt.Errorf("%w: SACK over %d fragments", ErrBadFrame, f.Count)
		}
		if f.Base >= f.Count {
			return fmt.Errorf("%w: SACK base %d of %d", ErrBadFrame, f.Base, f.Count)
		}
	case KindCommit, KindCancel, KindRepair, KindPoll:
		// Nothing further: the transfer id and the MAC are the whole content.
	default:
		return fmt.Errorf("%w: kind %d", ErrBadFrame, f.Kind)
	}
	return nil
}

// ChunkCRC is the per-fragment corruption check. CRC-32C, because it is what
// hardware and every other implementation already computes.
func ChunkCRC(chunk []byte) uint32 {
	return crc32.Checksum(chunk, crc32.MakeTable(crc32.Castagnoli))
}

// SetBit and HasBit read a SACK bitmap, where bit i means "fragment base+i
// arrived".
func SetBit(bitmap []byte, i int) {
	if i/8 < len(bitmap) {
		bitmap[i/8] |= 1 << uint(i%8)
	}
}

func HasBit(bitmap []byte, i int) bool {
	if i/8 >= len(bitmap) {
		return false
	}
	return bitmap[i/8]&(1<<uint(i%8)) != 0
}
