// Package compact is the TN-2A stateless radio wire profile: a REVERSIBLE
// transport encoding around opaque packets — DEFLATE when it wins, nothing
// stateful, byte-exact reconstruction always (the signature covers the
// canonical frame bytes; this layer may wrap them, never rewrite them).
//
// Wire discrimination: every raw sync packet begins with a CBOR map header
// (0xA0..0xBF under our codec); compact packets begin with the magic byte
// 0xC7 + version — the two grammars are disjoint by the first byte, and a
// raw-only receiver fails CLOSED on compact input (no silent misparse).
// Automatic negotiation/fallback is TN-2B; in 2A the operator opts a real
// radio link in with --compact.
//
// Framing: [0xC7, version=1, flags] ‖ body. flags bit0 = deflate. A packet
// larger than the inner MTU is sub-fragmented with the SAME fragment
// grammar the sync engine uses (kernel/sync.FragmentStream) — one wire
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

// Wrap builds the compact endpoint around a radio-scale inner endpoint.
func Wrap(inner transports.Endpoint) transports.Endpoint {
	return &wrapped{inner: inner, reasm: kernelsync.NewReassembler()}
}

type wrapped struct {
	inner      transports.Endpoint
	reasm      *kernelsync.Reassembler
	nextStream uint64
}

func (w *wrapped) Capabilities() transports.Capabilities {
	c := w.inner.Capabilities()
	if c.MaxPayload == 0 || c.MaxPayload > effectiveMTU {
		c.MaxPayload = effectiveMTU
	} else {
		c.MaxPayload = effectiveMTU // sync sizes to us; we size to the radio
	}
	return c
}

// encode wraps one packet into the compact framing (reversible).
func encode(pkt []byte) []byte {
	flags := byte(0)
	body := pkt
	var buf bytes.Buffer
	fw, _ := flate.NewWriter(&buf, flate.BestCompression)
	if _, err := fw.Write(pkt); err == nil {
		if err := fw.Close(); err == nil && buf.Len() < len(pkt) {
			flags |= flagDeflate
			body = buf.Bytes()
		}
	}
	out := make([]byte, 0, len(body)+3)
	out = append(out, Magic, Version, flags)
	return append(out, body...)
}

// decode reverses encode. Non-compact input errors (fail-closed).
func decode(pkt []byte) ([]byte, error) {
	if len(pkt) < 3 || pkt[0] != Magic {
		return nil, errors.New("compact: not a compact packet")
	}
	if pkt[1] != Version {
		return nil, errors.New("compact: unknown version")
	}
	flags, body := pkt[2], pkt[3:]
	if flags&flagDeflate == 0 {
		return append([]byte(nil), body...), nil
	}
	r := flate.NewReader(bytes.NewReader(body))
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (w *wrapped) Send(pkt []byte) error {
	enc := encode(pkt)
	mtu := w.inner.Capabilities().MaxPayload
	if mtu <= 0 || len(enc) <= mtu {
		return w.inner.Send(enc)
	}
	w.nextStream++
	frags, err := kernelsync.FragmentStream(w.nextStream, enc, mtu)
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

// Poll drains the inner endpoint: fragment packets reassemble at THIS
// layer (one grammar for everyone); completed or unfragmented packets
// then split by first byte — compact decodes, raw passes through verbatim
// (a compact peer always understands a raw peer).
func (w *wrapped) Poll() [][]byte {
	var out [][]byte
	deliver := func(pkt []byte) {
		if len(pkt) == 0 {
			return
		}
		if pkt[0] == Magic {
			if dec, err := decode(pkt); err == nil {
				out = append(out, dec)
			}
			return // fail-closed on malformed compact
		}
		out = append(out, pkt)
	}
	for _, pkt := range w.inner.Poll() {
		if len(pkt) > 0 && pkt[0] >= 0xA0 && pkt[0] <= 0xBF {
			// A fragment (or any raw map packet). Try reassembly: fragment
			// maps complete here; a non-fragment raw packet fails Feed and
			// passes through untouched.
			if msg, err := w.reasm.Feed(pkt); err == nil {
				if msg != nil {
					deliver(msg)
				}
				continue
			}
			deliver(pkt)
			continue
		}
		deliver(pkt)
	}
	return out
}
