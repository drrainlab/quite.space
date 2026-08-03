package node

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// A targeted invitation must NOT mint a bearer pass.
//
// A pass exists to solve the problem of an unknown future redeemer. Pressing
// somebody's name in a neighbour list means you hold their device id and the
// key on the card they signed — that problem does not exist, and buying three
// store-and-forward legs to solve it would cost the thing this carrier has
// least of.
func TestATargetedInvitationDoesNotRequireABearerMailbox(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	tid, err := rt.CreateSpace("in the car park")
	if err != nil {
		t.Fatal(err)
	}
	dev := id.DeviceID{7, 7, 7}
	if err := rt.recordInvitation(InvitationRecord{
		ID: newInvitationID(), Mode: InvitationTargeted, Space: tid.Hex(),
		Target: hex.EncodeToString(dev[:]), IssuedAt: time.Now().Unix(),
		State: InvitationOffered,
	}); err != nil {
		t.Fatal(err)
	}

	all := rt.Invitations()
	if len(all) != 1 || all[0].Mode != InvitationTargeted {
		t.Fatalf("the journal holds %d invitations, want one targeted", len(all))
	}
	if all[0].PassID != "" || all[0].Hint != "" {
		t.Fatalf("a targeted invitation carried pass %q and hint %q; it names a "+
			"device and has no mailbox anywhere", all[0].PassID, all[0].Hint)
	}
	// And it must not show up as a set of live words, because it has none.
	if got := rt.QuickLinks(); len(got) != 0 {
		t.Fatalf("a targeted invitation appeared as %d quick links", len(got))
	}
}

// The record is the fix, and a record that dies on restart is not one.
//
// The memory-only map this replaces is why a second press opened another
// empty room: it forgot everything, so the first press after a restart looked
// like a first press.
func TestATargetedInvitationSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpace("in the car park")
	if err != nil {
		t.Fatal(err)
	}
	dev := id.DeviceID{3, 1, 4}
	if err := rt.recordInvitation(InvitationRecord{
		ID: newInvitationID(), Mode: InvitationTargeted, Space: tid.Hex(),
		Target: hex.EncodeToString(dev[:]), IssuedAt: time.Now().Unix(),
		State: InvitationOffered,
	}); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	again := openRuntime(t, dir, "alice")
	defer again.Close()
	rec, ok := again.liveTargetedInvitation(dev)
	if !ok {
		t.Fatal("the invitation did not survive a restart, so pressing again " +
			"would open a second room — the six-empty-spaces bug, merely rarer")
	}
	if rec.Space != tid.Hex() {
		t.Fatalf("the restored invitation names space %s, want %s",
			rec.Space, tid.Hex())
	}
}

// A withdrawn invitation is not a live way in, whichever mode carried it.
func TestWithdrawingAnInvitationClosesIt(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("closing time")
	if err != nil {
		t.Fatal(err)
	}
	dev := id.DeviceID{2, 7}
	invID := newInvitationID()
	if err := rt.recordInvitation(InvitationRecord{
		ID: invID, Mode: InvitationTargeted, Space: tid.Hex(),
		Target: hex.EncodeToString(dev[:]), IssuedAt: time.Now().Unix(),
		State: InvitationOffered,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := rt.liveTargetedInvitation(dev); !ok {
		t.Fatal("the invitation was not live to begin with")
	}
	if err := rt.WithdrawInvitation(invID); err != nil {
		t.Fatal(err)
	}
	if _, ok := rt.liveTargetedInvitation(dev); ok {
		t.Fatal("a withdrawn invitation is still offered as a way in")
	}
}

// An existing quicklinks.json must keep working, and must not be counted
// twice when it is folded into the one journal.
func TestAnOlderQuickLinkFileFoldsIntoTheOneJournal(t *testing.T) {
	st := quickLinkState{Records: []QuickLinkRecord{
		{Hint: "aabb", Space: "cc", PassID: "p1", IssuedAt: 1},
		{Hint: "ccdd", Space: "cc", PassID: "p2", IssuedAt: 2, Withdrawn: true},
	}}
	once := migrateInvitations(st)
	if len(once.Invitations) != 2 {
		t.Fatalf("folded %d records, want 2", len(once.Invitations))
	}
	if once.Invitations[1].State != InvitationWithdrawn {
		t.Fatal("a withdrawn record did not fold as withdrawn")
	}
	// Idempotent: loading again must not duplicate. The fold happens on every
	// load, so a second copy would grow the journal without bound.
	twice := migrateInvitations(once)
	if len(twice.Invitations) != 2 {
		t.Fatalf("folding twice produced %d invitations, want 2 — the journal "+
			"would grow on every load", len(twice.Invitations))
	}
}

// A radio rendezvous holds nothing for somebody who is not here, and that must
// never be confused with a legacy record whose address is simply missing.
func TestARadioRendezvousIsNotRelayShaped(t *testing.T) {
	if custodyOf("127.0.0.1:9000") != custodyHeld {
		t.Fatal("a relay address did not read as held custody")
	}
	if custodyOf("") != custodyHeld {
		t.Fatal("an empty (legacy) address must stay relay-shaped, or every " +
			"record minted before RR-0 stops working")
	}
	if custodyOf("radio:abc123") != custodyLiveOnly {
		t.Fatal("a radio address read as held custody: a decision would be " +
			"sealed onto a relay the guest never looks at, and reported as sent")
	}
	if relayShaped("radio:abc123") {
		t.Fatal("a radio address is relay-shaped, so the legacy fallback would " +
			"fire on it")
	}
}
