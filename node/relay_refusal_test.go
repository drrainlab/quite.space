package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// THE HEADLINE FOR MD-0b DECISION C, and the successor of the 2026-08-13
// measurement that used to live here. That measurement asked "does a refused
// frame come back over the relay?" and the answer was NO — Collect is
// destructive, so a refusal was a loss. The custody layer closed the loss,
// and then exposed the deeper thing: a certificate sealed at seq 3 could
// never release the frames at seq 1-2 that needed it. Decision C is the fix
// this test pins end to end:
//
//	certificate = admission proof (plaintext, learned at the log's door,
//	              free of chain ordering)
//	            + log record     (applied in ordinary order, for
//	              convergence and audit)
//
// One stock relay in the middle, and the relay understands none of it — it
// stores opaque items under capabilities, which is itself part of what is
// asserted here (TestRelayNeedNotUnderstandBootstrapCertificate is this same
// test, seen from the relay's side).
//
// The history bob pulls has alice's certificate BEHIND her epoch and
// manifest in chain order — the exact shape that deadlocked. With the gate
// ON for every runtime in this test, the message must still arrive.
func TestAnUnknownCertifiedDeviceCanBootstrapItsOwnFirstFrame(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	setPersonalRelay(t, alice, addr)
	tid, err := alice.CreateSpace("the bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	// Written BEFORE bob exists, so his replica meets a fully unknown device
	// whose certificate sits mid-chain, not at its head.
	if _, err := alice.Say(tid, "you have never heard of me", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(tid, 1, 1, addr)
	if err != nil {
		t.Fatal(err)
	}

	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	setPersonalRelay(t, bob, addr)
	req, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if countMsg(t, bob, tid, "you have never heard of me") >= 1 {
			// And the second half of the promise: the device was actually
			// LEARNED, not waved through — one object, two roles, both
			// delivered.
			if _, known := bob.ident.certificateFor(alice.Device.ID); !known {
				t.Fatal("the message arrived but the device was never learned — " +
					"admission let something through without its proof")
			}
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("an unknown certified device could not bootstrap its first frame: " +
		"the message never arrived with the identity gate on")
}

// A bootstrap proof that does not verify must open nothing. The frame
// carrying it is signed by a real device key, the schema is right, and the
// payload is shaped like a certificate — only the root signature is wrong.
// Nothing may be learned from it, and the device's data stays held.
func TestAnInvalidBootstrapCertificateNeverOpensAdmission(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("forged proof")
	if err != nil {
		t.Fatal(err)
	}
	a := newCertifiedAuthor(t, tid)
	msg := a.nextText(t, "let me in")
	cert := a.certFrame(t, true /* corrupt the root signature */)

	held, err := rt.takeIngressCustody([][]byte{bundleOf(tid, msg, cert)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, release := rt.applyHeldRelayItem(nil, held[0]); release {
		t.Fatal("an item whose only proof is forged was released")
	}
	sp, _ := rt.spaceForTest(tid)
	if sp.Log.Has(id.EventIDOf(msg)) {
		t.Fatal("a message was admitted on a certificate that does not verify")
	}
	if _, known := rt.ident.certificateFor(a.dev); known {
		t.Fatal("a forged certificate became trust")
	}
}

// A valid certificate binds ONE principal to a device. A device presenting
// somebody else's identity alongside its own genuine certificate is not held
// pending better evidence — no future control event makes the claim true, so
// this is one of the few refusals that deletes.
func TestBootstrapCertificateCannotBindAnotherPrincipal(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("stolen name")
	if err != nil {
		t.Fatal(err)
	}
	a := newCertifiedAuthor(t, tid)
	cert := a.certFrame(t, false) // genuine: root A binds device A
	a.prin = id.PrincipalID{0xEE} // and now the device claims to be somebody else
	msg := a.nextText(t, "I am whoever I say I am")

	held, err := rt.takeIngressCustody([][]byte{bundleOf(tid, cert, msg)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, release := rt.applyHeldRelayItem(nil, held[0]); !release {
		t.Fatal("a principal mismatch is permanent and must not be held")
	}
	sp, _ := rt.spaceForTest(tid)
	if sp.Log.Has(id.EventIDOf(msg)) {
		t.Fatal("a frame claiming another principal was admitted")
	}
	mismatch := false
	for _, ref := range rt.IngressRefusals() {
		if ref.Reason == "principal_certificate_mismatch" {
			mismatch = true
		}
	}
	if !mismatch {
		t.Fatal("the refusal was not diagnosed as a principal mismatch")
	}
}

// THE PRIVACY HALF OF DECISION C, stated honestly. A relay already sees
// envelope HEADERS — principal and device ride cleartext on every frame
// (ADR-005 seals payloads, not headers; the bundle "adds integrity, not
// confidentiality"). What this test pins is the DELTA: a plaintext
// certificate payload must add no identity linkage a header-reading relay
// did not already have. Every (principal, device) binding readable out of a
// certificate in the bundle is already readable off an envelope header of
// the same bundle — so decision C required no new relay visibility and no
// relay protocol awareness, which is the invariant:
//
//	a device certificate must be visible to the intended peer's admission
//	BEFORE semantic admission, and must require NOTHING from a relay.
func TestBootstrapProofAddsNothingRelayVisible(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("what the relay could read")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Say(tid, "sealed like anything else", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)
	frames := sp.Log.FramesInRange(rt.Device.ID, 1, 1000)
	if len(frames) == 0 {
		t.Fatal("nothing on the chain")
	}

	type binding struct {
		P id.PrincipalID
		D id.DeviceID
	}
	fromHeaders := map[binding]bool{}
	fromCerts := map[binding]bool{}
	sawCert, sawSealed := false, false
	for _, f := range frames {
		env, err := signal.Decode(f)
		if err != nil {
			t.Fatal(err)
		}
		fromHeaders[binding{env.Principal, env.Device}] = true
		switch env.Schema {
		case schemas.DeviceCertified:
			c, err := identity.DecodeCertificate(env.Payload)
			if err != nil {
				t.Fatalf("a certificate payload must decode as a relay-opaque "+
					"blob decodes — plaintext by design: %v", err)
			}
			sawCert = true
			fromCerts[binding{c.Principal, c.Device}] = true
		case schemas.MessageText:
			// The message payload stays SEALED: plaintext proof must not have
			// dragged plaintext content along with it.
			if env.PayloadEncoding != signal.PayloadEncrypted {
				t.Fatal("an ordinary message travelled unsealed")
			}
			sawSealed = true
		}
	}
	if !sawCert || !sawSealed {
		t.Fatalf("the chain lacks its shape: cert=%v sealedMsg=%v", sawCert, sawSealed)
	}
	for b := range fromCerts {
		if !fromHeaders[b] {
			t.Fatalf("a certificate exposed a binding no envelope header already "+
				"showed: %x→%x", b.P[:4], b.D[:4])
		}
	}
}

// The deadlock's minimal form, pinned forever: data first, certificate after,
// all in one item. Chain order must be an ordering concern, never the thing
// the security model hangs from.
func TestCertificateEventMayArriveAfterDataWithoutDeadlock(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("data before proof")
	if err != nil {
		t.Fatal(err)
	}
	a := newCertifiedAuthor(t, tid)
	one := a.nextText(t, "first")
	two := a.nextText(t, "second")
	cert := a.certFrame(t, false) // seq 3: BEHIND the data that needs it

	held, err := rt.takeIngressCustody([][]byte{bundleOf(tid, one, two, cert)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = rt.applyHeldRelayItem(nil, held[0])

	// The proof was learned at the door regardless of its chain position, so
	// the coalesced reconsideration must drain everything.
	waitHoldEmpty(t, dir, 0)
	sp, _ := rt.spaceForTest(tid)
	for i, f := range [][]byte{one, two, cert} {
		if !sp.Log.Has(id.EventIDOf(f)) {
			t.Fatalf("frame %d never applied — the chain deadlocked", i+1)
		}
	}
}
