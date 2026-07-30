package terminals_test

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/human"
)

// The point of a separate envelope: a refusal must never be openable as a
// grant, in either direction. If one parser could read both, the door and
// the keys would be one message with two meanings.
func TestADeclineCannotBeReadAsAGrant(t *testing.T) {
	guest, err := human.New("bob")
	if err != nil {
		t.Fatal(err)
	}
	space := id.TerminalID{7}
	reqID := [32]byte{3}
	xpub := guest.Device.X25519Pub
	xpriv := guest.Device.X25519Priv()

	decl, err := terminals.BuildDecision(space, xpub, &terminals.Decision{
		RequestID: reqID, State: terminals.DecisionDeclined, Reason: "not this time",
	})
	if err != nil {
		t.Fatal(err)
	}
	// It opens as what it is.
	got, err := terminals.OpenDecision(space, reqID, xpriv, decl)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != terminals.DecisionDeclined || got.Reason != "not this time" {
		t.Fatalf("the decision changed in transit: %+v", got)
	}
	// And NOT as an acceptance — different info string, different meaning.
	if _, err := terminals.OpenAccepted(space, reqID, xpriv, decl); err == nil {
		t.Fatal("a decline opened as an acceptance")
	}
}

// A decision addressed to one request must not answer another.
func TestADecisionIsBoundToItsRequest(t *testing.T) {
	guest, err := human.New("bob")
	if err != nil {
		t.Fatal(err)
	}
	space := id.TerminalID{7}
	sealed, err := terminals.BuildDecision(space, guest.Device.X25519Pub,
		&terminals.Decision{RequestID: [32]byte{1}, State: terminals.DecisionDeclined})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := terminals.OpenDecision(space, [32]byte{2},
		guest.Device.X25519Priv(), sealed); err == nil {
		t.Fatal("a decision answered a request it was not about")
	}
}

// A decision with no state is not a decision.
func TestADecisionMustSayWhatWasDecided(t *testing.T) {
	guest, err := human.New("bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := terminals.BuildDecision(id.TerminalID{7}, guest.Device.X25519Pub,
		&terminals.Decision{RequestID: [32]byte{1}}); err == nil {
		t.Fatal("an empty decision was sealed")
	}
}
