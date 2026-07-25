package node

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// Mirrors the real listening-room path: an audio asset uploaded as
// block.attached.v1, referenced by a listening-room app instance, then a
// relay-only joiner fetches the track's bytes. Proves the track ref indexes on
// the joiner (same as a cover image) and the fetch completes over the relay.
func TestRelayListeningTrackFetch(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()

	tid, err := bob.CreateSpace("pine vibes")
	if err != nil {
		t.Fatal(err)
	}
	audio := randBytes(t, 300_000) // external-manifest asset, like a real track
	ref, err := bob.IngestAsset(bytes.NewReader(audio), int64(len(audio)),
		assets.Metadata{MediaType: "audio/mpeg", Role: "original", ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	blk := &schemas.AttachedBlock{Filename: "track.mp3", MediaType: "audio/mpeg", Original: ref}
	pl, err := blk.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.EmitBlock(tid, schemas.BlockAttached, pl); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.CreateAppInstance(tid, "listening-room", "", "",
		map[string]string{"title": "demo", "asset": ref.PublicIDHex()}); err != nil {
		t.Fatal(err)
	}

	invite, err := bob.MintInvite(tid, alice.Device.ID, alice.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}

	// Relay-only, no LAN.
	if err := bob.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := alice.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for { // the joiner indexes the track ref from the synced block.attached
		if _, err := alice.AssetStatus(tid, ref.PublicIDHex()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("track ref never indexed on the joiner")
		}
		time.Sleep(150 * time.Millisecond)
	}

	if err := alice.RequestAsset(tid, ref.PublicIDHex()); err != nil {
		t.Fatal(err)
	}
	for {
		st, err := alice.AssetStatus(tid, ref.PublicIDHex())
		if err != nil {
			t.Fatal(err)
		}
		if st.State == assets.StateComplete {
			break
		}
		if st.State == assets.StateFailed {
			t.Fatalf("track fetch failed: %s", st.Reason)
		}
		if time.Now().After(deadline) {
			t.Fatalf("track fetch never completed: %+v", st)
		}
		time.Sleep(200 * time.Millisecond)
	}
	got, _, err := alice.RetrieveAsset(tid, ref.PublicIDHex())
	if err != nil || !bytes.Equal(got, audio) {
		t.Fatalf("track bytes wrong over relay: %v", err)
	}
}
