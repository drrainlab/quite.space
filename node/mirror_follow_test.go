// A mirrored DIRECTORY brings what it lists with it.
//
// Mirroring a catalogue used to keep only the LIST reachable: the cards
// appeared and every place they named stayed on its owner's machine. Measured
// on the demo catalog, where the mirror's whole data directory was 44 KiB and
// held no media at all, while the screen it fed showed posts full of pictures
// that never arrived.
package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/publication"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// publishSpaceCard puts a card into `dir` pointing at `link` — the shape a
// catalogue is made of.
func publishSpaceCard(t *testing.T, owner *Runtime, dir id.TerminalID, title, link string) {
	t.Helper()
	var docID [16]byte
	copy(docID[:], title)
	doc := &publication.Document{
		DocumentID: docID,
		Kind:       "space",
		Title:      title,
		Visibility: "space",
		Blocks: []publication.Block{
			{ID: "l1", Type: "link", RawProps: publication.EncodeTextProps(
				publication.TextProps{Text: "qs:" + link})},
		},
	}
	if _, err := owner.PublishDocument(dir, doc, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAMirroredDirectoryFollowsWhatItLists(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	if err := owner.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}

	// A place with something in it, and a catalogue that points at it.
	listed := openPublicSpaceForMirror(t, owner, "somewhere, slowly")
	if _, err := owner.Say(listed, "a post with pictures", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	dir, err := owner.CreateSpaceWithOptions("Demo spaces", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Join:       terminals.JoinOpen,
			Kind:       "directory",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	link, err := owner.ComposePublicLink(listed, nil)
	if err != nil {
		t.Fatal(err)
	}
	publishSpaceCard(t, owner, dir, "somewhere, slowly", link)
	for _, tid := range []id.TerminalID{dir, listed} {
		if err := owner.publishPublicProjection(addr, tid); err != nil {
			t.Fatal(err)
		}
	}

	// A volunteer mirrors THE CATALOGUE ONLY, which is all an operator does.
	mirror := openRuntime(t, t.TempDir(), "mirror")
	defer mirror.Close()
	if err := mirror.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := mirror.OpenPublicSpace(dir, addr); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "the mirror never read the catalogue", func() bool {
		_ = mirror.fetchPublicProjection(addr, dir)
		return mirrorSeesCards(mirror, dir) > 0
	})
	if err := mirror.SetMirror(dir, true); err != nil {
		t.Fatal(err)
	}
	if err := mirror.SetSeed(dir, true); err != nil {
		t.Fatal(err)
	}

	mirror.mirrorFollowCards(dir)

	mirror.mu.Lock()
	meta, known := mirror.ks.Spaces[listed]
	mirror.mu.Unlock()
	if !known {
		t.Fatal("the mirror kept the list and not the place it names — the catalogue " +
			"is reachable and everything in it is not")
	}
	if !meta.Mirror {
		t.Error("the listed space was opened but not mirrored")
	}
	// Seeding is inherited: a mirror answering for the catalogue answers for
	// what the catalogue lists.
	if !meta.Seed {
		t.Error("the catalogue seeds and the space it brought in does not")
	}
}

// THE OPERATOR STAYS IN CHARGE. `mirror remove` clears the flags and leaves
// the space, so a space this node already knows is never re-decided here —
// which is the whole mechanism by which a deliberate removal stays removed.
func TestFollowingNeverOverridesAnOperatorsRemoval(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	if err := owner.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	listed := openPublicSpaceForMirror(t, owner, "listed")
	dir, err := owner.CreateSpaceWithOptions("catalogue", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Join:       terminals.JoinOpen,
			Kind:       "directory",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	link, err := owner.ComposePublicLink(listed, nil)
	if err != nil {
		t.Fatal(err)
	}
	publishSpaceCard(t, owner, dir, "listed", link)
	for _, tid := range []id.TerminalID{dir, listed} {
		if err := owner.publishPublicProjection(addr, tid); err != nil {
			t.Fatal(err)
		}
	}

	mirror := openRuntime(t, t.TempDir(), "mirror")
	defer mirror.Close()
	if err := mirror.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := mirror.OpenPublicSpace(dir, addr); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "the mirror never read the catalogue", func() bool {
		_ = mirror.fetchPublicProjection(addr, dir)
		return mirrorSeesCards(mirror, dir) > 0
	})
	if err := mirror.SetMirror(dir, true); err != nil {
		t.Fatal(err)
	}
	mirror.mirrorFollowCards(dir)

	// The operator says no to this one.
	if err := mirror.SetMirror(listed, false); err != nil {
		t.Fatal(err)
	}

	// Every later pass must leave that alone.
	for i := 0; i < 3; i++ {
		mirror.mirrorFollowCards(dir)
	}
	mirror.mu.Lock()
	meta := mirror.ks.Spaces[listed]
	mirror.mu.Unlock()
	if meta.Mirror {
		t.Error("the follow pass undid a removal the operator made by hand")
	}
}

// mirrorSeesCards counts the space cards a node can read in a directory.
func mirrorSeesCards(r *Runtime, dir id.TerminalID) int {
	n := 0
	_ = r.withSpace(dir, func(st *spaceState) error {
		for _, p := range st.space.State.Publications() {
			if p.Document != nil && p.Document.Kind == "space" {
				n++
			}
		}
		return nil
	})
	return n
}
