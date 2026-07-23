// TN-2B stateful compact endpoint: id-table substitution on top of the
// stateless framing, plus the narrow link-local negotiation (a receiver
// TABLE_ACKs the generation whose ids it has learned; the sender only
// relies on short indexes once warm). This is NOT general capability
// discovery — just "which compact table generation is this link on".
package compact

import (
	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/transports"
)

// tableAck is a tiny control packet: [Magic, Version, flags=0x04, gen:2].
const flagTableAck = 0x04

// WrapStateful adds id-table compaction to the compact profile. Every peer
// on the link must run it; a stateless-compact or raw peer would not learn
// the table (mode=auto negotiation lands the version handshake in the
// daemon layer — here both sides are configured compatibly).
func WrapStateful(inner transports.Endpoint) transports.Endpoint {
	return &statefulWrap{
		wrapped: wrapped{inner: inner, reasm: newReasm()},
		table:   newLinkTable(),
	}
}

type statefulWrap struct {
	wrapped
	table *linkTable
}

// idsOf extracts the 32-byte header values worth interning: terminal,
// principal, device, previous, source-terminal. The signature (64B) and
// payload are left to deflate. Header decode never touches the payload.
func idsOf(frame []byte) []idKey {
	env, err := signal.Decode(frame)
	if err != nil {
		return nil
	}
	push := func(out []idKey, b []byte) []idKey {
		var k idKey
		copy(k[:], b)
		return append(out, k)
	}
	var ids []idKey
	ids = push(ids, env.Terminal[:])
	ids = push(ids, env.Principal[:])
	ids = push(ids, env.Device[:])
	if env.Previous != nil {
		ids = push(ids, env.Previous[:])
	}
	if env.SourceTerminal != nil {
		ids = push(ids, env.SourceTerminal[:])
	}
	return ids
}

// The stateful body is: [Magic, Version, flags|flagTable] ‖ tableSection.
// Only signed frames (msgFrames content) carry ids worth a table; sync
// control messages (summaries) pass through the stateless path. We probe
// by attempting a header decode of the WHOLE packet — a frames-carrying
// sync message won't decode as an envelope, so the table only kicks in for
// direct frame packets. To keep this simple and correct, the stateful
// layer substitutes ids across the ENTIRE packet bytes: any 32-byte window
// equal to a known id compacts, regardless of message shape.
func (w *statefulWrap) Send(pkt []byte) error {
	ids := scanIDs(pkt)
	if len(ids) == 0 {
		return w.wrapped.Send(pkt) // nothing to intern → stateless path
	}
	section := w.table.encode(pkt, ids)
	flags := byte(flagTable)
	body := section
	if def, ok := deflateIfSmaller(section); ok {
		flags |= flagDeflate
		body = def
	}
	framed := append([]byte{Magic, Version, flags}, body...)
	return w.sendFramed(framed)
}

// scanIDs pulls the header ids from every signed frame inside a sync
// frames message (the only packets whose repeated 32-byte ids are worth
// interning). Non-frames messages (summaries) intern nothing.
func scanIDs(pkt []byte) []idKey {
	_, frames, ok := kernelsync.ExtractFramesMessage(pkt)
	if !ok {
		return nil
	}
	var ids []idKey
	seen := map[idKey]bool{}
	for _, f := range frames {
		for _, k := range idsOf(f) {
			if !seen[k] {
				seen[k] = true
				ids = append(ids, k)
			}
		}
	}
	return ids
}

func (w *statefulWrap) Poll() [][]byte {
	var out [][]byte
	for _, msg := range w.drain() {
		if len(msg) >= 3 && msg[0] == Magic && msg[1] == Version && msg[2]&flagTableAck != 0 {
			if len(msg) >= 5 {
				gen := uint16(msg[3])<<8 | uint16(msg[4])
				w.table.ackGeneration(gen)
			}
			continue
		}
		if len(msg) >= 3 && msg[0] == Magic && msg[1] == Version && msg[2]&flagTable != 0 {
			body := msg[3:]
			if msg[2]&flagDeflate != 0 {
				inflated, err := inflate(body)
				if err != nil {
					continue
				}
				body = inflated
			}
			payload, gen, err := w.table.decode(body)
			if err != nil {
				continue // undefined index etc. — drop; retry heals
			}
			// TABLE_ACK the generation we just learned (best-effort).
			_ = w.inner.Send([]byte{Magic, Version, flagTableAck, byte(gen >> 8), byte(gen)})
			out = append(out, payload)
			continue
		}
		out = append(out, msg)
	}
	return out
}
