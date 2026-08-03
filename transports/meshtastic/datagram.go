// Meshtastic as ONE carrier, rather than as "the radio".
//
// Everything above this file works in terms of four methods — MTU, Send,
// Receive, Credit — and nothing above it knows what a Meshtastic node is. The
// point is not abstraction for its own sake: it is that a later RNode driver
// satisfies the same four methods, so "RNode is a second small driver rather
// than a rewrite of the radio architecture" becomes something a compiler can
// check.
//
// The adapter is small because the honest surface is small. What it must get
// exactly right is the MTU, and that is the one thing it does not invent.
package meshtastic

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	"github.com/drrainlab/quiet_places/transports"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
)

// sender is what a Datagram needs from a link: the supervised link and the
// bare radio both satisfy it, so a caller may pick reconnect-or-not without
// this file caring.
type sender interface {
	SendTo(to uint32, pkt []byte) error
	PollFrom() []Inbound
	Capabilities() transports.Capabilities
}

// NodeAddress renders a Meshtastic node number as the opaque address the
// layer above passes around. Four bytes, big-endian; nil means broadcast.
//
// The address is opaque ON PURPOSE — radiotransfer never interprets it, it
// only hands back whatever it was given — so the encoding is this driver's
// business alone and a second carrier may name its peers however it likes.
func NodeAddress(node uint32) radiotransfer.RadioAddress {
	if node == 0 || node == Broadcast {
		return nil
	}
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], node)
	return radiotransfer.RadioAddress(b[:])
}

// nodeOf reads an address back. Anything it does not recognise is a broadcast,
// because refusing to send is worse than sending too widely.
func nodeOf(addr radiotransfer.RadioAddress) uint32 {
	if len(addr) != 4 {
		return Broadcast
	}
	return binary.BigEndian.Uint32(addr)
}

// Datagram presents a Meshtastic link as a radiotransfer.RadioDatagram.
type Datagram struct {
	link sender
	// pacer is the same link when it can report its queue. A link that cannot
	// says so by being absent, and the layer above treats that as unmetered
	// rather than as zero.
	pacer transports.Pacer
	// poll is how often Receive looks for arrivals. The Meshtastic client API
	// hands frames to a reader goroutine which parks them in an inbox; there
	// is no channel to select on, so this is a poll rather than a wait.
	poll time.Duration
	// pending holds the remainder of a batch. PollFrom returns several frames
	// at once; Receive returns one. Dropping the rest would be
	// indistinguishable from carrier loss — exactly the confusion this wave
	// exists to end — so they wait here for the next call.
	pending []Inbound
}

// NewDatagram wraps a link. It does not own it: closing the link stays the
// caller's job, because the link usually outlives any one session.
func NewDatagram(link sender) *Datagram {
	d := &Datagram{link: link, poll: 100 * time.Millisecond}
	if p, ok := link.(transports.Pacer); ok {
		d.pacer = p
	}
	return d
}

// MTU is the carrier's own answer, not a constant.
//
// The contract above defines this as the maximum Radio Transfer frame bytes
// Send accepts atomically — excluding carrier framing, including our header
// and MAC. For Meshtastic that is exactly the payload a MeshPacket carries,
// which the link already reports as MaxPayload. Hard-coding 200 here would be
// right until somebody changed an Option, and wrong silently: an oversized
// frame is refused by Send, which looks like a busy radio.
func (d *Datagram) MTU() int {
	if c := d.link.Capabilities(); c.MaxPayload > 0 {
		return c.MaxPayload
	}
	return 200
}

// Send hands one frame to the radio, addressed when the caller named a peer.
//
// A nil dst is a broadcast, which is what a DATA frame on a segment wants.
// Everything else — SACK, COMMIT, CANCEL — is meant for one peer, and used to
// go to the whole segment anyway because this driver discarded dst and always
// wrote Broadcast. That was airtime spent telling people something they could
// not use.
//
// What this does NOT claim: an addressed packet may still traverse
// intermediate nodes. It says who, never how far.
func (d *Datagram) Send(ctx context.Context, dst radiotransfer.RadioAddress, frame []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := d.link.SendTo(nodeOf(dst), frame)
	if err == nil {
		return nil
	}
	// A radio that will not take a frame right now is a normal answer on this
	// carrier, and it must reach the layer above as ErrCarrierFull rather than
	// as a failure — the difference is whether the frame is offered again or
	// the message is abandoned.
	if isFull(err) {
		return radiotransfer.ErrCarrierFull
	}
	return err
}

// Receive waits for the next frame, and names the node that sent it.
//
// It used to answer the constant "segment" for every arrival, which read as
// harmless and was not: the layer above keys MaxInflightTransfersPerPeer on
// this value, so one string for every neighbour turned a per-peer bound into a
// segment-wide one and quietly deleted the memory defence that limit exists to
// provide.
func (d *Datagram) Receive(ctx context.Context) (radiotransfer.RadioAddress, []byte, error) {
	t := time.NewTicker(d.poll)
	defer t.Stop()
	for {
		if pkts := d.link.PollFrom(); len(pkts) > 0 {
			d.stash(pkts[1:])
			return NodeAddress(pkts[0].From), pkts[0].Payload, nil
		}
		if p, ok := d.take(); ok {
			return NodeAddress(p.From), p.Payload, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-t.C:
		}
	}
}

// Credit passes the radio's own report on its queue straight through. A link
// that does not report is unmetered, which is what transports.CreditOf
// already means everywhere else.
func (d *Datagram) Credit() transports.Credit {
	if d.pacer == nil {
		return transports.Credit{Packets: transports.CreditUnlimited, Known: true}
	}
	return d.pacer.Credit()
}

func (d *Datagram) stash(rest []Inbound) {
	if len(rest) == 0 {
		return
	}
	d.pending = append(d.pending, rest...)
}

func (d *Datagram) take() (Inbound, bool) {
	if len(d.pending) == 0 {
		return Inbound{}, false
	}
	p := d.pending[0]
	d.pending = d.pending[1:]
	return p, true
}

// isFull recognises the carrier saying "not now".
func isFull(err error) bool {
	return errors.Is(err, radiotransfer.ErrCarrierFull) ||
		errors.Is(err, errQueueFull)
}

// errQueueFull is what this package returns when the firmware refuses.
var errQueueFull = errors.New("meshtastic: the radio would not queue the packet")
