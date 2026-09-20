package node

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// renamerWithAKeptPublicSpace is the stand of 2026-09-18 in miniature: a node
// that owns spaces of its own and has ALSO kept a public space it only reads
// (opened from Discover, never joined).
func renamerWithAKeptPublicSpace(t *testing.T, dir string, own int) (rt *Runtime, mine []id.TerminalID, kept id.TerminalID) {
	t.Helper()
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	t.Cleanup(func() { owner.Close() })
	kept = openPublicSpaceForMirror(t, owner, "Atmospheres")
	if _, err := owner.Say(kept, "hello", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := owner.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, kept); err != nil {
		t.Fatal(err)
	}

	rt = openRuntime(t, dir, "Maya")
	for i := 0; i < own; i++ {
		tid, err := rt.CreateSpace(fmt.Sprintf("Room %d", i))
		if err != nil {
			t.Fatal(err)
		}
		mine = append(mine, tid)
	}
	if err := rt.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := rt.OpenPublicSpace(kept, addr); err != nil {
		t.Fatal(err)
	}
	return rt, mine, kept
}

// selfRevisionIn reads which revision of this node's own manifest a space
// holds (0: the space has never heard of this terminal).
func selfRevisionIn(rt *Runtime, tid id.TerminalID) uint64 {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if t, ok := rt.spaces[tid].space.Registry.Get(rt.Self.TerminalID); ok {
		return t.Manifest.Revision
	}
	return 0
}

// A space this device only READS has no name of ours to carry. The rename
// loop used to ask it anyway, take the reader gate's "join this space to
// write" for the rename's own failure, and answer 400 — after the new name
// was already in memory and in whichever writable spaces the map walk had
// reached, and before the keystore was saved. One kept public space from
// Discover made a node unable to rename at all.
func TestRenameSkipsASpaceThisDeviceOnlyReads(t *testing.T) {
	dir := t.TempDir()
	rt, mine, kept := renamerWithAKeptPublicSpace(t, dir, 6)
	rev0 := rt.Self.Manifest.Revision

	// Through the same door the "Change name" button uses.
	api, err := NewAPIServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Handler())
	req, _ := http.NewRequest("POST", srv.URL+"/api/identity/name",
		bytes.NewBufferString(`{"name":"Robert"}`))
	req.Header.Set("X-QP-Token", api.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	srv.Close()
	var got map[string]string
	_ = json.Unmarshal(body, &got)
	if resp.StatusCode != 200 || got["name"] != "Robert" || got["warning"] != "" {
		t.Fatalf("rename with a kept read-only space: %d %s", resp.StatusCode, body)
	}

	// Every space we write into carries the new revision — all of them,
	// whatever order the map walk took.
	for _, tid := range mine {
		if r := selfRevisionIn(rt, tid); r != rev0+1 {
			t.Fatalf("writable space %s holds revision %d, want %d", tid, r, rev0+1)
		}
	}
	// The read-only space was left alone — skipped, not written around.
	if r := selfRevisionIn(rt, kept); r != 0 {
		t.Fatalf("a reader replica took our manifest (revision %d)", r)
	}

	// And it is a rename, not a mood: it survives a restart.
	rt.Close()
	rt2 := openRuntime(t, dir, "")
	defer rt2.Close()
	if rt2.DisplayName() != "Robert" || rt2.Self.Manifest.Revision != rev0+1 {
		t.Fatalf("after restart: %q at revision %d", rt2.DisplayName(), rt2.Self.Manifest.Revision)
	}
	if err := rt2.SetName("Bobby"); err != nil {
		t.Fatalf("second rename: %v", err)
	}
}

// A frozen space refuses everyone, its owner included. That is an ordinary
// state for a space — the open path already treats it as a diagnostic and
// republishes when it can — so it must not stop a person changing their name.
func TestRenameDefersAFrozenSpace(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "Maya")
	defer rt.Close()
	open, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}
	frozen := openPublicSpaceForMirror(t, rt, "Archive")
	yes := true
	if err := rt.RevisePolicy(frozen, PolicyDelta{Frozen: &yes}); err != nil {
		t.Fatal(err)
	}
	rev0 := rt.Self.Manifest.Revision
	if err := rt.SetName("Robert"); err != nil {
		t.Fatalf("rename with a frozen space: %v", err)
	}
	if r := selfRevisionIn(rt, open); r != rev0+1 {
		t.Fatalf("the open space holds revision %d, want %d", r, rev0+1)
	}
	noted := false
	for _, ref := range rt.IngressRefusals() {
		if ref.Space == frozen && ref.Reason == "self_manifest_deferred" {
			noted = true
		}
	}
	if !noted {
		t.Fatal("the deferral left no diagnostic")
	}
}

// nodeWithOneBrokenSpace owns five spaces, one of which refuses every frame
// for a reason that is nobody's policy — a real failure, not a hold.
func nodeWithOneBrokenSpace(t *testing.T, dir string) (rt *Runtime, mine []id.TerminalID, broken id.TerminalID) {
	t.Helper()
	rt = openRuntime(t, dir, "Maya")
	for i := 0; i < 5; i++ {
		tid, err := rt.CreateSpace(fmt.Sprintf("Room %d", i))
		if err != nil {
			t.Fatal(err)
		}
		mine = append(mine, tid)
	}
	broken = mine[2]
	rt.mu.Lock()
	rt.spaces[broken].space.Log.SetAdmit(func(*signal.Envelope) error {
		return errors.New("disk on fire")
	})
	rt.mu.Unlock()
	return rt, mine, broken
}

// A REAL failure in a space we write into is still said out loud — but as
// what it is. The name is saved first (a published revision cannot be taken
// back, so nothing else can be made whole), every other space still gets it,
// and the error says the rename happened.
func TestRenameReportsARealFailureWithoutUndoingTheRename(t *testing.T) {
	dir := t.TempDir()
	rt, mine, broken := nodeWithOneBrokenSpace(t, dir)
	rev0 := rt.Self.Manifest.Revision

	err := rt.SetName("Robert")
	var unpublished *RenameUnpublishedError
	if !errors.As(err, &unpublished) {
		t.Fatalf("want *RenameUnpublishedError, got %v", err)
	}
	if len(unpublished.Spaces) != 1 || unpublished.Spaces[0] != broken ||
		!strings.Contains(err.Error(), "disk on fire") ||
		!strings.Contains(err.Error(), `your name is now "Robert"`) {
		t.Fatalf("the error does not say what happened: %v", err)
	}
	for _, tid := range mine {
		want := rev0 + 1
		if tid == broken {
			want = rev0
		}
		if r := selfRevisionIn(rt, tid); r != want {
			t.Fatalf("space %s holds revision %d, want %d", tid, r, want)
		}
	}

	// Saved: the restart agrees with what the person was told, and the open
	// path carries the name into the space that missed it — the retry the
	// error promised.
	rt.Close()
	rt2 := openRuntime(t, dir, "")
	defer rt2.Close()
	if rt2.DisplayName() != "Robert" {
		t.Fatalf("after restart: %q", rt2.DisplayName())
	}
	if r := selfRevisionIn(rt2, broken); r != rev0+1 {
		t.Fatalf("the open path did not republish: revision %d, want %d", r, rev0+1)
	}
}

// The handler over the same shortfall: changed, with a warning beside the
// name — never a 400 over a done thing, which left the client showing the
// old name for a node that already answered to the new one.
func TestRenameHandlerAnswersChangedWithAWarning(t *testing.T) {
	rt, _, _ := nodeWithOneBrokenSpace(t, t.TempDir())
	defer rt.Close()
	api, err := NewAPIServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/identity/name",
		bytes.NewBufferString(`{"name":"Robert"}`))
	req.Header.Set("X-QP-Token", api.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var got map[string]string
	_ = json.Unmarshal(body, &got)
	if resp.StatusCode != 200 || got["name"] != "Robert" ||
		!strings.Contains(got["warning"], "disk on fire") {
		t.Fatalf("handler over a partial rename: %d %s", resp.StatusCode, body)
	}
}
