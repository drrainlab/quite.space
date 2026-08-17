package node

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/bundle"
)

// waitHoldEmpty gives the coalesced pass time to run, since the trigger is
// asynchronous by construction (the hook fires under r.mu and the pass needs
// it). A timeout here is a real failure: nothing about this should be slow.
func waitHoldEmpty(t *testing.T, dir string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	last := -1
	for time.Now().Before(deadline) {
		items, err := reopenHold(t, dir).List()
		if err != nil {
			t.Fatalf("list hold: %v", err)
		}
		last = len(items)
		if last == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("hold still holds %d item(s), want %d", last, want)
}

// A held frame becomes admissible when the policy revision authorising its
// author is APPLIED — and this must work on the path where no local authoring
// function was ever called, which is the whole reason the hook lives at the
// absorb funnel.
func TestPolicyRevisionReconsidersHeldIngress(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "owner")
	defer rt.Close()
	tid, err := rt.CreateSpaceWithOptions("a curated room", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic, Join: terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.RevisePolicy(tid, PolicyDelta{Publish: ptr("curated")}); err != nil {
		t.Fatal(err)
	}

	// A contributor the space does not authorise YET — but a CERTIFIED one,
	// with the certificate riding the same item (decision C), so the policy
	// layer is the one answering.
	d := newCertifiedAuthor(t, tid)
	cert := d.certFrame(t, false)
	frame := d.nextText(t, "authorised a moment from now")
	item := bundle.Encode(tid, [][]byte{cert, frame})

	held, err := rt.takeIngressCustody([][]byte{item}, storage.IngressRelay)
	if err != nil {
		t.Fatal(err)
	}
	if _, release := rt.applyHeldRelayItem(nil, held[0]); release {
		t.Fatal("a frame refused only by the current projection was let go")
	}

	// THE CONTROL EVENT. RevisePolicy emits it; what matters is that the hook
	// runs when it is APPLIED, so the same wiring serves a revision arriving
	// over a relay.
	w := terminals.WriterBinding{Principal: d.prin, Device: d.dev}
	if err := rt.RevisePolicy(tid, PolicyDelta{AddCurator: &w}); err != nil {
		t.Fatal(err)
	}

	waitHoldEmpty(t, dir, 0)
	sp, _ := rt.spaceForTest(tid)
	if !sp.Log.Has(id.EventIDOf(frame)) {
		t.Fatal("the hold was released without the journal owning the frame")
	}
}

// The startup pass must see a FULLY reconstructed world: identity, every
// space replayed and attached, membership and policy in place. A prerequisite
// applied before the crash never arrives again as an event, so if this pass
// runs too early — or not at all — the frame waits for a trigger that cannot
// come.
func TestStartupReconsiderRunsAfterAllAdmissionStateIsReconstructed(t *testing.T) {
	dir := t.TempDir()
	tid := func() id.TerminalID {
		rt := openRuntime(t, dir, "owner")
		defer rt.Close()
		tid, err := rt.CreateSpaceWithOptions("a curated room", CreateOptions{
			Policy: terminals.SpacePolicy{
				Visibility: terminals.VisibilityPublic, Join: terminals.JoinOpen,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.RevisePolicy(tid, PolicyDelta{Publish: ptr("curated")}); err != nil {
			t.Fatal(err)
		}
		d := newCertifiedAuthor(t, tid)
		cert := d.certFrame(t, false)
		frame := d.nextText(t, "held across the restart")
		// The authorising revision is applied FIRST, and only then are the
		// bytes taken into custody without being judged — the crash shape
		// where the trigger was missed.
		w := terminals.WriterBinding{Principal: d.prin, Device: d.dev}
		if err := rt.RevisePolicy(tid, PolicyDelta{AddCurator: &w}); err != nil {
			t.Fatal(err)
		}
		hold, err := rt.ingressHold()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := hold.Put(bundle.Encode(tid, [][]byte{cert, frame}),
			storage.HeldIngressMeta{ReceivedAt: 1, Source: storage.IngressRelay}); err != nil {
			t.Fatal(err)
		}
		return tid
	}()

	// THE RESTART. Nothing new arrives; the prerequisite is already in the log.
	rt2 := openRuntime(t, dir, "owner")
	defer rt2.Close()
	select {
	case <-rt2.startupReconsidered:
	case <-time.After(15 * time.Second):
		t.Fatal("the startup reconsider never finished")
	}
	items, err := reopenHold(t, dir).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("the startup pass left %d held item(s) whose prerequisite was "+
			"already applied before the crash", len(items))
	}
	sp, ok := rt2.spaceForTest(tid)
	if !ok {
		t.Fatal("lost the space")
	}
	if sp.Log.Len() == 0 {
		t.Fatal("nothing was applied")
	}
}

// THE CASE MD-0b EXISTS FOR: a peer's first message arrives while its
// certificate is not yet known. The frame must be HELD rather than dropped,
// and the certificate becoming trust is what releases it.
//
// The certificate is added to the trust store directly rather than carried in
// a bundle, and that is deliberate: at the bundle layer a certificate's
// payload is still epoch-encrypted, and a device's certificate sits BEHIND the
// epoch and manifest it wrote first — the chain-order deadlock recorded in
// node.Open. This test pins the custody half, which is what MD-0b owns; the
// ordering half is that decision.
func TestCertificateAdmissionReconsidersHeldIngress(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("certificate not yet known")
	if err != nil {
		t.Fatal(err)
	}

	// A real principal and a real device, so the certificate is a real one.
	prin, err := identity.NewPrincipal(identity.NewRand())
	if err != nil {
		t.Fatal(err)
	}
	dev, err := identity.NewDevice(identity.NewRand())
	if err != nil {
		t.Fatal(err)
	}
	peer := &testAuthor{priv: ed25519.NewKeyFromSeed(dev.Seed()), dev: dev.ID,
		prin: prin.ID, term: tid}
	frame := peer.frameAt(t, "my certificate is one round behind", 1)
	eid := id.EventIDOf(frame)

	rt.mu.Lock()
	rt.identityGate = true // armed here; still off in node.Open (see there)
	rt.mu.Unlock()

	held, err := rt.takeIngressCustody([][]byte{bundle.Encode(tid, [][]byte{frame})},
		storage.IngressRelay)
	if err != nil {
		t.Fatal(err)
	}
	if _, release := rt.applyHeldRelayItem(nil, held[0]); release {
		t.Fatal("an uncertified device's frame was let go — this is the loss " +
			"MD-0b exists to prevent")
	}
	sp, _ := rt.spaceForTest(tid)
	if sp.Log.Has(eid) {
		t.Fatal("admitted while the certificate was unknown — the gate is not armed")
	}

	// THE CERTIFICATE BECOMES TRUST, and then admission state has changed —
	// which is the whole trigger, whatever door the certificate came through.
	if err := rt.ident.store.AddCertificate(prin.Certify(dev, 1, 0)); err != nil {
		t.Fatalf("the certificate did not verify: %v", err)
	}
	rt.admissionStateChanged()

	waitHoldEmpty(t, dir, 0)
	if !sp.Log.Has(eid) {
		t.Fatal("the held frame was released without being applied")
	}
}

// A held frame may itself be a control event, so admission of it changes
// admission state again. That must coalesce, never recurse out of a reducer.
func TestReconsiderationDoesNotReenterItself(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	// Ask for a reconsideration from inside a reconsideration: with a dirty
	// flag this schedules exactly one more pass; with a recursive call it
	// would grow the stack until it fell over.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			rt.admissionStateChanged()
		}
		close(done)
	}()
	<-done

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rt.holdMu.Lock()
		running := rt.reconsiderRunning
		rt.holdMu.Unlock()
		if !running {
			return // it drained and stopped: coalesced, not recursive
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("reconsideration never settled — a coalesced pass must drain")
}

// The schema list is a gate on WHETHER TO ASK, so an omission here is a frame
// that waits forever. It is asserted rather than trusted.
func TestAdmissionRelevantSchemasCoverIdentityAndAuthorisation(t *testing.T) {
	for _, s := range []string{
		schemas.DeviceCertified, schemas.DeviceRevoked,
		schemas.ManifestUpdated,
		schemas.MemberJoined, schemas.MemberLeft, schemas.MemberAdded,
	} {
		if !admissionRelevantSchema(s) {
			t.Errorf("%s changes admission state but triggers no reconsideration", s)
		}
	}
	if admissionRelevantSchema(schemas.MessageText) {
		t.Error("an ordinary message must not trigger a reconsideration pass")
	}
}
