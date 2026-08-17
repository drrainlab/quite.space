package node

import (
	"testing"

	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// The split that makes IngressHold safe (MD-0b). `error` cannot express the
// difference between "not yet" and "never", so holding everything refused
// would turn loss-protection into a durable denial of service: anybody could
// fill the queue with signed-but-forbidden material.
//
// These tests are about the CLASSIFICATION only. Nothing here persists
// anything — that is deliberate, and it is why the identity layer never
// acquires transport custody.

func TestARevokedDeviceIsRejectedAndNeverHeld(t *testing.T) {
	p, _ := identity.NewPrincipal(identity.NewRand())
	d, _ := identity.NewDevice(identity.NewRand())

	s := newIdentityState()
	s.observe(certEnvelope(t, p, d, 1))
	s.observe(revEnvelope(t, p, d, 10))

	got := s.classify(&signal.Envelope{Principal: p.ID, Device: d.ID, LogicalClock: 20})
	if got.Verdict != Reject {
		t.Fatalf("a revoked device was not rejected: %+v", got)
	}
	if got.Reason != ReasonRevoked {
		t.Fatalf("wrong reason, and the reason is what decides holding: %v", got.Reason)
	}
}

// The ONE transient case this wave knows about. It is a HOLD because the
// prerequisite may still arrive; everything else that fails is permanent.
func TestAnUncertifiedDeviceIsHeldNotRejected(t *testing.T) {
	p, _ := identity.NewPrincipal(identity.NewRand())
	stranger, _ := identity.NewDevice(identity.NewRand())

	s := newIdentityState()
	got := s.classify(&signal.Envelope{Principal: p.ID, Device: stranger.ID, LogicalClock: 3})
	if got.Verdict != Hold {
		t.Fatalf("a frame whose certificate may still arrive was not held: %+v", got)
	}
	if got.Reason != ReasonCertificateNotKnown {
		t.Fatalf("wrong reason: %v", got.Reason)
	}
}

// A device certified by SOMEBODY ELSE's root claiming our principal is not
// waiting for anything — no certificate will ever make that true.
func TestAPrincipalMismatchIsPermanent(t *testing.T) {
	mine, _ := identity.NewPrincipal(identity.NewRand())
	theirs, _ := identity.NewPrincipal(identity.NewRand())
	d, _ := identity.NewDevice(identity.NewRand())

	s := newIdentityState()
	s.observe(certEnvelope(t, theirs, d, 1))

	got := s.classify(&signal.Envelope{Principal: mine.ID, Device: d.ID, LogicalClock: 2})
	if got.Verdict != Reject {
		t.Fatalf("a principal mismatch was not permanent: %+v", got)
	}
	if got.Reason != ReasonPrincipalMismatch {
		t.Fatalf("wrong reason: %v", got.Reason)
	}
}

func TestAllowlistedAndCertifiedBothAdmit(t *testing.T) {
	p, _ := identity.NewPrincipal(identity.NewRand())
	legacy, _ := identity.NewDevice(identity.NewRand())
	certified, _ := identity.NewDevice(identity.NewRand())

	s := newIdentityState()
	s.freezeLegacy(map[storage.LegacyBinding]bool{
		{Principal: p.ID, Device: legacy.ID}: true,
	})
	s.observe(certEnvelope(t, p, certified, 1))

	if got := s.classify(&signal.Envelope{Principal: p.ID, Device: legacy.ID}); got.Verdict != Admit {
		t.Fatalf("an allowlisted device was not admitted: %+v", got)
	}
	if got := s.classify(&signal.Envelope{Principal: p.ID, Device: certified.ID}); got.Verdict != Admit {
		t.Fatalf("a certified device was not admitted: %+v", got)
	}
}

// A certificate carries its own proof, so it is admitted on its own merits.
// Without this the queue could never drain: the frame that would release the
// held ones would itself be held.
func TestACertificateIsAlwaysAdmitted(t *testing.T) {
	p, _ := identity.NewPrincipal(identity.NewRand())
	d, _ := identity.NewDevice(identity.NewRand())

	s := newIdentityState()
	if got := s.classify(certEnvelope(t, p, d, 1)); got.Verdict != Admit {
		t.Fatalf("a certificate could not travel: %+v", got)
	}
}
