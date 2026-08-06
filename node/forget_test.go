package node

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// SD-0 — what "delete this space" has to mean before it may be offered.
//
// The promise is small and it must be kept exactly: THIS DEVICE forgets. Not
// "everyone forgets", which no local-first system can honour, and not "it
// disappears from the list while the messages stay on disk", which is the
// version that gets shipped by accident and is worse than not offering it.

func TestADeletedSpaceIsGoneFromTheListAndFromTheDisk(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpace("a room")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Say(tid, "something said once", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	events := rt.root.EventsDir(tid)
	if _, err := os.Stat(events); err != nil {
		t.Fatalf("the fixture never wrote anything: %v", err)
	}

	if err := rt.DeleteSpace(tid); err != nil {
		t.Fatal(err)
	}

	for _, s := range rt.Spaces() {
		if s.ID == tid {
			t.Fatal("the space is still listed")
		}
	}
	if _, err := os.Stat(events); !os.IsNotExist(err) {
		t.Fatal("THE MESSAGES ARE STILL ON DISK. A space that vanishes from " +
			"the list while its log survives is the deletion people think " +
			"they got and did not.")
	}
	rt.Close()

	// And it stays gone across a restart — a deletion that a reopen undoes
	// is a filtered view, not a deletion.
	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	for _, s := range rt2.Spaces() {
		if s.ID == tid {
			t.Fatal("the space came back after a restart")
		}
	}
	if _, ok := rt2.ks.TerminalSeeds[tid]; ok {
		t.Fatal("the terminal seed survived: the space could be re-created")
	}
	if _, ok := rt2.ks.Epochs[tid]; ok {
		t.Fatal("the epoch keys survived: the content could still be read")
	}
	if n, _ := rt2.ForgottenSpaces(); n != 1 {
		t.Fatalf("the tombstone did not survive the restart: %d", n)
	}
}

// A deletion is several file operations after one keystore write. The write is
// the commit: if the process dies immediately after it, the space is gone and
// its events are not, and the next open must finish the job rather than leave
// somebody's conversation lying about.
func TestAnInterruptedDeletionIsFinishedAtTheNextOpen(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpace("a room")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Say(tid, "something said once", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	events := rt.root.EventsDir(tid)

	// Exactly the crash window: the commit happened, the cleanup did not.
	rt.mu.Lock()
	delete(rt.ks.Spaces, tid)
	delete(rt.ks.TerminalSeeds, tid)
	delete(rt.ks.Epochs, tid)
	rt.ks.Forgotten[tid] = 1
	if err := rt.root.SaveKeystore(rt.ks); err != nil {
		rt.mu.Unlock()
		t.Fatal(err)
	}
	rt.mu.Unlock()
	rt.Close()

	if _, err := os.Stat(events); err != nil {
		t.Fatal("the fixture did not leave the events behind; nothing to sweep")
	}

	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	if _, err := os.Stat(events); !os.IsNotExist(err) {
		t.Fatal("a deletion interrupted by a crash left the log on disk, and " +
			"nothing ever came back for it")
	}
}

// MEDIA IS SHARED ON PURPOSE — the same photo in two rooms is stored once —
// so deleting a room must take the media only that room referred to. Taking
// the shared bytes would empty the other conversation of its pictures; taking
// nothing would leave the deleted conversation's pictures on the disk.
func TestDeletingASpaceTakesItsOwnMediaAndLeavesWhatIsShared(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	gone, err := rt.CreateSpace("the one being deleted")
	if err != nil {
		t.Fatal(err)
	}
	kept, err := rt.CreateSpace("the one staying")
	if err != nil {
		t.Fatal(err)
	}

	only, err := rt.root.PutBlob([]byte("bytes only the deleted space knows"))
	if err != nil {
		t.Fatal(err)
	}
	shared, err := rt.root.PutBlob([]byte("the same photo, sent in both rooms"))
	if err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.assetIdx.allow(only, gone)
	rt.assetIdx.allow(shared, gone)
	rt.assetIdx.allow(shared, kept)
	rt.mu.Unlock()

	if err := rt.DeleteSpace(gone); err != nil {
		t.Fatal(err)
	}

	if rt.root.HasBlob(only) {
		t.Fatal("media nothing else refers to survived the deletion")
	}
	if !rt.root.HasBlob(shared) {
		t.Fatal("MEDIA ANOTHER SPACE STILL USES WAS DELETED. The photo would " +
			"vanish from a conversation nobody touched.")
	}
}

// Deleting is not leaving: the protocol has no departure, so nothing is sent
// and nothing is claimed. What must be true is that the space stops being
// polled — otherwise the device keeps asking a relay about a conversation the
// person deleted, which is the opposite of what they asked for.
func TestADeletedSpaceIsNoLongerPolledOnTheRelay(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	keep, err := rt.CreateSpace("kept")
	if err != nil {
		t.Fatal(err)
	}
	drop, err := rt.CreateSpace("deleted")
	if err != nil {
		t.Fatal(err)
	}

	before := rt.relayMailboxSpaces()
	if len(before) != 2 {
		t.Fatalf("expected both spaces in the mailbox list, got %d", len(before))
	}
	if err := rt.DeleteSpace(drop); err != nil {
		t.Fatal(err)
	}
	after := rt.relayMailboxSpaces()
	if len(after) != 1 || after[0] != keep {
		t.Fatalf("the deleted space is still polled: %v", after)
	}
}

// Deleting a space nobody has is not an error worth inventing a state for,
// but it must not silently succeed either: a UI that reports "deleted" for a
// space it failed to find is a UI that will one day report it for the wrong
// space.
func TestDeletingASpaceThatIsNotHereSaysSo(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	var nobody id.TerminalID
	nobody[0] = 9
	if err := rt.DeleteSpace(nobody); err == nil {
		t.Fatal("deleting a space that is not here reported success")
	} else if !strings.Contains(err.Error(), "no such space") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

// The tombstone stops a join saga that was ALREADY IN FLIGHT from bringing
// the space back — that is a leftover, not a decision. It must not stop the
// person from deliberately joining again later, which is why the sweep drops
// the tombstone the moment the space is listed again.
func TestJoiningAgainAfterADeletionIsAllowed(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpace("a room")
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.DeleteSpace(tid); err != nil {
		t.Fatal(err)
	}
	// Deliberately here again, the way a re-join leaves it.
	if _, err := rt.CreateSpace("a room, again"); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	if len(rt2.Spaces()) != 1 {
		t.Fatalf("expected the new space to survive the sweep, got %d", len(rt2.Spaces()))
	}
}

// The HTTP surface, because that is what a person actually presses.
//
// TWO THINGS ARE ASSERTED AND BOTH MATTER. That DELETE removes it — and that
// the answer says WHERE it was removed from. A response of {"ok":true} would
// let every client word this operation however it liked, and the one wording
// that must never appear is the one that implies everyone else's copy went
// too.
func TestTheDeleteRouteRemovesTheSpaceAndSaysWhatItDid(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("a room")
	if err != nil {
		t.Fatal(err)
	}

	api, err := NewAPIServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()

	do := func(method, path string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.Header.Set("X-QP-Token", api.Token())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	code, body := do("DELETE", "/api/spaces/"+tid.Hex())
	if code != http.StatusOK {
		t.Fatalf("DELETE answered %d: %s", code, body)
	}
	if !strings.Contains(body, "this_device") {
		t.Fatalf("the response does not say whose copy was deleted: %s", body)
	}
	if len(rt.Spaces()) != 0 {
		t.Fatal("the space is still here after a successful DELETE")
	}

	// And doing it twice is not a success. A client that hears "ok" for a
	// space it could not find will one day say it about the wrong one.
	code, _ = do("DELETE", "/api/spaces/"+tid.Hex())
	if code != http.StatusNotFound {
		t.Fatalf("deleting a space that is not here answered %d, want 404", code)
	}
}
