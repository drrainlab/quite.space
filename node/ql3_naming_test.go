package node

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// displayOf reads a space's projected name the way the list does.
func displayOf(t *testing.T, rt *Runtime, tid id.TerminalID) SpaceDisplay {
	t.Helper()
	for _, s := range rt.Spaces() {
		if s.ID == tid {
			return s.Display
		}
	}
	t.Fatalf("space %s not listed", tid.Hex())
	return SpaceDisplay{}
}

// An unnamed space says who is in it. Nobody yet, one person, several —
// each is a different sentence, and none of them is a leftover sentinel
// like "my line".
func TestAnUnnamedSpaceShowsWhoIsInIt(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	tid, err := rt.CreateSpaceUnnamed()
	if err != nil {
		t.Fatal(err)
	}
	d := displayOf(t, rt, tid)
	if d.Key != "space.waiting" {
		t.Fatalf("an empty space should say it is waiting, said %+v", d)
	}
	if d.Text != "" {
		t.Fatal("a projected name must not also carry a literal text")
	}
}

// The multi-device break: MemberCards is per TERMINAL, so one person on two
// devices used to be three cards and the projection silently fell back to
// the raw title. People are counted by person.
func TestOnePersonOnTwoDevicesIsStillOnePerson(t *testing.T) {
	me := id.PrincipalID{1}
	other := id.PrincipalID{2}
	cards := []memberLike{
		{principal: me, name: "alice"},
		{principal: other, name: "bob"},
		{principal: other, name: "bob"}, // bob's phone
	}
	d := projectDisplay(cards, me)
	if d.Key != "space.with_one" {
		t.Fatalf("two devices of one person read as a crowd: %+v", d)
	}
	if len(d.Names) != 1 || d.Names[0] != "bob" {
		t.Fatalf("names deduped wrong: %+v", d.Names)
	}
}

// My own second device is me, not company.
func TestMyOtherDeviceIsNotSomebodyElse(t *testing.T) {
	me := id.PrincipalID{1}
	cards := []memberLike{
		{principal: me, name: "alice"},
		{principal: me, name: "alice"}, // my laptop
	}
	d := projectDisplay(cards, me)
	if d.Key != "space.waiting" {
		t.Fatalf("my own devices looked like other people: %+v", d)
	}
}

// A three-person unnamed space must not be called "my line" — the sentinel
// was never a name, and after per-link spaces it is not even related.
func TestAThreePersonUnnamedSpaceDoesNotSayMyLine(t *testing.T) {
	me := id.PrincipalID{1}
	cards := []memberLike{
		{principal: me, name: "alice"},
		{principal: id.PrincipalID{2}, name: "bob"},
		{principal: id.PrincipalID{3}, name: "carol"},
	}
	d := projectDisplay(cards, me)
	if d.Key != "space.with_many" {
		t.Fatalf("a group did not project as one: %+v", d)
	}
	if strings.Contains(strings.Join(d.Names, " "), "line") {
		t.Fatalf("the sentinel leaked into a name: %+v", d.Names)
	}
	if len(d.Names) != 2 {
		t.Fatalf("expected the two others, got %+v", d.Names)
	}
}

// Somebody arrived whose manifest has not landed: say that, rather than a
// hex prefix or an empty space.
func TestSomeoneWithoutANameStillCounts(t *testing.T) {
	me := id.PrincipalID{1}
	cards := []memberLike{
		{principal: me, name: "alice"},
		{principal: id.PrincipalID{2}, name: ""},
	}
	d := projectDisplay(cards, me)
	if d.Key != "space.someone_arrived" {
		t.Fatalf("an unnamed arrival was mishandled: %+v", d)
	}
}

// A name this node chose wins over everything, including a manifest title.
func TestALocalNameWinsOverEverything(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Fieldnotes")
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.SetLocalTitle(tid, "Exhibition prep"); err != nil {
		t.Fatal(err)
	}
	d := displayOf(t, rt, tid)
	if d.Text != "Exhibition prep" {
		t.Fatalf("the local name did not win: %+v", d)
	}
	// And it is LOCAL: nothing was signed, nothing published.
	rt.mu.Lock()
	meta := rt.ks.Spaces[tid]
	rt.mu.Unlock()
	if meta.Title != "Fieldnotes" {
		t.Fatalf("renaming locally rewrote the shared title: %q", meta.Title)
	}
}

// A space somebody deliberately named is never overridden by a projection.
func TestANamedSpaceIsNeverOverridden(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Fieldnotes")
	if err != nil {
		t.Fatal(err)
	}
	if d := displayOf(t, rt, tid); d.Text != "Fieldnotes" {
		t.Fatalf("a named space was projected over: %+v", d)
	}
}

// A legacy "my line" keeps working: it falls into the projection rather
// than showing a sentinel nobody chose.
func TestALegacyMyLineProjectsRatherThanShowingItsSentinel(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace(LineTitle)
	if err != nil {
		t.Fatal(err)
	}
	if d := displayOf(t, rt, tid); d.Text == LineTitle {
		t.Fatalf("the legacy sentinel was shown as a name: %+v", d)
	}
}

// The local title survives a restart — a name is not a session.
func TestALocalNameSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpace("Fieldnotes")
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.SetLocalTitle(tid, "Exhibition prep"); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	rt2.mu.Lock()
	meta := rt2.ks.Spaces[tid]
	rt2.mu.Unlock()
	if meta.LocalTitle != "Exhibition prep" {
		t.Fatalf("the local name did not survive: %q", meta.LocalTitle)
	}
	var _ storage.SpaceMeta = meta
}
