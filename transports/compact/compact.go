// Package compact is the TN-2A stateless radio wire profile: a REVERSIBLE
// transport encoding around opaque packets — DEFLATE when it wins, nothing
// stateful, byte-exact reconstruction always (the signature covers the
// canonical frame bytes; this layer may wrap them, never rewrite them).
// TN-2B (table.go, stateful.go) adds a link-scoped id table on top.
//
// Wire discrimination: every raw sync packet begins with a CBOR map header
// (0xA0..0xBF under our codec); compact packets begin with the magic byte
// 0xC7 + version — the two grammars are disjoint by the first byte, and a
// raw-only receiver fails CLOSED on compact input (no silent misparse).
// Automatic negotiation/fallback is TN-2B; in 2A the operator opts a real
// radio link in with --compact.
//
// Framing: [0xC7, version=1, flags] ‖ body. flag bits: deflate, table,
// table-ack. A packet larger than the inner MTU is sub-fragmented with the
// SAME grammar the sync engine uses (kernel/sync.FragmentStream) — one wire
// vocabulary, reassembled at this layer regardless of who fragmented.
package compact

import (
	"bytes"
	"compress/flate"
	"errors"
	"io"

	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/transports"
)

const (
	// Magic is provably outside the raw grammar: our codec's packets start
	// with a map header 0xA0..0xBF; 0xC7 can never open a valid raw packet.
	Magic   = 0xC7
	Version = 1

	flagDeflate = 0x01

	// effectiveMTU is what the wrapper advertises upward: sync keeps its
	// small-message shrink logic while this layer owns radio-size splitting.
	effectiveMTU = 2048
)

func newReasm() *kernelsync.Reassembler { return kernelsync.NewReassembler() }

// Wrap builds the stateless compact endpoint around a radio-scale inner.
func Wrap(inner transports.Endpoint) transports.Endpoint {
	return &wrapped{inner: inner, reasm: newReasm()}
}

type wrapped struct {
	inner      transports.Endpoint
	reasm      *kernelsync.Reassembler
	nextStream uint64
}

func (w *wrapped) Capabilities() transports.Capabilities {
	c := w.inner.Capabilities()
	c.MaxPayload = effectiveMTU
	return c
}

// deflateIfSmaller returns the deflated bytes and true when smaller.
func deflateIfSmaller(b []byte) ([]byte, bool) {
	var buf bytes.Buffer
	fw, _ := flate.NewWriter(&buf, flate.BestCompression)
	if _, err := fw.Write(b); err != nil {
		return b, false
	}
	if err := fw.Close(); err != nil || buf.Len() >= len(b) {
		return b, false
	}
	return buf.Bytes(), true
}

func inflate(b []byte) ([]byte, error) {
	r := flate.NewReader(bytes.NewReader(b))
	defer r.Close()
	return io.ReadAll(r)
}

// encode wraps one packet into the stateless compact framing (reversible).
func encode(pkt []byte) []byte {
	flags := byte(0)
	body := pkt
	if def, ok := deflateIfSmaller(pkt); ok {
		flags |= flagDeflate
		body = def
	}
	return append([]byte{Magic, Version, flags}, body...)
}

// decode reverses stateless encode. Non-compact / table input errors.
func decode(pkt []byte) ([]byte, error) {
	if len(pkt) < 3 || pkt[0] != Magic {
		return nil, errors.New("compact: not a compact packet")
	}
	if pkt[1] != Version {
		return nil, errors.New("compact: unknown version")
	}
	flags, body := pkt[2], pkt[3:]
	if flags&(flagTable|flagTableAck) != 0 {
		return nil, errors.New("compact: table packet on a stateless endpoint")
	}
	if flags&flagDeflate == 0 {
		return append([]byte(nil), body...), nil
	}
	return inflate(body)
}

// sendFramed fragments a fully-framed compact packet to the inner MTU and
// sends it. Small packets go as one fragment.
func (w *wrapped) sendFramed(framed []byte) error {
	mtu := w.inner.Capabilities().MaxPayload
	if mtu <= 0 || len(framed) <= mtu {
		return w.inner.Send(framed)
	}
	w.nextStream++
	frags, err := kernelsync.FragmentStream(w.nextStream, framed, mtu)
	if err != nil {
		return err
	}
	for _, f := range frags {
		if err := w.inner.Send(f); err != nil {
			return err
		}
	}
	return nil
}

func (w *wrapped) Send(pkt []byte) error { return w.sendFramed(encode(pkt)) }

// drain polls the inner endpoint, reassembling fragments at THIS layer
// (one grammar for everyone) and returning whole messages — compact,
// table, ack, or raw — for the caller to dispatch by first byte.
func (w *wrapped) drain() [][]byte {
	var out [][]byte
	for _, pkt := range w.inner.Poll() {
		if len(pkt) > 0 && pkt[0] >= 0xA0 && pkt[0] <= 0xBF {
			if msg, err := w.reasm.Feed(pkt); err == nil {
				if msg != nil {
					out = append(out, msg)
				}
				continue
			}
			out = append(out, pkt)
			continue
		}
		out = append(out, pkt)
	}
	return out
}

// Poll delivers stateless-compact and raw messages; malformed compact fails
// closed. (The stateful endpoint overrides this to add the id table.)
func (w *wrapped) Poll() [][]byte {
	var out [][]byte
	for _, msg := range w.drain() {
		if len(msg) == 0 {
			continue
		}
		if msg[0] == Magic {
			if dec, err := decode(msg); err == nil {
				out = append(out, dec)
			}
			continue // fail-closed on malformed compact
		}
		out = append(out, msg)
	}
	return out
}
