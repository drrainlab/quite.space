// SP-3 envelope classification (ADR-031): a position is an ephemeral
// state patch with its signed TTL mirrored into custody expiry; a
// check-in is a custodied fact; SOS rides PriorityEmergency — the first
// use of the lane ADR-015 declared.
package terminals_test

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/geo"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
)

func fieldTestSpace(t *testing.T) (*terminals.Space, *terminals.Participant) {
	t.Helper()
	s, owner := buildPublicSpace(t, 0)
	return s, owner
}

func TestFieldEnvelopeClassification(t *testing.T) {
	s, owner := fieldTestSpace(t)
	now := uint64(time.Now().Unix())
	pt, err := geo.FromDegrees(59.33, 18.04)
	if err != nil {
		t.Fatal(err)
	}

	// Position: StatePatch, NoCustody, custody expiry = the SIGNED TTL.
	posPayload, err := (&schemas.PositionObservation{Point: pt, ExpiresAt: now + 600}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	a, err := owner.Emit(s, schemas.ObservationPosition, posPayload, signal.AuthorshipHuman, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.Env.Priority != signal.PriorityStatePatch {
		t.Fatalf("position priority %d", a.Env.Priority)
	}
	if a.Env.ForwardingScope() != signal.NoCustody {
		t.Fatal("position took custody")
	}
	if a.Env.ExpiresAt != now+600 {
		t.Fatalf("position custody expiry %d, want the signed TTL", a.Env.ExpiresAt)
	}

	// Plain check-in: custodied (no expiry, no forwarding cap), Message.
	ciPayload, err := (&schemas.Checkin{Text: "✓ check-in"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	a, err = owner.Emit(s, schemas.CheckinSent, ciPayload, signal.AuthorshipHuman, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.Env.Priority != signal.PriorityMessage || a.Env.ForwardingScope() != signal.CustodyAllowed || a.Env.ExpiresAt != 0 {
		t.Fatalf("checkin class wrong: prio=%d scope=%v exp=%d",
			a.Env.Priority, a.Env.ForwardingScope(), a.Env.ExpiresAt)
	}

	// SOS: the first PriorityEmergency in the codebase — still custodied.
	sosPayload, err := (&schemas.Checkin{Text: "🆘 SOS", SOS: true}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	a, err = owner.Emit(s, schemas.CheckinSent, sosPayload, signal.AuthorshipHuman, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.Env.Priority != signal.PriorityEmergency {
		t.Fatalf("SOS priority %d, want Emergency", a.Env.Priority)
	}
	if a.Env.ForwardingScope() != signal.CustodyAllowed {
		t.Fatal("SOS lost custody — it must ARRIVE")
	}

	// Marker: deliberately unclassified — default Message lane, custodied.
	mk := &schemas.PlacedMarker{Text: "hazard", Kind: "hazard", Point: pt}
	copy(mk.MarkerID[:], []byte("0123456789abcdef"))
	mkPayload, err := mk.Encode()
	if err != nil {
		t.Fatal(err)
	}
	a, err = owner.Emit(s, schemas.MarkerPlaced, mkPayload, signal.AuthorshipHuman, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.Env.Priority != signal.PriorityMessage || a.Env.ExpiresAt != 0 {
		t.Fatalf("marker class wrong: prio=%d exp=%d", a.Env.Priority, a.Env.ExpiresAt)
	}
}

// The position folds into the trust engine on absorb, and honest ageing
// falls straight out of the signed TTL.
func TestPositionFoldsIntoTrust(t *testing.T) {
	s, owner := fieldTestSpace(t)
	now := uint64(time.Now().Unix())
	pt, err := geo.FromDegrees(59.33, 18.04)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := (&schemas.PositionObservation{Point: pt, AccuracyM: 8, ExpiresAt: now + 600}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Emit(s, schemas.ObservationPosition, payload, signal.AuthorshipHuman, now); err != nil {
		t.Fatal(err)
	}
	pos := s.Trust.Position(owner.TerminalID, now+30)
	if !pos.Known || !pos.Current || pos.AccuracyM != 8 {
		t.Fatalf("live projection wrong: %+v", pos)
	}
	stale := s.Trust.Position(owner.TerminalID, now+4000)
	if !stale.Known || stale.Current {
		t.Fatalf("expired position still current: %+v", stale)
	}
}
