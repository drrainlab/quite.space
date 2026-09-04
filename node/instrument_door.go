// The instrument door (QI-B1, ADR-035): a certified board enters the LAN
// listener and gets exactly one space — never the router.
//
// A sync peer and an instrument arrive through the same TLS accept, and
// the FIRST MESSAGE tells them apart: a peer's opening traffic (hello,
// summary) adopts the conn as an ordinary link, re-fed bit-for-bit so the
// engines never learn a classifier stood in front of them; an epoch
// request opens the door instead. The door's laws:
//
//   - THE LINK SCOPES TO ITS SPACE. An instrument's conn is adopted
//     through adoptLinkFiltered with an allow that names one terminal;
//     the summaries and frames of every other space never touch this
//     wire (a summary names its terminal in plaintext — that is exactly
//     the metadata the LAN hint scheme exists to withhold).
//   - EPOCHS RIDE DOWN THE SAME PIPE FRAMES RIDE UP. The reply and every
//     later rotation push travel this conn — no side channel, no open
//     HTTP, no session.
//   - THE DOOR IS NOT AN ORACLE. A knock that fails — unknown space,
//     unknown device, bad signature: one refusal, indistinguishable —
//     closes the conn with nothing said. The hello that already went out
//     is link-scoped and names no space; it tells a prober only that a
//     quiet node lives here, which the announcer already broadcasts.
//   - TIME ONLY FORWARD. Every epochs payload carries the node's unix
//     clock; the device treats it as a floor, never as a setback.
package node

import (
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/drrainlab/quiet_places/kernel/routing"
	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// instrDoorLabel is both the TLS exporter label and the signature domain
// of the door knock — session-unique, so a knock recorded on one conn
// proves nothing on another (the lanHello law, applied to devices that
// are not people).
const instrDoorLabel = "qp-instr-door-v0"

// Door-knock payload keys (append-only).
const (
	reqKeySpace  = 1
	reqKeyDevice = 2
	reqKeySig    = 3
)

// Epochs payload keys (append-only).
const (
	epochsKeySpace  = 1
	epochsKeyFrames = 2
	epochsKeyUnix   = 3
)

// classifyWait bounds how long an inbound conn may stay silent before it
// is adopted as an ordinary peer anyway — the pre-classifier behavior,
// kept for peers that connect and listen.
const classifyWait = 4 * time.Second

// encodeEpochReqPayload builds the knock a device sends: which space,
// which device, and a signature binding both to THIS session.
func encodeEpochReqPayload(space id.TerminalID, device id.DeviceID, sig []byte) []byte {
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, reqKeySpace)
	buf = codec.AppendBytes(buf, space[:])
	buf = codec.AppendUint(buf, reqKeyDevice)
	buf = codec.AppendBytes(buf, device[:])
	buf = codec.AppendUint(buf, reqKeySig)
	buf = codec.AppendBytes(buf, sig)
	return buf
}

func decodeEpochReqPayload(payload []byte) (space id.TerminalID, device id.DeviceID, sig []byte, err error) {
	d := codec.NewDecoder(payload)
	m, e := d.ReadMapHeader()
	if e != nil {
		return space, device, nil, e
	}
	var spaceB, devB []byte
	for {
		k, more, e := m.Next()
		if e != nil {
			return space, device, nil, e
		}
		if !more {
			break
		}
		switch k {
		case reqKeySpace:
			spaceB, e = d.ReadBytes()
		case reqKeyDevice:
			devB, e = d.ReadBytes()
		case reqKeySig:
			sig, e = d.ReadBytes()
		default:
			e = d.SkipItem()
		}
		if e != nil {
			return space, device, nil, e
		}
	}
	if len(spaceB) != id.Size || len(devB) != id.Size || len(sig) != ed25519.SignatureSize {
		return space, device, nil, errors.New("node: malformed epoch request")
	}
	copy(space[:], spaceB)
	copy(device[:], devB)
	return space, device, sig, nil
}

// encodeEpochsPayload builds the freight the node sends down: the space it
// answers for, the current epoch frame(s) — empty for a plaintext space,
// honestly — and the node's clock as a floor.
func encodeEpochsPayload(space id.TerminalID, frames [][]byte, unix uint64) []byte {
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, epochsKeySpace)
	buf = codec.AppendBytes(buf, space[:])
	buf = codec.AppendUint(buf, epochsKeyFrames)
	buf = codec.AppendArray(buf, len(frames))
	for _, f := range frames {
		buf = codec.AppendBytes(buf, f)
	}
	buf = codec.AppendUint(buf, epochsKeyUnix)
	buf = codec.AppendUint(buf, unix)
	return buf
}

func decodeEpochsPayload(payload []byte) (space id.TerminalID, frames [][]byte, unix uint64, err error) {
	d := codec.NewDecoder(payload)
	m, e := d.ReadMapHeader()
	if e != nil {
		return space, nil, 0, e
	}
	var spaceB []byte
	for {
		k, more, e := m.Next()
		if e != nil {
			return space, nil, 0, e
		}
		if !more {
			break
		}
		switch k {
		case epochsKeySpace:
			spaceB, e = d.ReadBytes()
		case epochsKeyFrames:
			var n int
			n, e = d.ReadArray()
			for i := 0; e == nil && i < n; i++ {
				var f []byte
				f, e = d.ReadBytes()
				if e == nil {
					frames = append(frames, append([]byte(nil), f...))
				}
			}
		case epochsKeyUnix:
			unix, e = d.ReadUint()
		default:
			e = d.SkipItem()
		}
		if e != nil {
			return space, nil, 0, e
		}
	}
	if len(spaceB) != id.Size {
		return space, nil, 0, errors.New("node: malformed epochs payload")
	}
	copy(space[:], spaceB)
	return space, frames, unix, nil
}

// prefixLink replays packets a classifier already consumed before handing
// the live link over — the engines see the exact byte stream the wire
// carried, in order, as if nobody had peeked.
type prefixLink struct {
	link
	prefix [][]byte
}

func (p *prefixLink) Poll() [][]byte {
	if len(p.prefix) > 0 {
		out := p.prefix
		p.prefix = nil
		return append(out, p.link.Poll()...)
	}
	return p.link.Poll()
}

// SessionBinding forwards the inner link's session capability — the
// embedded interface alone would hide it from type assertions.
func (p *prefixLink) SessionBinding(label string) ([]byte, bool) {
	if sb, ok := p.link.(sessionBound); ok {
		return sb.SessionBinding(label)
	}
	return nil, false
}

// Close forwards to the inner link. The link interface (transports.Endpoint
// + Closed) does NOT declare Close, so the embedding alone hid it — and
// StopLAN closes a link exactly by asserting interface{ Close() error }.
// Without this the wrapped socket could not be closed, its peer never saw
// EOF, and a downed LAN link's binding survived it forever.
func (p *prefixLink) Close() error {
	if cl, ok := p.link.(interface{ Close() error }); ok {
		return cl.Close()
	}
	return nil
}

// classifyLANConn is the seam in front of adoption: say hello first (both
// sides speaking first is what makes two classifying nodes converge
// instead of deadlocking; a board simply ignores it), then let the first
// complete message pick the road.
func (r *Runtime) classifyLANConn(c link) {
	r.sendLANHello(c)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		reasm := kernelsync.NewReassembler()
		var consumed [][]byte
		deadline := time.Now().Add(classifyWait)
		for {
			select {
			case <-r.stop:
				return
			default:
			}
			if closed, _ := c.Closed(); closed {
				return
			}
			pkts := c.Poll()
			for i, pkt := range pkts {
				raw, err := reasm.Feed(pkt)
				if errors.Is(err, kernelsync.ErrNotFragment) {
					raw, err = pkt, nil
				}
				if err != nil || raw == nil {
					consumed = append(consumed, pkt)
					continue
				}
				if req, ok := kernelsync.ExtractEpochReq(raw); ok {
					// The knock. Whatever the board sent after it (owed
					// frames, typically) is the door's prefix; everything
					// before it was fragments of the knock itself.
					r.openInstrumentDoor(c, req, pkts[i+1:])
					return
				}
				// Any other complete message: an ordinary peer. Re-feed
				// every consumed packet bit-for-bit, this one and the
				// rest of the batch included.
				consumed = append(consumed, pkts[i:]...)
				r.adoptLinkFilteredOpts(&prefixLink{link: c, prefix: consumed},
					pumpEvery, 2*time.Second, "lan", nil, false)
				return
			}
			if time.Now().After(deadline) {
				// A silent peer gets the pre-classifier behavior: adopted,
				// nothing lost, nothing revealed.
				r.adoptLinkFilteredOpts(&prefixLink{link: c, prefix: consumed},
					pumpEvery, 2*time.Second, "lan", nil, false)
				return
			}
			select {
			case <-r.stop:
				return
			case <-time.After(25 * time.Millisecond):
			}
		}
	}()
}

// openInstrumentDoor verifies the knock and, on success, scopes the conn
// to the named space and starts the epoch push watcher. Every failure is
// the same failure: close, silently.
func (r *Runtime) openInstrumentDoor(c link, req []byte, rest [][]byte) {
	closeQuiet := func() {
		if cl, ok := c.(interface{ Close() error }); ok {
			_ = cl.Close()
		}
	}
	space, device, sig, err := decodeEpochReqPayload(req)
	if err != nil {
		closeQuiet()
		return
	}
	sb, ok := c.(sessionBound)
	if !ok {
		closeQuiet()
		return
	}
	ekm, ok := sb.SessionBinding(instrDoorLabel)
	if !ok {
		closeQuiet()
		return
	}
	msg := append([]byte(instrDoorLabel+":"), ekm...)
	msg = append(msg, space[:]...)
	if !ed25519.Verify(ed25519.PublicKey(device[:]), msg, sig) {
		closeQuiet()
		return
	}
	// The device must be an ENROLLED EXTERNAL INSTRUMENT of that space —
	// the same record the serial stand's ingest binds to. Nothing else
	// opens this door: not a member's device, not a certificate alone.
	r.mu.Lock()
	known := false
	for _, rec := range r.ks.Instruments {
		if rec.External && rec.Space == space && rec.DevicePub == device {
			known = true
			break
		}
	}
	_, hasSpace := r.spaces[space]
	r.mu.Unlock()
	if !known || !hasSpace {
		closeQuiet()
		return
	}

	r.adoptLinkFilteredOpts(&prefixLink{link: c, prefix: rest},
		pumpEvery, 2*time.Second, "lan",
		func(m routing.FrameMeta) bool { return m.Destination == space }, false)
	r.sendEpochsTo(c, space)
	r.watchInstrumentDoor(c, space)
}

// sendEpochsTo pushes the space's current epoch freight down one conn.
func (r *Runtime) sendEpochsTo(c link, space id.TerminalID) {
	frames, err := r.ExternalInstrumentEpochFrames(space)
	if err != nil {
		return
	}
	payload := encodeEpochsPayload(space, frames, uint64(time.Now().Unix()))
	pkts, err := kernelsync.FragmentStream(0, kernelsync.EncodeEpochsMessage(payload), 0)
	if err != nil {
		return
	}
	for _, p := range pkts {
		_ = c.Send(p)
	}
}

// watchInstrumentDoor pushes every rotation to a live conn. It watches
// the node's OWN log — a local replay on a timer costs nothing the wire
// can see; the conn carries bytes only when the epoch actually changed.
func (r *Runtime) watchInstrumentDoor(c link, space id.TerminalID) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		var last []byte
		if frames, err := r.ExternalInstrumentEpochFrames(space); err == nil && len(frames) > 0 {
			last = frames[len(frames)-1]
		}
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-t.C:
			}
			if closed, _ := c.Closed(); closed {
				return
			}
			frames, err := r.ExternalInstrumentEpochFrames(space)
			if err != nil || len(frames) == 0 {
				continue
			}
			cur := frames[len(frames)-1]
			if string(cur) == string(last) {
				continue
			}
			last = cur
			r.sendEpochsTo(c, space)
		}
	}()
}
