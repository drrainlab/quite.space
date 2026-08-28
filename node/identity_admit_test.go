package node

import (
	"testing"

	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func certEnvelope(t *testing.T, p *identity.Principal, d *identity.Device, at uint64) *signal.Envelope {
	t.Helper()
	frame, err := p.Certify(d, at, 0).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return &signal.Envelope{Schema: schemas.DeviceCertified, Payload: frame}
}

func revEnvelope(t *testing.T, p *identity.Principal, d *identity.Device, at uint64) *signal.Envelope {
	t.Helper()
	frame, err := p.Revoke(d.ID, at).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return &signal.Envelope{Schema: schemas.DeviceRevoked, Payload: frame}
}

// A device that already had history here when this build first ran predates
// certification. It keeps speaking — an upgrade is never a reason to stop
// hearing the people already in somebody's spaces.
func TestAllowlistedDeviceStillVerifies(t *testing.T) {
	p, _ := identity.NewPrincipal(identity.NewRand())
	d, _ := identity.NewDevice(identity.NewRand())
	old := &signal.Envelope{Principal: p.ID, Device: d.ID, LogicalClock: 5}

	s := newIdentityState()
	s.freezeLegacy(map[storage.LegacyBinding]bool{
		{Principal: p.ID, Device: d.ID}: true,
	})
	if err := s.admit(old); err != nil {
		t.Fatalf("a device that predates certification was refused: %v", err)
	}
}

// The hole an open ratchet would leave. A device FIRST SEEN after the
// migration has no history to grandfather it in, so it must present a
// certificate — otherwise an attacker simply never publishes one, stays
// "legacy" forever, and keeps putting anybody's id in env.Principal.
func TestDeviceFirstSeenAfterMigrationNeedsACertificate(t *testing.T) {
	p, _ := identity.NewPrincipal(identity.NewRand())
	known, _ := identity.NewDevice(identity.NewRand())
	stranger, _ := identity.NewDevice(identity.NewRand())

	s := newIdentityState()
	// The migration froze one pair. The stranger is not in it.
	s.freezeLegacy(map[storage.LegacyBinding]bool{
		{Principal: p.ID, Device: known.ID}: true,
	})

	newcomer := &signal.Envelope{Principal: p.ID, Device: stranger.ID, LogicalClock: 9}
	if err := s.admit(newcomer); err == nil {
		t.Fatal("an uncertified device first seen after the migration was admitted")
	}

	// With a certificate it is admitted — the ratchet closes a door, it does
	// not wall the network off.
	s.observe(certEnvelope(t, p, stranger, 1))
	if err := s.admit(newcomer); err != nil {
		t.Fatalf("a certified device was refused: %v", err)
	}
}

// The allowlist is a PAIR, not a device: being grandfathered in as yourself
// is not permission to start claiming somebody else.
func TestAnAllowlistedDeviceCannotClaimADifferentPerson(t *testing.T) {
	p, _ := identity.NewPrincipal(identity.NewRand())
	victim, _ := identity.NewPrincipal(identity.NewRand())
	d, _ := identity.NewDevice(identity.NewRand())

	s := newIdentityState()
	s.freezeLegacy(map[storage.LegacyBinding]bool{
		{Principal: p.ID, Device: d.ID}: true,
	})
	impersonation := &signal.Envelope{Principal: victim.ID, Device: d.ID, LogicalClock: 3}
	if err := s.admit(impersonation); err == nil {
		t.Fatal("an allowlisted device claimed a different principal and was admitted")
	}
}

// THE ORDERING TEST. Certificates and revocations live in different space
// logs. If admission were decided while replaying, the answer would depend on
// which space opened first — replay B, admit an event from D, then replay A
// and only now meet Revocation(D), by which point what B applied does not
// un-apply itself. The scan must finish across every log before one decision
// is taken, and this pins it: all six orders, one answer.
//
// The one answer CHANGED once, and the change is the point of the second
// assertion pair. This test used to demand that a live frame stamped
// "before" the revocation be admitted as history — and the e2e revoke test
// caught what that arithmetic hands a thief: the stamp is the AUTHOR's
// claim, a stolen device's clock naturally lags the authority's, so its
// next message filed itself under history and landed. History standing is
// the REPLAY path's property (the gate is not yet installed while a log
// replays its own frames); the gate itself, which only ever sees live
// arrivals, refuses a revoked device at every clock.
func TestAdmissionIsIndependentOfSpaceReplayOrder(t *testing.T) {
	p, _ := identity.NewPrincipal(identity.NewRand())
	d, _ := identity.NewDevice(identity.NewRand())

	// Three "space logs": one carries the certificate, one the revocation,
	// one nothing relevant at all.
	spaceA := []*signal.Envelope{certEnvelope(t, p, d, 1)}
	spaceB := []*signal.Envelope{revEnvelope(t, p, d, 10)}
	spaceC := []*signal.Envelope{{Principal: p.ID, Device: d.ID, LogicalClock: 4}}

	logs := [][]*signal.Envelope{spaceA, spaceB, spaceC}
	orders := [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}

	// A live frame from the revoked device is refused at EVERY clock — the
	// backdated one exactly as firmly as the honest one.
	backdated := &signal.Envelope{Principal: p.ID, Device: d.ID, LogicalClock: 4}
	after := &signal.Envelope{Principal: p.ID, Device: d.ID, LogicalClock: 40}

	for _, order := range orders {
		s := newIdentityState()
		for _, which := range order {
			for _, env := range logs[which] {
				s.observe(env)
			}
		}
		if err := s.admit(backdated); err == nil {
			t.Fatalf("order %v: a backdated live frame from a revoked device was admitted", order)
		}
		if err := s.admit(after); err == nil {
			t.Fatalf("order %v: a revoked device was admitted", order)
		}
	}
}

// ADR-002 decision 3, both halves in one place: a revoked device stops
// SPEAKING at once — at any clock it cares to claim, because the clock is
// its own claim — while everything a log already HOLDS stands, because
// replay runs before this gate exists (eventlog.Open takes nil hooks;
// attachSpace wires them after). The first half is asserted here; the
// second is asserted where it lives, by the e2e revoke test counting its
// pre-revocation message after the refusals.
func TestRevokedDeviceCannotSpeakAtAnyClock(t *testing.T) {
	p, _ := identity.NewPrincipal(identity.NewRand())
	d, _ := identity.NewDevice(identity.NewRand())

	s := newIdentityState()
	s.observe(certEnvelope(t, p, d, 1))
	s.observe(revEnvelope(t, p, d, 100))

	for _, clk := range []uint64{1, 99, 100, 101} {
		if err := s.admit(&signal.Envelope{Principal: p.ID, Device: d.ID, LogicalClock: clk}); err == nil {
			t.Fatalf("a revoked device spoke at clock %d", clk)
		}
	}
}

// A certificate is a claim by a ROOT KEY about a device. One signed by
// somebody else's root does not make that device ours, and a forged one does
// not verify at all.
func TestACertificateFromAnotherPrincipalDoesNotAdmit(t *testing.T) {
	mine, _ := identity.NewPrincipal(identity.NewRand())
	theirs, _ := identity.NewPrincipal(identity.NewRand())
	d, _ := identity.NewDevice(identity.NewRand())

	s := newIdentityState()
	s.observe(certEnvelope(t, theirs, d, 1)) // valid, but not from our root

	claimingMe := &signal.Envelope{Principal: mine.ID, Device: d.ID, LogicalClock: 2}
	if err := s.admit(claimingMe); err == nil {
		t.Fatal("a device certified by another principal spoke as ours")
	}
}
