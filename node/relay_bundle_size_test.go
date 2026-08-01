package node

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// A space keeps syncing after its log outgrows one relay item.
//
// Reported from two live nodes: a person published a long post into a shared
// space and the other member never saw it — while ordinary chat in the same
// space had been arriving all along.
//
// pushToRelay puts the WHOLE log into ONE bundle and Puts it as a single relay
// item. The relay wire carries an item as one CBOR byte string bounded by
// codec.MaxItemLen (1 MiB), so a space whose accumulated frames pass that
// figure stops delivering — permanently, and to every member at once, because
// the bundle only ever grows. A publication document may be half a megabyte on
// its own, which is why a post is what finally tips a space over.
//
// The media path next door already knows this: answerWants batches blobs into
// items each under maxRelayItem, and its comment says a single oversized
// bundle "would fail to decode at the relay (and the failure would be
// silent)". Only the frame path was left whole.
func TestASpaceKeepsSyncingPastOneRelayItem(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	tid, err := alice.CreateSpace("Long Room")
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

	// Past the item cap, and not by a hair: a hundred and ten messages of 15 KiB is
	// about 1.6 MiB of frames, so the whole-log bundle cannot fit however
	// generously the wire is measured.
	const (
		msgs = 110
		size = 15 << 10
	)
	for i := 0; i < msgs; i++ {
		body := fmt.Sprintf("%03d ", i) + strings.Repeat("x", size)
		if _, err := alice.Say(tid, body, SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// And the last thing said is what the reader is looking for — a short
	// line after the wall, so "did the tail arrive" is unambiguous.
	if _, err := alice.Say(tid, "the post everybody is waiting for", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := alice.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := bob.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(45 * time.Second)
	var got int
	var sawTail bool
	for time.Now().Before(deadline) {
		_, _, _ = alice.PushToRelay(addr, tid)
		_, _ = bob.PullFromRelay(addr)
		got, sawTail = 0, false
		for _, e := range bobEntries(t, bob, tid) {
			got++
			if strings.Contains(e, "everybody is waiting") {
				sawTail = true
			}
		}
		if sawTail {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !sawTail {
		t.Fatalf("the tail of a %d-message space never reached the other member "+
			"(got %d entries) — a log past one relay item stops delivering, and "+
			"it never starts again because the bundle only grows", msgs+1, got)
	}
}

// Every text entry bob can project, as strings.
func bobEntries(t *testing.T, rt *Runtime, tid id.TerminalID) []string {
	t.Helper()
	var out []string
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, st := range rt.spaces {
		if st.space.ID != tid {
			continue
		}
		for _, e := range st.space.State.Entries() {
			if e.Content.Text != nil {
				out = append(out, e.Content.Text.Text)
			}
		}
	}
	return out
}
