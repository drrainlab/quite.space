package node

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports"
	"github.com/drrainlab/quiet_places/transports/compact"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
)

// pacedFake is a carrier that meters what it accepts, so a wrapper losing
// the report is visible rather than merely suspected.
type pacedFake struct{ credit transports.Credit }

func (p *pacedFake) Send([]byte) error { return nil }
func (p *pacedFake) Poll() [][]byte    { return nil }
func (p *pacedFake) Credit() transports.Credit {
	return p.credit
}
func (p *pacedFake) Capabilities() transports.Capabilities {
	return transports.Capabilities{MaxPayload: 200, Ack: transports.AckNone}
}

// THE defect this whole wave was built on top of.
//
// transferLink and compactLink embed transports.Endpoint — an INTERFACE — and
// Go promotes only the methods that interface declares. Credit() is not one of
// them, so transports.CreditOf() finds no Pacer, falls through to
// CreditUnlimited, and kernel/sync's Allows() returns true forever. The engine
// believed it was pacing against a radio and was pacing against nothing.
//
// This is the SAME promotion trap the file already documents for SendControl
// (node/mesh.go:192-199), found the same way — by running it — and fixed there
// by explicit forwarding. Credit was left behind.
//
// Why no test caught it: the e2e helper endpointLink embeds the CONCRETE
// *radiotransfer.Endpoint, so it gets Credit promoted for free. The harness
// accidentally had the property production lacked.
func TestCreditReachesTheEngineOnATransferLink(t *testing.T) {
	seed := sha256.Sum256([]byte("a segment for measuring credit"))
	key, err := radiotransfer.DeriveTransferKey(seed[:], radiotransfer.KDFVersion)
	if err != nil {
		t.Fatal(err)
	}
	air, _ := newSegment(200, 0, 11)
	ep, err := radiotransfer.Wrap(air, key, radiotransfer.EndpointOptions{
		Options: radiotransfer.Options{Limits: radiotransfer.Limits{
			Window: 4, MaxRounds: 2, AckTimeout: 50 * time.Millisecond,
			SACKDelay: 5 * time.Millisecond, SendFloor: 5 * time.Millisecond,
			FrameGap: time.Millisecond}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()

	// Exactly as node/mesh.go builds it, held at the interface type the pump
	// and the sync engine actually see.
	var lk transports.Endpoint = transferLink{Endpoint: ep, transfer: ep}

	c := transports.CreditOf(lk)
	if c.Unlimited() {
		t.Fatal("a transfer link reported UNMETERED credit: the carrier's " +
			"backpressure never reaches kernel/sync, so Allows() is true " +
			"however full the radio is")
	}
	if !c.Known {
		t.Fatal("a transfer link reported unknown credit where the carrier " +
			"underneath answers with Known: true")
	}
}

// The same loss, on the compact wire — and worse, because compact/compact.go:56
// forwards Credit deliberately, with a comment explaining that a wrapper which
// did not "would silently restore the flood". compactLink then drops it again
// one layer up.
func TestCreditReachesTheEngineOnACompactLink(t *testing.T) {
	inner := &pacedFake{credit: transports.Credit{
		Packets: 3, Known: true, RetryAfter: time.Second}}

	var lk transports.Endpoint = compactLink{Endpoint: compact.Wrap(inner)}

	c := transports.CreditOf(lk)
	if c.Unlimited() || !c.Known {
		t.Fatalf("a compact link reported %+v where the carrier said 3 packets: "+
			"compact forwards Credit on purpose and compactLink loses it again", c)
	}
	if c.Packets != 3 {
		t.Fatalf("compact link credit = %d packets, carrier said 3", c.Packets)
	}
}
