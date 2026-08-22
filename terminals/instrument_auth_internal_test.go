package terminals

// The authorization set moves only FORWARD with the epoch number (QI-M,
// owner's amendment 13). Two authority devices of one principal, or a
// replayed frame, can deliver epoch 7 after epoch 8; the key of 7 stays
// usable for delayed opens, but the RECIPIENTS of 7 never re-authorize a
// device that 8 dropped. A security property, so a regression test.

import (
	"testing"

	"github.com/drrainlab/quiet_places/kernel/crypto"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func epochFrame(t *testing.T, n uint64, devs ...id.DeviceID) *signal.Envelope {
	t.Helper()
	ep := &schemas.EpochPayload{N: n}
	for _, d := range devs {
		ep.Wraps = append(ep.Wraps, crypto.Wrap{Device: d, Enc: make([]byte, 32), CT: make([]byte, 48)})
	}
	payload, err := ep.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return &signal.Envelope{Schema: schemas.InstrumentEpoch, Payload: payload}
}

func TestALateLowerEpochNeverRollsAuthorizationBack(t *testing.T) {
	var space id.TerminalID
	space[0] = 7
	s := Replica(space)
	var kept, dropped id.DeviceID
	kept[0], dropped[0] = 1, 2

	s.absorbInstrumentEpoch(epochFrame(t, 8, kept))
	if !s.InstrumentAuthorized(kept) || s.InstrumentAuthorized(dropped) {
		t.Fatal("epoch 8 did not install its recipients as the authorization set")
	}
	// Epoch 7 — older, and it still listed the dropped device — arrives late.
	s.absorbInstrumentEpoch(epochFrame(t, 7, kept, dropped))
	if s.InstrumentAuthorized(dropped) {
		t.Fatal("a late epoch 7 re-authorized a device epoch 8 had dropped")
	}
	if s.CurrentInstrumentEpoch() != 8 {
		t.Fatalf("current epoch rolled back to %d", s.CurrentInstrumentEpoch())
	}
	// And a genuinely newer epoch moves the set forward again.
	s.absorbInstrumentEpoch(epochFrame(t, 9, dropped))
	if s.InstrumentAuthorized(kept) || !s.InstrumentAuthorized(dropped) {
		t.Fatal("epoch 9 did not replace the authorization set")
	}
}
