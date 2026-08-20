package node

// ROUTES GO STALE TWO HONEST WAYS: automatic selection moves a device to
// another relay and nobody tells its peers, and a fresh join knocks
// before its own ingress exists, so the owner records nothing. One beta
// evening produced both at once — a phone reachable only at the staging
// relay, a mac addressing it at the main one, and a want that never
// arrived anywhere. The cure is one rule: a device states its current
// ingress with every content push, and the latest self-statement wins.

import (
	"bytes"
	"crypto/rand"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

func TestAContentPushAnnouncesItsSendersIngress(t *testing.T) {
	srvA, addrA := startRelay(t)
	defer srvA.Close()
	srvB, addrB := startRelay(t)
	defer srvB.Close()

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	setPersonalRelay(t, owner, addrA)
	owner.applyRelaySync("", 0) // every sync by hand
	tid, err := owner.CreateSpace("room")
	if err != nil {
		t.Fatal(err)
	}

	joiner := openRuntime(t, t.TempDir(), "joiner")
	defer joiner.Close()
	setPersonalRelay(t, joiner, addrB)
	joiner.applyRelaySync("", 0)

	pass, err := owner.MintPass(tid, 1, 24, addrA)
	if err != nil {
		t.Fatal(err)
	}
	req, err := joiner.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, joiner, req, JoinReady)
	// The joiner must absorb the room before it can address anyone in it:
	// members are knowledge from the log, and a fresh joiner that has
	// pulled nothing delivers to nobody (the "solo space" no-op). One
	// owner push + one joiner pull is the tick the background loop would
	// have supplied.
	owner.relaySyncOnce(addrA)
	if _, err := joiner.PullFromRelay(addrB); err != nil {
		t.Fatal(err)
	}

	// The joiner holds a file the owner will later want. NOT armed to
	// ride ahead — this test is about the want path.
	payload := make([]byte, 200<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	ref, err := joiner.IngestAsset(bytes.NewReader(payload), int64(len(payload)),
		assets.Metadata{MediaType: "application/octet-stream", Role: "original"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := (&schemas.FileBlock{Filename: "f.bin",
		MediaType: "application/octet-stream", Size: uint64(len(payload)),
		Original: ref}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := joiner.EmitBlock(tid, schemas.BlockFile, body); err != nil {
		t.Fatal(err)
	}
	joiner.relaySyncOnce(addrB)
	if _, err := owner.PullFromRelay(addrA); err != nil {
		t.Fatal(err)
	}

	// THE KIT SHAPE: the owner's book forgets the joiner entirely — the
	// state a knock-without-ingress or a lost keystore leaves behind.
	owner.mu.Lock()
	delete(owner.ks.PeerRoutes, joiner.Device.ID)
	owner.mu.Unlock()
	if eps := owner.PeerRoutesFor(joiner.Device.ID); len(eps) != 0 {
		t.Fatalf("the wipe did not take: %v", eps)
	}

	// The joiner merely SPEAKS — no want, no fetch, just a message — and
	// the push carries its ingress.
	if _, err := joiner.Say(tid, "ping", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	joiner.relaySyncOnce(addrB)
	if _, err := owner.PullFromRelay(addrA); err != nil {
		t.Fatal(err)
	}
	eps := owner.PeerRoutesFor(joiner.Device.ID)
	found := false
	for _, ep := range eps {
		if ep == addrB {
			found = true
		}
	}
	if !found {
		t.Fatalf("the owner did not learn the speaker's ingress from its push: %v", eps)
	}

	// And the proof that the knowledge works: the owner's fetch crosses
	// two relays and completes.
	aid := ref.PublicIDHex()
	if err := owner.RequestAsset(tid, aid); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(25 * time.Second)
	for {
		owner.relaySyncOnce(addrA)
		time.Sleep(700 * time.Millisecond)
		joiner.relaySyncOnce(addrB)
		if _, err := owner.PullFromRelay(addrA); err != nil {
			t.Fatal(err)
		}
		st, err := owner.AssetStatus(tid, aid)
		if err == nil && st.State == assets.StateComplete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fetch never completed after the route healed: %+v", st)
		}
	}
	data, _, err := owner.RetrieveAsset(tid, aid)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatal("the owner holds different bytes than were sent")
	}
}
