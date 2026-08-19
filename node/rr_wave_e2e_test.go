// The RR wave's closing end-to-end test: TWO relays, real runtimes, and
// the property the whole wave turns on — a public space's traffic
// follows its SIGNED relay set, and moving that set moves everybody
// without anyone reconfiguring anything.
//
// Nothing here is mocked: two relay servers, a publisher, a reader, and
// the ordinary sync loops. What is asserted is what a person would see.
package node

import (
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/publication"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// postDoc is one minimal article.
func postDoc(t *testing.T, title string) *publication.Document {
	t.Helper()
	var docID [16]byte
	if _, err := rand.Read(docID[:]); err != nil {
		t.Fatal(err)
	}
	return &publication.Document{
		DocumentID: docID, Kind: "article", Title: title,
		Visibility: "space",
		Blocks: []publication.Block{{
			ID: "b1", Type: "text",
			RawProps: publication.EncodeTextProps(publication.TextProps{Text: title}),
		}},
	}
}

func startRelay(t *testing.T) (*relayserver.Server, string) {
	t.Helper()
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return srv, fmt.Sprintf("127.0.0.1:%d", port)
}

func setPersonalRelay(t *testing.T, rt *Runtime, addr string) {
	t.Helper()
	s := rt.GetSettings()
	s.Relay = addr
	if err := rt.SetSettings(s); err != nil {
		t.Fatal(err)
	}
}

// waitForPost polls until the reader's replica holds a publication whose
// title contains want.
// hasPost is waitForPost's question without its verdict, for a loop that
// wants to stop the moment the answer is yes.
func hasPost(rt *Runtime, tid id.TerminalID, want string) bool {
	found := false
	_ = rt.withSpace(tid, func(st *spaceState) error {
		for _, p := range st.space.State.Publications() {
			if p.Title == want {
				found = true
			}
		}
		return nil
	})
	return found
}

func waitForPost(t *testing.T, rt *Runtime, tid id.TerminalID, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var seen []string
	for time.Now().Before(deadline) {
		seen = seen[:0]
		_ = rt.withSpace(tid, func(st *spaceState) error {
			for _, p := range st.space.State.Publications() {
				seen = append(seen, p.Title)
			}
			return nil
		})
		for _, s := range seen {
			if s == want {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%q never reached the reader; it holds %v", want, seen)
}

// TestFollowingAPublisherWhoIsNotUpYetStillConverges pins the bug the
// wave closer found: a reader who opens a link BEFORE the publisher has
// put anything on the relay has no signed policy and no observed source
// yet — so without recording the link's own address his sync loop falls
// back to his PERSONAL relay, where the space was never published, and he
// never converges. The link is an observation; it must be kept as one.
func TestFollowingAPublisherWhoIsNotUpYetStillConverges(t *testing.T) {
	r1, addr1 := startRelay(t)
	defer r1.Close()
	r2, addr2 := startRelay(t)
	defer r2.Close()

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	setPersonalRelay(t, alice, addr1)
	setPersonalRelay(t, bob, addr2)

	tid, err := alice.CreateSpaceWithOptions("Тихий сад", CreateOptions{
		Character: terminals.DefaultCharacter("campfire"),
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Join:       terminals.JoinOpen, Publish: terminals.PublishAll,
			Relays: []string{RelayRef{Endpoint: addr1}.String()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	link, err := alice.ComposePublicLink(tid, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Bob opens the link while NOTHING has been published yet: his first
	// fetch finds an empty outbox, which is routine, not a failure.
	if _, err := bob.OpenPublicLink(link); err != nil {
		t.Fatal(err)
	}
	if got := bob.ResolvePublicReadRelay(tid); got != addr1 {
		t.Fatalf("a reader with no projection yet reads from %q — "+
			"the link's address was forgotten", got)
	}

	// NOW alice publishes. Bob's ordinary loop must find it.
	if _, err := alice.PublishDocument(tid, postDoc(t, "Появился позже"), nil); err != nil {
		t.Fatal(err)
	}
	waitForPost(t, bob, tid, "Появился позже", 25*time.Second)
}

// TestSpaceRelaySetMovesEveryoneAcrossRelays is the wave closer.
//
//	R1, R2          two independent blind relays
//	alice           owns a public space whose SIGNED relay set is [R1]
//	bob             opens it from a link and follows the SIGNED set —
//	                not his own relay, which is R2 the whole time
//	the revision     alice signs [R2] into the policy; the manifest rides
//	                the projection alice is still publishing to R1, bob
//	                installs it, and BOTH sides move — with no settings
//	                change on either device
func TestSpaceRelaySetMovesEveryoneAcrossRelays(t *testing.T) {
	r1, addr1 := startRelay(t)
	defer r1.Close()
	r2, addr2 := startRelay(t)
	defer r2.Close()

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	// Alice publishes through R1; BOB'S OWN relay is R2 from the start,
	// so anything he reads about this space he reads because the SPACE
	// said so, never because his personal relay happened to match.
	setPersonalRelay(t, alice, addr1)
	setPersonalRelay(t, bob, addr2)

	tid, err := alice.CreateSpaceWithOptions("Сад", CreateOptions{
		Character: terminals.DefaultCharacter("campfire"),
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Join:       terminals.JoinOpen, Publish: terminals.PublishAll,
			Relays: []string{RelayRef{Endpoint: addr1}.String()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := alice.ResolvePublicWriteRelay(tid); got != addr1 {
		t.Fatalf("the owner writes to %q, not the signed set", got)
	}
	if _, err := alice.PublishDocument(tid, postDoc(t, "Первый пост"), nil); err != nil {
		t.Fatal(err)
	}

	// Bob opens the space from its link. The link's relay is R1 (that is
	// where the projection lives); he must NOT adopt it as his own.
	link, err := alice.ComposePublicLink(tid, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.OpenPublicLink(link); err != nil {
		t.Fatal(err)
	}
	if s := bob.GetSettings(); s.Relay != addr2 {
		t.Fatalf("opening a link changed bob's personal relay to %q", s.Relay)
	}
	waitForPost(t, bob, tid, "Первый пост", 25*time.Second)
	if got := bob.ResolvePublicReadRelay(tid); got != addr1 {
		t.Fatalf("bob reads the space from %q, expected the signed %q", got, addr1)
	}

	// THE MIGRATION. Alice signs [R2] into the policy. The revised
	// manifest rides the projection she is still publishing to R1 — the
	// one place bob is still looking — so the move is self-carrying.
	next := []string{RelayRef{Endpoint: addr2}.String()}
	if err := alice.RevisePolicy(tid, PolicyDelta{Relays: &next}); err != nil {
		t.Fatal(err)
	}
	// Alice's own resolution moves immediately: her next publish goes to R2.
	if got := alice.ResolvePublicWriteRelay(tid); got != addr2 {
		t.Fatalf("after the revision the owner still writes to %q", got)
	}
	// She must publish ONCE more to R1 so the revised manifest reaches
	// bob — this is the ordinary loop's job; do it explicitly so the test
	// does not race the tick.
	if err := alice.publishPublicProjection(addr1, tid); err != nil {
		t.Fatal(err)
	}

	// Bob installs the revision from R1 and re-resolves to R2 by himself.
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		_ = bob.fetchPublicProjection(addr1, tid)
		if bob.ResolvePublicReadRelay(tid) == addr2 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got := bob.ResolvePublicReadRelay(tid); got != addr2 {
		t.Fatalf("bob never migrated: still reading %q", got)
	}

	// And the migration is real: a post published ONLY to R2 arrives.
	if _, err := alice.PublishDocument(tid, postDoc(t, "После переезда"), nil); err != nil {
		t.Fatal(err)
	}
	if err := alice.publishPublicProjection(addr2, tid); err != nil {
		t.Fatal(err)
	}
	// Leave as soon as it lands: this loop once had no exit and sat out
	// its whole 25 seconds on every run, then asked waitForPost with a
	// one-second budget for what had arrived twenty seconds earlier.
	deadline = time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		_ = bob.fetchPublicProjection(bob.ResolvePublicReadRelay(tid), tid)
		if hasPost(bob, tid, "После переезда") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	waitForPost(t, bob, tid, "После переезда", time.Second)

	// Neither side ever reconfigured: bob's personal relay is untouched,
	// and alice's is still R1 — the SPACE moved, not the people.
	if s := bob.GetSettings(); s.Relay != addr2 {
		t.Fatalf("bob's personal relay drifted to %q", s.Relay)
	}
	if s := alice.GetSettings(); s.Relay != addr1 {
		t.Fatalf("alice's personal relay drifted to %q", s.Relay)
	}
}
