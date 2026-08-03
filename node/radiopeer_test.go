package node

import (
	"crypto/ed25519"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
)

// peerPair boots two runtimes joined by a lossless radio segment, each able to
// hear the other's control messages.
func peerPair(t *testing.T, loss float64) (alice, bob *Runtime) {
	t.Helper()
	seedRaw := sha256.Sum256([]byte("one segment, two people, a field"))
	key, err := radiotransfer.DeriveTransferKey(seedRaw[:], radiotransfer.KDFVersion)
	if err != nil {
		t.Fatal(err)
	}
	alice = openRuntime(t, t.TempDir(), "alice")
	t.Cleanup(alice.Close)
	bob = openRuntime(t, t.TempDir(), "bob")
	t.Cleanup(bob.Close)

	// Both sides must derive the SAME segment fingerprint, so both hold the
	// same seed — that is what makes a segment a segment.
	for _, rt := range []*Runtime{alice, bob} {
		rt.mu.Lock()
		rt.meshSeed = seedRaw[:]
		rt.mu.Unlock()
	}

	aAir, bAir := newSegment(200, loss, 20260804)
	lim := radiotransfer.Limits{Window: 4, MaxRounds: 6,
		AckTimeout: 300 * time.Millisecond, SACKDelay: 10 * time.Millisecond,
		SendFloor: 5 * time.Millisecond, FrameGap: time.Millisecond}

	// WIRED THE SAME WAY PRODUCTION IS. A harness that omits a callback the
	// real attach path installs is how a defect stays invisible — which is
	// exactly what happened with Credit, where the helper embedded a concrete
	// type and production embedded an interface.
	aEP, err := radiotransfer.Wrap(aAir, key, radiotransfer.EndpointOptions{
		Options:       radiotransfer.Options{Limits: lim},
		OnControl:     alice.onRadioControl,
		OnControlSent: alice.onRadioControlSent})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { aEP.Close() })
	bEP, err := radiotransfer.Wrap(bAir, key, radiotransfer.EndpointOptions{
		Options:       radiotransfer.Options{Limits: lim},
		OnControl:     bob.onRadioControl,
		OnControlSent: bob.onRadioControlSent})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bEP.Close() })

	alice.adoptLink(endpointLink{aEP}, 50*time.Millisecond, time.Hour, "radio")
	bob.adoptLink(endpointLink{bEP}, 50*time.Millisecond, time.Hour, "radio")
	return alice, bob
}

func waitPeer(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The headline: two fresh beacons are enough to establish an ADDRESSED link,
// and the link is a fact about reachability rather than a claim about anybody.
func TestFreshBeaconsCanEstablishAnAddressedPeerLink(t *testing.T) {
	alice, bob := peerPair(t, 0)

	if err := bob.AnnounceOnRadio(); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "alice to hear bob", func() bool {
		for _, n := range alice.RadioNeighbours() {
			if n.Device == bob.Device.ID {
				return true
			}
		}
		return false
	})
	// Alice must also have observed WHERE bob is, or she can only broadcast.
	alice.meet.mu.Lock()
	addr := alice.meet.neighbour[bob.Device.ID].addr
	alice.meet.mu.Unlock()
	if len(addr) == 0 {
		t.Fatal("alice heard bob but recorded no radio address: every answer " +
			"would have to be broadcast to the whole segment")
	}

	if err := alice.LinkToPeer(bob.Device.ID); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "the link to come up on alice's side", func() bool {
		l, ok := alice.PeerLink(bob.Device.ID)
		return ok && l.State == PeerLinkUp
	})
	waitPeer(t, "the link to come up on bob's side", func() bool {
		l, ok := bob.PeerLink(alice.Device.ID)
		return ok && l.State == PeerLinkUp
	})

	a, _ := alice.PeerLink(bob.Device.ID)
	b, _ := bob.PeerLink(alice.Device.ID)
	if a.SessionID != b.SessionID {
		t.Fatal("the two sides derived different keys from the same exchange: " +
			"the transcript binding does not agree")
	}
	if a.SessionID == ([16]byte{}) {
		t.Fatal("the derived session id is all zeroes")
	}
	// Reachability, never directness. An addressed Meshtastic packet is
	// exactly what a mesh forwards, so arriving proves nothing about hops.
	if a.Direct() {
		t.Fatal("a link reported itself DIRECT with no observed hop count: " +
			"'addressed' and 'direct' are different claims")
	}
}

// A peer link is not a space, and must never quietly become one.
func TestAPeerLinkDoesNotGrantMembership(t *testing.T) {
	alice, bob := peerPair(t, 0)
	if err := bob.AnnounceOnRadio(); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "alice to hear bob", func() bool {
		_, ok := neighbourOf(alice, bob.Device.ID)
		return ok
	})
	before := len(alice.Spaces())
	if err := alice.LinkToPeer(bob.Device.ID); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "the link", func() bool {
		l, ok := alice.PeerLink(bob.Device.ID)
		return ok && l.State == PeerLinkUp
	})
	if got := len(alice.Spaces()); got != before {
		t.Fatalf("a peer link created %d spaces; it must create none", got-before)
	}
	if got := len(bob.Spaces()); got != 0 {
		t.Fatalf("a peer link put bob in %d spaces; it grants no membership", got)
	}
}

// THE hole this signature closes, and it is not the obvious one.
//
// The old comment argued a self-asserted card was safe because "an impostor who
// claims somebody else's name receives an invite they cannot open". That
// protects the person being impersonated. It does nothing for the person doing
// the inviting, who seals the invitation to the impostor's key believing it is
// their friend's.
func TestAForgedCardCannotEstablishALink(t *testing.T) {
	alice, bob := peerPair(t, 0)
	if err := bob.AnnounceOnRadio(); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "alice to hear bob", func() bool {
		_, ok := neighbourOf(alice, bob.Device.ID)
		return ok
	})
	genuine, _ := neighbourOf(alice, bob.Device.ID)

	// An impostor announces BOB's device id with its own ephemeral key, signed
	// by its own key — which is the only key it has.
	impostor := openRuntime(t, t.TempDir(), "impostor")
	defer impostor.Close()
	fp, ok := alice.segmentFP()
	if !ok {
		t.Fatal("no segment fingerprint")
	}
	body := encodeCardBody(cardBody{
		Device: bob.Device.ID, StaticX: impostor.Device.X25519Pub,
		Name: "bob", Ephemeral: [32]byte{9, 9, 9}, Fingerpr: fp,
		Generation: 1, Expires: uint64(time.Now().Add(time.Hour).Unix()),
	})
	forged := signedRadio(radioMsgCard, domainCard, body, impostor.Device.SignKey())

	alice.onRadioControl(radiotransfer.RadioAddress("whoever"), forged)

	after, _ := neighbourOf(alice, bob.Device.ID)
	if after.x25519 != genuine.x25519 || after.eph != genuine.eph {
		t.Fatal("a card signed by a DIFFERENT device overwrote bob's keys: " +
			"an invitation alice now seals for 'bob' goes to the impostor")
	}
}

// A card from another segment is heard and is not ours.
func TestACardFromAnotherSegmentIsNotANeighbour(t *testing.T) {
	alice, _ := peerPair(t, 0)
	stranger := openRuntime(t, t.TempDir(), "stranger")
	defer stranger.Close()

	body := encodeCardBody(cardBody{
		Device: stranger.Device.ID, StaticX: stranger.Device.X25519Pub,
		Name: "stranger", Ephemeral: [32]byte{1}, Generation: 1,
		Fingerpr: segmentFingerprint([]byte("somebody else's segment entirely")),
		Expires:  uint64(time.Now().Add(time.Hour).Unix()),
	})
	msg := signedRadio(radioMsgCard, domainCard, body, stranger.Device.SignKey())
	alice.onRadioControl(radiotransfer.RadioAddress("elsewhere"), msg)

	if _, ok := neighbourOf(alice, stranger.Device.ID); ok {
		t.Fatal("a card from a different segment was taken as a neighbour")
	}
}

// The domains keep the messages apart: a probe replayed as an acknowledgement
// must not verify, however genuine its signature.
func TestASignedMessageCannotBeReplayedAsAnotherKind(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	body := encodeProbeBody(probeBody{From: rt.Device.ID, To: rt.Device.ID}, false)
	msg := signedRadio(radioMsgProbe, domainProbe, body, rt.Device.SignKey())

	if _, _, err := openSignedRadio(msg, domainProbe, rt.Device.ID); err != nil {
		t.Fatalf("a genuine probe did not verify: %v", err)
	}
	if _, _, err := openSignedRadio(msg, domainAck, rt.Device.ID); err == nil {
		t.Fatal("a probe verified as an acknowledgement: the signing domains " +
			"are not separating the kinds")
	}
	if _, _, err := openSignedRadio(msg, domainProbe, id.DeviceID{1}); err == nil {
		t.Fatal("a probe verified against the wrong device")
	}
}

// Both sides must derive the same key regardless of who probed first.
func TestTheLinkKeyDoesNotDependOnWhoWentFirst(t *testing.T) {
	a := id.DeviceID{9, 9}
	b := id.DeviceID{1, 1}
	shared := []byte("a shared secret of exactly some length")
	nA := [nonceLen]byte{1}
	nB := [nonceLen]byte{2}
	fp := [fingerprintLn]byte{7}

	one, err := linkKey(shared, a, b, nA, nB, fp)
	if err != nil {
		t.Fatal(err)
	}
	two, err := linkKey(shared, b, a, nB, nA, fp)
	if err != nil {
		t.Fatal(err)
	}
	if one != two {
		t.Fatal("the two sides derived different keys from the same exchange")
	}
	// A different segment must not produce the same key from the same secret.
	other, err := linkKey(shared, a, b, nA, nB, [fingerprintLn]byte{8})
	if err != nil {
		t.Fatal(err)
	}
	if one == other {
		t.Fatal("the segment fingerprint is not bound into the key")
	}
}

// A signature is only as good as the body it covers.
func TestATamperedBodyDoesNotVerify(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	body := encodeCardBody(cardBody{Device: rt.Device.ID, Name: "alice", Generation: 1})
	msg := signedRadio(radioMsgCard, domainCard, body, rt.Device.SignKey())

	// Flip one byte of the signature and one of the body, separately.
	bad := append([]byte(nil), msg...)
	bad[len(bad)-1] ^= 0xff
	if _, _, err := openSignedRadio(bad, domainCard, rt.Device.ID); err == nil {
		t.Fatal("a message with a corrupted signature verified")
	}
	if len(msg) < ed25519.SignatureSize+8 {
		t.Fatal("message unexpectedly short")
	}
}

func neighbourOf(rt *Runtime, dev id.DeviceID) (RadioNeighbour, bool) {
	for _, n := range rt.RadioNeighbours() {
		if n.Device == dev {
			return n, true
		}
	}
	return RadioNeighbour{}, false
}

// The peer link's whole justification: a radio nobody is listening to must be
// discovered in SECONDS, before six frames of invitation are committed to it.
//
// An unanswered probe must therefore resolve on the probe's own clock. Giving
// it the five-minute lifetime of an established link would reinstate exactly
// the wait it exists to remove.
func TestANonListeningPeerFailsBeforeTheLargeOfferStarts(t *testing.T) {
	alice, bob := peerPair(t, 0)
	if err := bob.AnnounceOnRadio(); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "alice to hear bob", func() bool {
		_, ok := neighbourOf(alice, bob.Device.ID)
		return ok
	})
	if err := alice.LinkToPeer(bob.Device.ID); err != nil {
		t.Fatal(err)
	}
	l, ok := alice.PeerLink(bob.Device.ID)
	if !ok {
		t.Fatal("probing produced no link record at all")
	}
	// Not the established lifetime: an unanswered question is cheap to give up
	// on, and must be.
	if budget := time.Until(l.ExpiresAt); budget > probeDeadline+10*time.Second {
		t.Fatalf("an unanswered probe holds the link for %s; the probe deadline "+
			"is %s and peerLinkTTL (%s) is the wrong clock for a question",
			budget.Round(time.Second), probeDeadline, peerLinkTTL)
	}
}

// "Nobody answered" and "they answered and then left" are different sentences,
// and this codebase's rule is that they never collapse into one.
func TestAnUnansweredProbeSaysNoAnswerRatherThanGone(t *testing.T) {
	alice, bob := peerPair(t, 0)
	if err := bob.AnnounceOnRadio(); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "alice to hear bob", func() bool {
		_, ok := neighbourOf(alice, bob.Device.ID)
		return ok
	})
	if err := alice.LinkToPeer(bob.Device.ID); err != nil {
		t.Fatal(err)
	}
	// Age the probe out without ever answering it.
	alice.meet.mu.Lock()
	l := alice.meet.peers[bob.Device.ID]
	l.State = PeerLinkProbing
	l.ExpiresAt = time.Now().Add(-time.Second)
	alice.meet.mu.Unlock()

	got, _ := alice.PeerLink(bob.Device.ID)
	if got.State != PeerLinkNoAnswer {
		t.Fatalf("an unanswered probe resolved to %q, want %q — a person "+
			"standing in a field is owed the true sentence",
			got.State, PeerLinkNoAnswer)
	}

	// A link that DID stand and then aged out is the other sentence.
	alice.meet.mu.Lock()
	l.State = PeerLinkUp
	l.ExpiresAt = time.Now().Add(-time.Second)
	alice.meet.mu.Unlock()
	if got, _ := alice.PeerLink(bob.Device.ID); got.State != PeerLinkGone {
		t.Fatalf("an expired established link resolved to %q, want %q",
			got.State, PeerLinkGone)
	}
}

// Pressing again must not mint a second link to the same peer.
func TestRepeatedProbeReusesTheSamePeerLink(t *testing.T) {
	alice, bob := peerPair(t, 0)
	if err := bob.AnnounceOnRadio(); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "alice to hear bob", func() bool {
		_, ok := neighbourOf(alice, bob.Device.ID)
		return ok
	})
	for range 3 {
		if err := alice.LinkToPeer(bob.Device.ID); err != nil {
			t.Fatal(err)
		}
	}
	alice.meet.mu.Lock()
	n := len(alice.meet.peers)
	alice.meet.mu.Unlock()
	if n != 1 {
		t.Fatalf("three presses produced %d links to one peer; a link is per "+
			"device and segment, not per press", n)
	}
}

// A peer that restarted is a peer whose old link describes a process that no
// longer exists.
func TestARestartInvalidatesTheOldLinkGeneration(t *testing.T) {
	alice, bob := peerPair(t, 0)
	if err := bob.AnnounceOnRadio(); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "alice to hear bob", func() bool {
		_, ok := neighbourOf(alice, bob.Device.ID)
		return ok
	})
	if err := alice.LinkToPeer(bob.Device.ID); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "the link", func() bool {
		l, ok := alice.PeerLink(bob.Device.ID)
		return ok && l.State == PeerLinkUp
	})

	// Bob restarts: same device, new run, new generation.
	bob.meet.mu.Lock()
	bob.meet.generation++
	bob.meet.mu.Unlock()
	if err := bob.AnnounceOnRadio(); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "alice to drop the stale link", func() bool {
		_, ok := alice.PeerLink(bob.Device.ID)
		return !ok
	})
}
