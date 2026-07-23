package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/routing"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports"
	"github.com/drrainlab/quiet_places/transports/loopback"
)

type testLink struct{ transports.Endpoint }

func (testLink) Closed() (bool, error) { return false, nil }

// TN-1 seam: a filtered link syncs ONLY the allowed spaces; the peer never
// sees the filtered-out space over that link.
func TestAdoptLinkFilteredScopesSpaces(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	tidA, err := alice.CreateSpace("Allowed")
	if err != nil {
		t.Fatal(err)
	}
	tidB, err := alice.CreateSpace("Filtered")
	if err != nil {
		t.Fatal(err)
	}
	for _, tid := range []id.TerminalID{tidA, tidB} {
		if _, err := alice.Say(tid, "hello from "+tid.Hex()[:6]); err != nil {
			t.Fatal(err)
		}
		invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bob.JoinInvite(invite); err != nil {
			t.Fatal(err)
		}
	}

	pair := loopback.NewPair(loopback.Faults{Seed: 5})
	allowOnlyA := func(m routing.FrameMeta) bool { return m.Destination == tidA }
	alice.adoptLinkFiltered(testLink{pair.A}, 30*time.Millisecond, 200*time.Millisecond,
		"test", allowOnlyA)
	bob.adoptLink(testLink{pair.B}, 30*time.Millisecond, 200*time.Millisecond, "test")

	deadline := time.Now().Add(8 * time.Second)
	for {
		spA, _ := bob.Space(tidA)
		if len(spA.State.Messages()) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("allowed space did not sync")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Give the filtered space every chance to leak, then assert it didn't.
	time.Sleep(1 * time.Second)
	spB, _ := bob.Space(tidB)
	if n := len(spB.State.Messages()); n != 0 {
		t.Fatalf("filtered space leaked %d messages over the scoped link", n)
	}
}
