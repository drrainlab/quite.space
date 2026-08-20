package node

// ADR-024, mechanism 1: the owner derives the wrap list from the
// principal, using certificates its own log already admitted. These are
// the unit teeth of the latent-deafness half of the 1B gate.

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/identity"
)

// TestRotationCarriesCertifiedSiblings — a member's sibling, whose
// certificate has reached the owner, is wrapped into the next epoch
// without anyone asking; a revoked sibling never is.
func TestRotationCarriesCertifiedSiblings(t *testing.T) {
	now := uint64(time.Now().Unix())
	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	tid, err := owner.CreateSpace("room")
	if err != nil {
		t.Fatal(err)
	}

	// A person with two devices: guest (joins) and its paired sibling.
	guest := openRuntime(t, t.TempDir(), "guest")
	defer guest.Close()
	sibling := pairChild(t, guest, now)
	defer sibling.Close()

	// The guest is invited the ordinary way…
	invite, err := owner.MintInvite(tid, guest.Device.ID, guest.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guest.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	// …and the person's certificates reach the owner — BOTH of them: in
	// life the guest publishes its own cert into the space at first open
	// and the sibling publishes its own once it converges; here they are
	// fed to the same store the log feeds. Expansion walks member →
	// principal → siblings, so the member's own certificate is the first
	// link of the chain, not a nicety.
	guestCert, ok := guest.ident.certificateFor(guest.Device.ID)
	if !ok {
		t.Fatal("the guest holds no certificate for itself")
	}
	if err := owner.ident.store.AddCertificate(guestCert); err != nil {
		t.Fatal(err)
	}
	sibCert, ok := sibling.ident.certificateFor(sibling.Device.ID)
	if !ok {
		t.Fatal("the sibling holds no certificate for itself")
	}
	if err := owner.ident.store.AddCertificate(sibCert); err != nil {
		t.Fatal(err)
	}

	// The next rotation must carry the sibling.
	sp, _ := owner.spaceForTest(tid)
	owner.mu.Lock()
	owner.expandMembersLocked(sp)
	_, err = owner.Self.RotateEpoch(sp)
	owner.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !sp.HasMember(sibling.Device.ID) {
		t.Fatal("the certified sibling was not carried into the wrap list")
	}

	// Revoke the sibling at its root (the guest holds the root), feed the
	// revocation to the owner, remove+rotate: the expansion must NOT
	// resurrect it.
	if err := guest.RevokeDevice(sibling.Device.ID); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rec := range guest.ks.Revs {
		if rec.Device == sibling.Device.ID {
			rev, err := identity.DecodeRevocation(rec.Frame)
			if err != nil {
				t.Fatal(err)
			}
			if err := owner.ident.store.AddRevocation(rev); err != nil {
				t.Fatal(err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("the revocation was not recorded at the root device")
	}
	sp.RemoveMember(sibling.Device.ID)
	owner.mu.Lock()
	owner.expandMembersLocked(sp)
	owner.mu.Unlock()
	if sp.HasMember(sibling.Device.ID) {
		t.Fatal("expansion resurrected a revoked device — future epochs would reach a dead phone")
	}
}
