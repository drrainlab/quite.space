package meshtastic

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
)

type sentPacket struct {
	to  uint32
	pkt []byte
}

// fakeLink stands in for a radio and remembers WHERE each packet was addressed,
// which is the whole subject of these tests.
type fakeLink struct {
	sent []sentPacket
	in   []Inbound
}

func (f *fakeLink) SendTo(to uint32, pkt []byte) error {
	f.sent = append(f.sent, sentPacket{to: to, pkt: append([]byte(nil), pkt...)})
	return nil
}

func (f *fakeLink) PollFrom() []Inbound {
	out := f.in
	f.in = nil
	return out
}

func (f *fakeLink) Capabilities() transports.Capabilities {
	return transports.Capabilities{MaxPayload: 200}
}

// Two neighbours must not look like one.
//
// The adapter answered the constant "segment" for every arrival, and it read as
// a harmless simplification. It was not: radiotransfer keys
// MaxInflightTransfersPerPeer on this value, so one string standing in for
// every neighbour turned "at most four transfers per peer" into "at most four
// transfers on the whole segment", and deleted the memory defence that limit
// exists to provide.
func TestTwoNeighboursAreTwoPeers(t *testing.T) {
	link := &fakeLink{in: []Inbound{
		{From: 0x043ccd50, Payload: []byte("from the heltec")},
		{From: 0x4a307b54, Payload: []byte("from the other board")},
	}}
	d := NewDatagram(link)
	ctx := context.Background()

	srcA, payloadA, err := d.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srcB, payloadB, err := d.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(srcA, srcB) {
		t.Fatalf("two different nodes both reported as %q: per-peer limits "+
			"collapse into one segment-wide limit", srcA)
	}
	if !bytes.Equal(payloadA, []byte("from the heltec")) ||
		!bytes.Equal(payloadB, []byte("from the other board")) {
		t.Fatal("the batch remainder was not preserved in order")
	}
	// And the address must name the node it came from, or an answer cannot be
	// sent back to it.
	if got := nodeOf(srcA); got != 0x043ccd50 {
		t.Fatalf("address decoded to node %08x, want 043ccd50", got)
	}
}

// An addressed answer must actually be addressed.
//
// MeshPacket has carried a destination all along and this driver wrote
// Broadcast into every one, so a SACK meant for one peer went to the whole
// segment — airtime spent telling people something they cannot use.
func TestAnAddressedFrameReachesOneNodeNotTheSegment(t *testing.T) {
	link := &fakeLink{}
	d := NewDatagram(link)
	ctx := context.Background()

	if err := d.Send(ctx, NodeAddress(0x4a307b54), []byte("your fragment 3 is missing")); err != nil {
		t.Fatal(err)
	}
	if err := d.Send(ctx, nil, []byte("data for everybody")); err != nil {
		t.Fatal(err)
	}
	if len(link.sent) != 2 {
		t.Fatalf("sent %d packets, want 2", len(link.sent))
	}
	if link.sent[0].to != 0x4a307b54 {
		t.Fatalf("an addressed frame went to %08x, want 4a307b54 — dst is "+
			"still being discarded", link.sent[0].to)
	}
	if link.sent[1].to != Broadcast {
		t.Fatalf("a nil destination went to %08x, want Broadcast", link.sent[1].to)
	}
}

// The address is opaque to everything above this package, so its only
// obligation is to round-trip.
func TestANodeAddressRoundTrips(t *testing.T) {
	for _, n := range []uint32{1, 0x043ccd50, 0xFFFFFFFE} {
		if got := nodeOf(NodeAddress(n)); got != n {
			t.Fatalf("node %08x round-tripped to %08x", n, got)
		}
	}
	// Broadcast and zero have no address: they ARE the absence of one.
	if NodeAddress(Broadcast) != nil || NodeAddress(0) != nil {
		t.Fatal("broadcast rendered as an address rather than as nil")
	}
	// Anything unrecognised must broadcast rather than refuse to send.
	if got := nodeOf(radiotransfer.RadioAddress("odd")); got != Broadcast {
		t.Fatalf("an unreadable address resolved to %08x, want Broadcast", got)
	}
}

// Receive must not spin when there is nothing to hear.
func TestReceiveWaitsRatherThanBusyLooping(t *testing.T) {
	d := NewDatagram(&fakeLink{})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, _, err := d.Receive(ctx); err == nil {
		t.Fatal("Receive returned a frame from a silent carrier")
	}
}
