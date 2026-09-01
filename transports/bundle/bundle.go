// Package bundle is the T1 transport (plan §19): export a terminal's frames
// to a *.terminal-bundle file that travels by flash drive, QR, email, or any
// other channel.
//
// Honesty note: v0 bundles carry the signed frames as-is. Payload secrecy is
// exactly the payload's own encryption state (ADR-005 arrives in M1); the
// bundle container adds integrity (per-frame signatures) but no additional
// confidentiality yet, and nothing here claims otherwise.
package bundle

import (
	"errors"
	"fmt"
	"os"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// magic identifies bundle files; version gates format changes.
const (
	magic   = "QPB0"
	version = 0
)

// Bundle key table v0 (append-only, ADR-009: old decoders skip unknown keys).
const (
	keyVersion  = 1
	keyTerminal = 2
	keyFrames   = 3
	keyBlobs    = 4 // encrypted asset blobs (manifests and chunks)
	keyWants    = 5 // requested blob hashes (relay media fetch)
	keyWanter   = 6 // requester device id — where to send the response
	keyReplyBox = 7 // PH-1: the mailbox HINT to answer into (see below)
	// keyReturnRoutes: where the WANTER states it can be answered — its own
	// ingress endpoints, carried beside the wants they belong to. This is a
	// STATED route (RT-0): a claim in the bundle body, attributed to the
	// wanter the same way the wants themselves are — never derived from
	// whichever connection the bundle happened to arrive through. It is
	// the weak, unsigned form of T3's signed advertisements and will be
	// retired by them. Old decoders skip it (append-only, ADR-009).
	keyReturnRoutes = 8
	// keyReceipts: DR-1 delivery receipts riding beside (or instead of)
	// frames — each entry an opaque signed statement "device D holds
	// author A's chain in this space up to position P". Opaque HERE on
	// purpose (ADR-007): the node layer signs and verifies; the transport
	// only carries. Old decoders skip the key (ADR-009).
	keyReceipts = 9
)

// Encode serializes frames for one terminal (same bytes as the file form —
// bundles travel identically by file, QR, or relay).
func Encode(terminal id.TerminalID, frames [][]byte) []byte {
	return EncodeWithBlobs(terminal, frames, nil)
}

// EncodeWithBlobs additionally carries encrypted asset blobs — the offline
// path for media. A blob is opaque here; possession proves nothing about
// access (keys travel inside the epoch-encrypted block events).
func EncodeWithBlobs(terminal id.TerminalID, frames [][]byte, blobs [][]byte) []byte {
	return EncodeWithWants(terminal, frames, blobs, nil, nil)
}

// EncodeWithWants is the full form: frames, blobs, and an optional relay media
// request. wants lists blob hashes the sender is missing; wanter is the
// sender's device id, so a holder knows which inbox to push the response to.
// Both empty = a plain frames/blobs bundle (byte-identical to EncodeWithBlobs).
func EncodeWithWants(terminal id.TerminalID, frames, blobs, wants [][]byte, wanter []byte) []byte {
	return EncodeWithReplyBox(terminal, frames, blobs, wants, wanter, nil)
}

// EncodeWithReplyBox adds PH-1's reply box: the HINT of a mailbox the
// requester alone can drain. A holder needs only the address to deliver an
// answer, and cannot use it to take one — which is what stops a stranger
// (or the relay) from intercepting media in flight.
//
// It also carries less than the device id it replaces: a bundle sitting in a
// publicly fetchable ingress shard used to name the reader who asked, to
// anyone who cared to look. A per-request random address names nobody.
func EncodeWithReplyBox(terminal id.TerminalID, frames, blobs, wants [][]byte, wanter, replyBox []byte) []byte {
	return EncodeWithReturnRoutes(terminal, frames, blobs, wants, wanter, replyBox, nil)
}

// EncodeWithReturnRoutes is the full form: the sender's own stated
// ingress, riding beside wants OR beside plain frames. The first wire
// rule here was "routes only beside wants — a bundle that asks for
// nothing has no answer to route", and it was too narrow by exactly one
// beta evening: a device that moves relays, or joins before its ingress
// exists, must be able to state where it lives the next time it SAYS
// anything — not the next time it happens to want a file. The statement
// needs an author (wanter names the stating device, cert-gated at the
// receiver); without one the routes still travel and are ignored there.
func EncodeWithReturnRoutes(terminal id.TerminalID, frames, blobs, wants [][]byte, wanter, replyBox []byte, returnRoutes []string) []byte {
	n := 3
	if len(blobs) > 0 {
		n++
	}
	if len(wants) > 0 {
		n++
	}
	if len(wanter) > 0 {
		n++
	}
	if len(replyBox) > 0 {
		n++
	}
	if len(returnRoutes) > 0 {
		n++
	}
	buf := []byte(magic)
	buf = codec.AppendMap(buf, n)
	buf = codec.AppendUint(buf, keyVersion)
	buf = codec.AppendUint(buf, version)
	buf = codec.AppendUint(buf, keyTerminal)
	buf = codec.AppendBytes(buf, terminal[:])
	buf = codec.AppendUint(buf, keyFrames)
	buf = codec.AppendArray(buf, len(frames))
	for _, f := range frames {
		buf = codec.AppendBytes(buf, f)
	}
	if len(blobs) > 0 {
		buf = codec.AppendUint(buf, keyBlobs)
		buf = codec.AppendArray(buf, len(blobs))
		for _, b := range blobs {
			buf = codec.AppendBytes(buf, b)
		}
	}
	if len(wants) > 0 {
		buf = codec.AppendUint(buf, keyWants)
		buf = codec.AppendArray(buf, len(wants))
		for _, h := range wants {
			buf = codec.AppendBytes(buf, h)
		}
	}
	if len(wanter) > 0 {
		buf = codec.AppendUint(buf, keyWanter)
		buf = codec.AppendBytes(buf, wanter)
	}
	if len(replyBox) > 0 {
		buf = codec.AppendUint(buf, keyReplyBox)
		buf = codec.AppendBytes(buf, replyBox)
	}
	if len(returnRoutes) > 0 {
		buf = codec.AppendUint(buf, keyReturnRoutes)
		buf = codec.AppendArray(buf, len(returnRoutes))
		for _, ep := range returnRoutes {
			buf = codec.AppendText(buf, ep)
		}
	}
	return buf
}

// Write exports frames for one terminal to a file.
func Write(path string, terminal id.TerminalID, frames [][]byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, Encode(terminal, frames), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Read imports a bundle file.
func Read(path string) (id.TerminalID, [][]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return id.TerminalID{}, nil, err
	}
	return Decode(data)
}

// Decode parses bundle bytes, returning the terminal and its frames.
func Decode(data []byte) (id.TerminalID, [][]byte, error) {
	tid, frames, _, err := DecodeFull(data)
	return tid, frames, err
}

// Parts is the fully decoded content of a bundle. Wants/Wanter are set only on
// a relay media request (see EncodeWithWants); they are empty otherwise.
// EncodeReceipts is the receipt-only bundle (DR-1): no frames, just the
// signed statements. It still carries an EMPTY frames array so that every
// decoder — including pre-DR-1 ones — sees the shape it expects and
// applies zero frames rather than refusing the item.
func EncodeReceipts(terminal id.TerminalID, receipts [][]byte) []byte {
	buf := []byte(magic)
	buf = codec.AppendMap(buf, 4)
	buf = codec.AppendUint(buf, keyVersion)
	buf = codec.AppendUint(buf, version)
	buf = codec.AppendUint(buf, keyTerminal)
	buf = codec.AppendBytes(buf, terminal[:])
	buf = codec.AppendUint(buf, keyFrames)
	buf = codec.AppendArray(buf, 0)
	buf = codec.AppendUint(buf, keyReceipts)
	buf = codec.AppendArray(buf, len(receipts))
	for _, rc := range receipts {
		buf = codec.AppendBytes(buf, rc)
	}
	return buf
}

type Parts struct {
	Terminal id.TerminalID
	Frames   [][]byte
	Blobs    [][]byte
	Wants    [][]byte
	Wanter   []byte
	// ReplyBox is the mailbox hint to answer into (PH-1). When set, a holder
	// MUST use it instead of deriving an inbox from Wanter.
	ReplyBox []byte
	// ReturnRoutes: the wanter's own stated ingress endpoints, riding
	// beside its wants. A claim by the wanter, never a fact about the
	// connection — see keyReturnRoutes.
	ReturnRoutes []string
	// Receipts: opaque signed delivery statements (DR-1, keyReceipts).
	Receipts [][]byte
}

// DecodeFull returns the terminal, frames, and carried asset blobs. Frames and
// blobs are opaque here — validation happens in the event log and the asset
// layer, not in the transport (ADR-007).
func DecodeFull(data []byte) (id.TerminalID, [][]byte, [][]byte, error) {
	p, err := DecodeParts(data)
	return p.Terminal, p.Frames, p.Blobs, err
}

// DecodeParts returns everything a bundle carries, including a relay media
// request if present.
func DecodeParts(data []byte) (Parts, error) {
	var p Parts
	if len(data) < len(magic) || string(data[:len(magic)]) != magic {
		return p, errors.New("bundle: not a terminal-bundle file")
	}
	d := codec.NewDecoder(data[len(magic):])
	m, err := d.ReadMapHeader()
	if err != nil {
		return p, err
	}
	var seenTerminal bool
	for {
		k, ok, err := m.Next()
		if err != nil {
			return p, err
		}
		if !ok {
			break
		}
		switch k {
		case keyVersion:
			v, e := d.ReadUint()
			if e != nil {
				return p, e
			}
			if v != version {
				return p, fmt.Errorf("bundle: unsupported version %d", v)
			}
		case keyTerminal:
			b, e := d.ReadBytes()
			if e != nil {
				return p, e
			}
			if len(b) != id.Size {
				return p, errors.New("bundle: bad terminal id")
			}
			copy(p.Terminal[:], b)
			seenTerminal = true
		case keyFrames:
			n, e := d.ReadArray()
			if e != nil {
				return p, e
			}
			for range n {
				f, e := d.ReadBytes()
				if e != nil {
					return p, e
				}
				p.Frames = append(p.Frames, append([]byte(nil), f...))
			}
		case keyBlobs:
			n, e := d.ReadArray()
			if e != nil {
				return p, e
			}
			for range n {
				b, e := d.ReadBytes()
				if e != nil {
					return p, e
				}
				p.Blobs = append(p.Blobs, append([]byte(nil), b...))
			}
		case keyWants:
			n, e := d.ReadArray()
			if e != nil {
				return p, e
			}
			for range n {
				h, e := d.ReadBytes()
				if e != nil {
					return p, e
				}
				p.Wants = append(p.Wants, append([]byte(nil), h...))
			}
		case keyWanter:
			b, e := d.ReadBytes()
			if e != nil {
				return p, e
			}
			p.Wanter = append([]byte(nil), b...)
		case keyReplyBox:
			b, e := d.ReadBytes()
			if e != nil {
				return p, e
			}
			p.ReplyBox = append([]byte(nil), b...)
		case keyReturnRoutes:
			n, e := d.ReadArray()
			if e != nil {
				return p, e
			}
			for range n {
				ep, e := d.ReadText()
				if e != nil {
					return p, e
				}
				p.ReturnRoutes = append(p.ReturnRoutes, ep)
			}
		case keyReceipts:
			n, e := d.ReadArray()
			if e != nil {
				return p, e
			}
			for range n {
				rc, e := d.ReadBytes()
				if e != nil {
					return p, e
				}
				p.Receipts = append(p.Receipts, append([]byte(nil), rc...))
			}
		default:
			if err := d.SkipItem(); err != nil {
				return p, err
			}
		}
	}
	if err := d.Done(); err != nil {
		return p, err
	}
	if !seenTerminal {
		return p, errors.New("bundle: missing terminal id")
	}
	return p, nil
}
