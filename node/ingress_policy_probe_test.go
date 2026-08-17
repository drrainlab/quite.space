package node

import (
	"errors"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
)

// THE PROBE BEHIND ONE VERDICT (MD-0b).
//
// The question is not "does this error look permanent" but the only one that
// decides whether bytes may be deleted:
//
//	can the arrival of one more VALID control event change the decision
//	about these EXACT signed bytes?
//
// If yes, the refusal is knowledge about the current projection rather than
// proof of impossibility, and deleting the frame recreates TR-0 one layer up:
// the dependency is no longer certificate → message but membership → message.
//
// This is a measurement, and it asserts what it measured so a later change to
// the policy layer cannot silently move the answer.
func TestMessageBeforeMembershipRevisionIsEventuallyAdmitted(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "owner")
	defer rt.Close()
	// Created public, because the private/public boundary is immutable by
	// design — a curated room is born one, it does not become one.
	tid, err := rt.CreateSpaceWithOptions("a curated room", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic, Join: terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.RevisePolicy(tid, PolicyDelta{Publish: ptr("curated")}); err != nil {
		t.Fatalf("make the space curated: %v", err)
	}
	sp, ok := rt.spaceForTest(tid)
	if !ok {
		t.Fatal("lost the space")
	}

	// A contributor whose authorisation has NOT yet arrived here, writing a
	// perfectly valid, correctly signed first frame. CERTIFIED, and the
	// certificate pre-taught: this probe measures the POLICY refusal, and the
	// identity gate now runs first — an uncertified author would be answered
	// by the wrong layer.
	d := newCertifiedAuthor(t, tid)
	if err := rt.ident.store.AddCertificate(d.root.Certify(d.device, 1, 0)); err != nil {
		t.Fatal(err)
	}
	frame := d.nextText(t, "I was added, but you have not heard yet")

	if _, err := sp.Log.Ingest(frame); !errors.Is(err, terminals.ErrNotAuthorized) {
		t.Fatalf("expected the curated gate to refuse an unlisted writer, got %v", err)
	}
	if sp.Log.Has(id.EventIDOf(frame)) {
		t.Fatal("refused and yet stored")
	}

	// NOW THE CONTROL EVENT ARRIVES: the owner authorises exactly that
	// (principal, device). Nothing about the frame changes — same bytes, same
	// signature, same EventID.
	w := terminals.WriterBinding{Principal: d.prin, Device: d.dev}
	if err := rt.RevisePolicy(tid, PolicyDelta{AddCurator: &w}); err != nil {
		t.Fatalf("authorise the writer: %v", err)
	}

	if _, err := sp.Log.Ingest(frame); err != nil {
		t.Fatalf("MEASUREMENT: the same bytes are STILL refused after the "+
			"authorising revision (%v) — only then is policy_refused terminal", err)
	}
	if !sp.Log.Has(id.EventIDOf(frame)) {
		t.Fatal("accepted without durable ownership")
	}
	// MEASURED: ErrNotAuthorized is knowledge about the CURRENT projection,
	// not proof of impossibility. So it must be HOLD, and capacity is the
	// answer to the denial-of-service worry — never turning a temporary
	// unknown into a permanent loss.
}

// The same question for a freeze, which has its own answer because a freeze
// may or may not be reversible by design.
func TestWhetherAFrozenSpaceCanEverAdmitTheSameBytes(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "owner")
	defer rt.Close()
	tid, err := rt.CreateSpaceWithOptions("a room that stops", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic, Join: terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sp, ok := rt.spaceForTest(tid)
	if !ok {
		t.Fatal("lost the space")
	}
	yes, no := true, false
	if err := rt.RevisePolicy(tid, PolicyDelta{Frozen: &yes}); err != nil {
		t.Fatalf("freeze: %v", err)
	}

	d := newCertifiedAuthor(t, tid)
	if err := rt.ident.store.AddCertificate(d.root.Certify(d.device, 1, 0)); err != nil {
		t.Fatal(err)
	}
	frame := d.nextText(t, "written while the room was frozen")
	if _, err := sp.Log.Ingest(frame); !errors.Is(err, terminals.ErrSpaceFrozen) {
		t.Fatalf("expected a frozen space to refuse, got %v", err)
	}

	// Can the freeze be lifted at all? If the owner cannot unfreeze, then no
	// future valid state admits these bytes and REJECT is correct. If it can,
	// the refusal is projection state like the one above.
	unfreezeErr := rt.RevisePolicy(tid, PolicyDelta{Frozen: &no})
	if unfreezeErr != nil {
		t.Logf("MEASURED: a freeze cannot be lifted (%v) — space_frozen is "+
			"terminal for these bytes", unfreezeErr)
		return
	}
	_, err = sp.Log.Ingest(frame)
	if err == nil && sp.Log.Has(id.EventIDOf(frame)) {
		t.Log("MEASURED: unfreezing admits the very same bytes — space_frozen " +
			"is TRANSIENT and must be HOLD")
		return
	}
	if errors.Is(err, eventlog.ErrChainForked) {
		t.Fatalf("the probe forked the chain rather than measuring: %v", err)
	}
	t.Logf("MEASURED: unfreeze succeeded but the same bytes are still refused "+
		"(%v) — space_frozen is terminal", err)
}
