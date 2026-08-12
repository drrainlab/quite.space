package node

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/relay"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// A1 — the privacy invariant of the whole wave: what caught your attention is
// yours alone. Nothing QuietRank produces may enter the event log, ride a
// bundle, or reach a relay. Enforced by a test rather than promised in a doc.
func TestAttentionNeverReachesTheRelay(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	tid, err := alice.CreateSpace("Workshop")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "посмотришь конфиг?", SayOptions{
		Mentions: []id.PrincipalID{bob.Principal.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := alice.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}

	// Bob's attention layer now holds distinctive private state: what he
	// watches, what he is called, and a learned verdict.
	const (
		secretWatch = "СЕКРЕТНЫЙ-ИНТЕРЕС-БОБА"
		secretAlias = "СЕКРЕТНЫЙ-АЛИАС-БОБА"
	)
	pol := bob.AttentionPolicy()
	pol.Watched = []string{secretWatch}
	pol.Aliases = []string{secretAlias}
	if err := bob.SetAttentionPolicy(pol); err != nil {
		t.Fatal(err)
	}
	sigs := bob.Signals()
	if len(sigs) == 0 {
		t.Fatal("no signal to test with")
	}
	if err := bob.SignalFeedback(sigs[0].ID, true); err != nil {
		t.Fatal(err)
	}
	bob.MarkSignalsSeen("")

	// Bob now pushes his copy of the space — the exact bytes a relay, a
	// courier, or a radio carrier would see.
	if _, _, err := bob.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}

	// Read back everything the relay is holding for this space and scan it.
	client, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	now := uint64(time.Now().Unix())
	b := relay.Bucket(now)
	hints := [][]byte{relay.Hint(tid, b), relay.Hint(tid, b-1)}
	for _, dev := range []id.DeviceID{alice.Device.ID, bob.Device.ID} {
		hints = append(hints, relay.HintFor(tid, dev, b), relay.HintFor(tid, dev, b-1))
	}
	items, err := client.Fetch(hints)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("relay held nothing — the test would be vacuous")
	}
	needles := [][]byte{
		[]byte(secretWatch), []byte(secretAlias),
		[]byte("attention"), []byte("quietrank"), []byte("signal"),
	}
	for i, item := range items {
		for _, n := range needles {
			if bytes.Contains(item, n) {
				t.Fatalf("relay item %d leaked attention data: %q", i, n)
			}
		}
	}

	// The log itself never grew an attention event.
	sp, _ := bob.spaceForTest(tid)
	for schema := range sp.State.Unsupported {
		if strings.Contains(schema, "attention") {
			t.Fatalf("an attention schema reached the reducer: %s", schema)
		}
	}
	for _, e := range sp.State.Entries() {
		if e.Content.Text != nil && strings.Contains(e.Content.Text.Text, secretWatch) {
			t.Fatal("a watched phrase was written into the space")
		}
	}
}

// The attention state IS persisted — sealed, on Bob's own disk. Without this
// the leak test above would pass vacuously.
func TestAttentionIsPersistedSealedLocally(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "bob")
	tid, err := rt.CreateSpace("Workshop")
	if err != nil {
		t.Fatal(err)
	}
	_ = tid
	pol := rt.AttentionPolicy()
	pol.Watched = []string{"ЛОКАЛЬНЫЙ-ИНТЕРЕС"}
	if err := rt.SetAttentionPolicy(pol); err != nil {
		t.Fatal(err)
	}
	rt.MarkSignalsSeen("")
	rt.Close()

	var sealedFiles int
	var plaintextLeak string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if strings.Contains(filepath.Base(path), "attention") {
			sealedFiles++
		}
		data, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Contains(data, []byte("ЛОКАЛЬНЫЙ-ИНТЕРЕС")) {
			plaintextLeak = path
		}
		return nil
	})
	if plaintextLeak != "" {
		t.Fatalf("attention policy stored in the clear at %s", plaintextLeak)
	}
	// Reopening restores it, which proves it really was written.
	rt2 := openRuntime(t, dir, "bob")
	defer rt2.Close()
	if got := rt2.AttentionPolicy().Watched; len(got) != 1 || got[0] != "ЛОКАЛЬНЫЙ-ИНТЕРЕС" {
		t.Fatalf("attention policy did not survive a restart: %+v", got)
	}
}

// Attention is viewer-relative, so it stays out of the convergence digest by
// construction — the same precedent as resonance, keep, and publications.
func TestAttentionDoesNotAffectTheSpaceDigest(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Workshop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Say(tid, "первое сообщение", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)
	before := sp.State.Digest()

	pol := rt.AttentionPolicy()
	pol.Watched = []string{"НЕ-ДОЛЖНО-МЕНЯТЬ-DIGEST"}
	pol.Aliases = []string{"алиса"}
	if err := rt.SetAttentionPolicy(pol); err != nil {
		t.Fatal(err)
	}
	rt.Signals() // force a scan, feedback, and a sealed write
	if after := sp.State.Digest(); after != before {
		t.Fatal("attention changed the space convergence digest")
	}
}
