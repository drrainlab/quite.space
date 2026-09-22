package sync

// LT-4 S4: DR-1 receipts between two live peers ride the link as their
// own message, and a peer that does not listen for them loses nothing.

import (
	"bytes"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/loopback"
)

func TestReceiptsMessageRoundTrip(t *testing.T) {
	term := id.TerminalID{0xD4}
	a := NewEngine(eventlog.New(term, nil))
	b := NewEngine(eventlog.New(term, nil))
	var got [][]byte
	a.OnDeliveryReceipts = func(rc [][]byte) { got = append(got, rc...) }
	pair := loopback.NewPair(loopback.Faults{Seed: 7})
	want := [][]byte{[]byte("receipt one"), []byte("receipt two")}
	if err := b.SendReceipts(pair.B, want); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Pump(pair.A); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !bytes.Equal(got[0], want[0]) || !bytes.Equal(got[1], want[1]) {
		t.Fatalf("receipts did not round-trip: %q", got)
	}
}

// A peer without a receipts listener — or an older build that never
// learned the type — takes the message as a no-op: applied 0, no error,
// the link stays up.
func TestAPeerWithoutAListenerSkipsAReceiptsMessage(t *testing.T) {
	term := id.TerminalID{0xD5}
	a := NewEngine(eventlog.New(term, nil))
	b := NewEngine(eventlog.New(term, nil))
	pair := loopback.NewPair(loopback.Faults{Seed: 8})
	if err := b.SendReceipts(pair.B, [][]byte{[]byte("nobody listens")}); err != nil {
		t.Fatal(err)
	}
	applied, rejected, err := a.Pump(pair.A)
	if err != nil || applied != 0 || rejected != 0 {
		t.Fatalf("a receipts message must be a silent no-op: applied=%d rejected=%d err=%v", applied, rejected, err)
	}
}
