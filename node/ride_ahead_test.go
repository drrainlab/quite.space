package node

// THE SLEEPING-SENDER INVARIANT. The beta's worst media case was a mac
// pulling 612 KB from a pocketed phone for two minutes: the want arrived
// instantly and then waited for an OS to unfreeze the holder's loop. The
// one moment a phone is certainly awake is the moment it SENDS — so the
// bytes of a reasonably-sized asset ride the same push as the frame, and
// the recipient's fetch finds them already in its mailbox.
//
// The test is the sentence itself: the sender posts, pushes once, and
// DIES. The recipient must still end up holding the file.

import (
	"bytes"
	"crypto/rand"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

func TestMediaRidesAheadOfTheRequest(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()

	dirA := t.TempDir()
	sender := openRuntime(t, dirA, "sender")
	setPersonalRelay(t, sender, addr)
	sender.applyRelaySync("", 0) // deterministic: every sync is by hand
	tid, err := sender.CreateSpace("room")
	if err != nil {
		t.Fatal(err)
	}

	guest := openRuntime(t, t.TempDir(), "guest")
	defer guest.Close()
	setPersonalRelay(t, guest, addr)
	guest.applyRelaySync("", 0)

	pass, err := sender.MintPass(tid, 1, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := guest.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, guest, req, JoinReady)

	// The photo: 1 MB, well under rideAheadMaxBytes.
	payload := make([]byte, 1<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	ref, err := sender.IngestAsset(bytes.NewReader(payload), int64(len(payload)),
		assets.Metadata{MediaType: "image/jpeg", Role: "original"})
	if err != nil {
		t.Fatal(err)
	}
	sender.RideAhead(tid, ref)
	body, err := (&schemas.FileBlock{Filename: "photo.jpg",
		MediaType: "image/jpeg", Size: uint64(len(payload)), Original: ref}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sender.EmitBlock(tid, schemas.BlockFile, body); err != nil {
		t.Fatal(err)
	}

	// One push — the sender's last act while awake.
	sender.relaySyncOnce(addr)
	// And the phone goes back into the pocket, terminally.
	sender.Close()

	// The recipient pulls its own mailbox. Nobody is left to answer a
	// want; whatever it holds now is all it will ever get.
	deadline := time.Now().Add(20 * time.Second)
	aid := ref.PublicIDHex()
	for {
		if _, err := guest.PullFromRelay(addr); err != nil {
			t.Fatal(err)
		}
		st, err := guest.AssetStatus(tid, aid)
		if err == nil && st.State == assets.StateComplete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the bytes did not ride ahead: state %+v, err %v", st, err)
		}
		time.Sleep(700 * time.Millisecond)
	}
	data, _, err := guest.RetrieveAsset(tid, aid)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatal("the recipient holds different bytes than were sent")
	}
}

// And the bound: a feature film must NOT flood every member's mailbox.
func TestOversizedMediaDoesNotRideAhead(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "sender")
	defer rt.Close()
	tid, err := rt.CreateSpace("room")
	if err != nil {
		t.Fatal(err)
	}
	ref := &schemas.AssetRef{Size: rideAheadMaxBytes + 1}
	rt.RideAhead(tid, ref)
	rt.mu.Lock()
	armed := len(rt.rideAhead[tid])
	rt.mu.Unlock()
	if armed != 0 {
		t.Fatalf("an oversized asset was armed to ride ahead (%d entries)", armed)
	}
}
