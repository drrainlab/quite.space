package node

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

func decodeB64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// THE run that failed in a car park, in one test.
//
// Alice presses Bob's name. Nothing is granted yet. Bob sees the offer and
// answers it. Only then does Alice add him and rotate — and only then does
// he actually join.
func TestTwoPeopleStartALineOverTheRadioInFourMessages(t *testing.T) {
	alice, bob := peerPair(t, 0)

	if err := bob.AnnounceOnRadio(); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "alice to hear bob", func() bool {
		_, ok := neighbourOf(alice, bob.Device.ID)
		return ok
	})

	tid, err := alice.OfferLineOverRadio(bob.Device.ID)
	if err != nil {
		t.Fatal(err)
	}

	// THE point of the whole gate: offering grants NOTHING.
	assertNoPhantomMember(t, alice, tid, "immediately after offering")

	waitPeer(t, "the offer to reach bob", func() bool {
		return len(bob.RadioOffers()) > 0
	})
	offers := bob.RadioOffers()
	if offers[0].Space != tid {
		t.Fatalf("bob was offered space %s, alice opened %s",
			offers[0].Space.Hex()[:8], tid.Hex()[:8])
	}

	if err := bob.AcceptRadioLine(offers[0].ID); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "bob to join", func() bool {
		bob.mu.Lock()
		_, in := bob.spaces[tid]
		bob.mu.Unlock()
		return in
	})
	// And only NOW is he a member on alice's side too.
	waitPeer(t, "alice to record the acceptance", func() bool {
		for _, rec := range alice.Invitations() {
			if rec.Space == tid.Hex() && rec.State == InvitationAccepted {
				return true
			}
		}
		return false
	})
	if !hasMember(alice, tid, bob) {
		t.Fatal("bob joined but alice does not hold him as a member")
	}
}

// An offer nobody ever accepts must leave the space exactly as it was.
//
// The one-shot form could not do this: MintInvite adds the member and rotates
// the epoch AT MINT (node/node.go:718-724), so a person who never answered —
// common, when failure takes minutes — left a space carrying somebody who was
// not there, and a rotation nobody needed.
func TestAnOfferNeverAcceptedDoesNotLeaveAPhantomMember(t *testing.T) {
	alice, bob := peerPair(t, 0)
	if err := bob.AnnounceOnRadio(); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "alice to hear bob", func() bool {
		_, ok := neighbourOf(alice, bob.Device.ID)
		return ok
	})
	tid, err := alice.OfferLineOverRadio(bob.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Bob receives it and simply never answers, which is what walking away
	// looks like from here.
	waitPeer(t, "the offer to arrive", func() bool {
		return len(bob.RadioOffers()) > 0
	})
	time.Sleep(300 * time.Millisecond)
	assertNoPhantomMember(t, alice, tid, "after an offer nobody accepted")
}

// Pressing again re-offers the same place, and grants nothing extra.
func TestPressingAgainReOffersTheSamePlace(t *testing.T) {
	alice, bob := peerPair(t, 0)
	if err := bob.AnnounceOnRadio(); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "alice to hear bob", func() bool {
		_, ok := neighbourOf(alice, bob.Device.ID)
		return ok
	})
	first, err := alice.OfferLineOverRadio(bob.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := alice.OfferLineOverRadio(bob.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("two presses opened two places (%s, %s): the six-empty-rooms "+
			"bug", first.Hex()[:8], second.Hex()[:8])
	}
	n := 0
	for _, rec := range alice.Invitations() {
		if rec.Mode == InvitationTargeted {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("two presses recorded %d invitations, want 1", n)
	}
}

// A grant is honoured only from the device that made the offer. Otherwise
// anyone in range who overheard an invitation id could hand somebody a space.
func TestAGrantFromAStrangerIsIgnored(t *testing.T) {
	alice, bob := peerPair(t, 0)
	if err := bob.AnnounceOnRadio(); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "alice to hear bob", func() bool {
		_, ok := neighbourOf(alice, bob.Device.ID)
		return ok
	})
	if _, err := alice.OfferLineOverRadio(bob.Device.ID); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "the offer to arrive", func() bool {
		return len(bob.RadioOffers()) > 0
	})
	invID := bob.RadioOffers()[0].ID

	// A stranger mints a space of its own and grants it under alice's
	// invitation id, signed perfectly well — by the wrong device.
	stranger := openRuntime(t, t.TempDir(), "stranger")
	defer stranger.Close()
	other, err := stranger.CreateSpace("not yours")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := stranger.MintInvite(other, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := decodeB64(invite)
	if err != nil {
		t.Fatal(err)
	}
	body := encodeLineBody(lineBody{
		Invite: invID, From: stranger.Device.ID, To: bob.Device.ID,
		Space: other, Payload: raw,
	})
	bob.onRadioControl(nil,
		signedRadio(radioMsgGrant, domainGrant, body, stranger.Device.SignKey()))

	bob.mu.Lock()
	_, joined := bob.spaces[other]
	bob.mu.Unlock()
	if joined {
		t.Fatal("a grant from a device that made no offer put bob in a space")
	}
}

func assertNoPhantomMember(t *testing.T, rt *Runtime, tid id.TerminalID, when string) {
	t.Helper()
	rt.mu.Lock()
	st, ok := rt.spaces[tid]
	var members int
	if ok {
		members = len(st.space.Members())
	}
	rt.mu.Unlock()
	if !ok {
		t.Fatalf("%s: the space is gone", when)
	}
	if members != 1 {
		t.Fatalf("%s: the space holds %d members, want 1 (only the offerer) — "+
			"a person who has not answered is not in the room", when, members)
	}
}

func hasMember(rt *Runtime, tid id.TerminalID, who *Runtime) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	st, ok := rt.spaces[tid]
	if !ok {
		return false
	}
	return st.space.HasMember(who.Device.ID)
}

// The contrast, stated rather than assumed.
//
// The one-shot form is still here and still useful where the trade is
// acceptable, so the trade is written down as a test: it grants at OFFER time,
// which means a person who never answers is nonetheless in the room. This is
// the thing the four-message saga exists to avoid, and a test that only
// asserted the new path was clean would not show why it was worth four legs.
func TestTheOneShotFormDoesLeaveAPhantomMember(t *testing.T) {
	alice, bob := peerPair(t, 0)
	if err := bob.AnnounceOnRadio(); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "alice to hear bob", func() bool {
		_, ok := neighbourOf(alice, bob.Device.ID)
		return ok
	})
	tid, err := alice.StartLineOverRadio(bob.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	alice.mu.Lock()
	st := alice.spaces[tid]
	members := len(st.space.Members())
	alice.mu.Unlock()
	if members != 2 {
		t.Fatalf("the one-shot form holds %d members, expected 2 (a phantom) — "+
			"if this changed, the four-message saga's justification changed too",
			members)
	}
}

// The delivery signal is what lets a screen say the true thing.
//
// SendControl returns when a message is QUEUED, and on this carrier that is
// indistinguishable from "gone" for minutes. Without the outcome reported back,
// "sending…" is the only sentence available — which is the screen this whole
// wave exists because of.
func TestAnOfferThatNobodyHeardIsNotReportedAsSent(t *testing.T) {
	alice, bob := peerPair(t, 0)
	if err := bob.AnnounceOnRadio(); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "alice to hear bob", func() bool {
		_, ok := neighbourOf(alice, bob.Device.ID)
		return ok
	})
	if _, err := alice.OfferLineOverRadio(bob.Device.ID); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, "the offer to be recorded as heard", func() bool {
		for _, rec := range alice.Invitations() {
			if rec.Mode == InvitationTargeted && rec.Delivery == DeliveryHeard {
				return true
			}
		}
		return false
	})
}

// Host approval over a live-only rendezvous is refused at MINT.
//
// A link that asks somebody to decide needs somewhere to hold the question,
// and a radio segment holds nothing for a person who is not here. Refusing is
// better than a six-leg saga built around a human pause.
func TestHostApprovalIsRefusedOnALiveOnlyRendezvousWithTheReason(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	s := rt.GetSettings()
	s.Relay = radioRendezvousPrefix + "abc123"
	if err := rt.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	_, err := rt.CreateQuickLink(QuickLinkOptions{Approval: "host"})
	if err == nil {
		t.Fatal("host approval was accepted on a rendezvous that holds nothing")
	}
	for _, want := range []string{"decide", "holds nothing"} {
		if !contains(err.Error(), want) {
			t.Fatalf("the refusal should mention %q: %v", want, err)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
