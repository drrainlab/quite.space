package node

// The delivery delta (node/offers.go): a mailbox that already holds the
// history is not sent the history again — measured at the relay, which now
// counts its puts (transports/relayserver/status.go). And the two
// neighbours that shipped with it: a legacy route has a shelf life, and a
// shell with no window infers attention from its API being used.

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func TestAConvergedSpaceStopsRemailingItsHistory(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	setPersonalRelay(t, alice, addr)
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	setPersonalRelay(t, bob, addr)

	tid, err := alice.CreateSpace("тихая комната")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(tid, 2, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)
	for i := 0; i < 5; i++ {
		if _, err := alice.Say(tid, "история", SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	nodes := map[string]*Runtime{"alice": alice, "bob": bob}
	addrs := map[string]string{"alice": addr, "bob": addr}
	deadline := time.Now().Add(30 * time.Second)
	for countMsg(t, bob, tid, "история") < 5 {
		convergeTick(nodes, addrs)
		if time.Now().After(deadline) {
			t.Fatal("bob never converged")
		}
	}
	// Steady state: nothing new to say. Before the delta every one of
	// these cycles re-put the whole log into bob's mailbox.
	before := srv.StatusSnapshot("", "").Traffic.PutsTotal
	for range 6 {
		convergeTick(nodes, addrs)
	}
	after := srv.StatusSnapshot("", "").Traffic.PutsTotal
	// Presence rides on its own short clock and receipts have their own
	// mailbox; what must NOT be here is six copies of the history.
	if after-before > 8 {
		t.Fatalf("idle cycles put %d items into the relay — the history is being re-mailed", after-before)
	}
	// And a new message still crosses, as a delta.
	if _, err := alice.Say(tid, "новое", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(30 * time.Second)
	for countMsg(t, bob, tid, "новое") < 1 {
		convergeTick(nodes, addrs)
		if time.Now().After(deadline) {
			t.Fatal("the delta never reached bob")
		}
	}
}

func TestALegacyRouteExpiresIntoAGuess(t *testing.T) {
	now := time.Now().Unix()
	fresh := storage.Route{Endpoint: "1.2.3.4:7411", Provenance: storage.RouteLegacy,
		LearnedAt: now - 3600, LastSeen: now - 3600}
	old := storage.Route{Endpoint: "1.2.3.4:7411", Provenance: storage.RouteLegacy,
		LearnedAt: now - legacyRouteMaxAge - 1, LastSeen: now - legacyRouteMaxAge - 1}
	if legacyRouteExpired(fresh, now) {
		t.Fatal("an hour-old legacy route is not expired")
	}
	if !legacyRouteExpired(old, now) {
		t.Fatal("a legacy route past its shelf life must expire")
	}
	// The delivery resolver: an expired legacy entry is not a route AND
	// not a "stated route that is down" — the device falls to the
	// bootstrap guess, tentative, exactly like a device nothing is known
	// about.
	rt := openRuntime(t, t.TempDir(), "carol")
	defer rt.Close()
	srv, addr := startRelay(t)
	defer srv.Close()
	setPersonalRelay(t, rt, addr)
	var ghost id.DeviceID
	ghost[0] = 0x42
	rt.mu.Lock()
	rt.ks.PeerRoutes = map[id.DeviceID][]storage.Route{ghost: {old}}
	rt.mu.Unlock()
	tid, err := rt.CreateSpace("призрак")
	if err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	st := rt.spaces[tid]
	rt.mu.Unlock()
	st.space.AddMember(ghost, [32]byte{})
	if _, err := rt.Say(tid, "кто-нибудь", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	_, _, noRoute, tentative, legacyBasis, err := rt.deliverSpace(tid, AssetsManifests, addr)
	if err != nil {
		t.Fatal(err)
	}
	if legacyBasis || tentative != 1 || noRoute != 0 {
		t.Fatalf("expired legacy route: tentative=%d noRoute=%d legacyBasis=%v — want a plain guess",
			tentative, noRoute, legacyBasis)
	}
}

func TestAttentionFromTheAPIIsAFactWithAShelfLife(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "dana")
	defer rt.Close()
	if !rt.foregrounded() {
		t.Fatal("a shell that never spoke keeps the foreground default")
	}
	rt.EnableAttentionFromAPI()
	if rt.foregrounded() {
		t.Fatal("enabling attention-from-API starts in the background")
	}
	rt.NoteAttention()
	if !rt.foregrounded() {
		t.Fatal("an API request is attention")
	}
	// The window is measured in tens of seconds; fire the timer by hand
	// rather than wait — the contract under test is the transition.
	rt.attMu.Lock()
	rt.attTimer.Reset(time.Millisecond)
	rt.attMu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for rt.foregrounded() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if rt.foregrounded() {
		t.Fatal("attention did not lapse after the window")
	}
}

func TestAPublicPublisherKeepsTheBackgroundMinute(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "erin")
	defer rt.Close()
	rt.SetForeground(false)
	base := 2 * time.Second
	rt.listenParked.Add(1) // a healthy parked listener
	defer rt.listenParked.Add(-1)
	if got := rt.syncInterval(base); got != base*listenedMultiplier {
		t.Fatalf("a listening node with no public spaces stretches to the doorbell cadence, got %s", got)
	}
	openPublicSpaceForMirror(t, rt, "площадь")
	if got := rt.syncInterval(base); got != base*backgroundMultiplier {
		t.Fatalf("a public publisher must keep the background minute for its ingress, got %s", got)
	}
}
