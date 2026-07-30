package node

import (
	"testing"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// ---- the People discriminator ----

// The regression this field exists for. displayFor puts a chosen name AHEAD
// of the projection, so a conversation with one person that somebody
// renamed stops saying "space.with_one" — and a People section reading that
// key would quietly move the row into Spaces. Classification is structural;
// naming is presentation.
func TestRenamingAConversationDoesNotStopItBeingAPerson(t *testing.T) {
	me := id.PrincipalID{1}
	cards := []memberLike{{principal: me, name: "alice"}, {principal: id.PrincipalID{2}, name: "anna"}}

	if !isDisplayDyad(cards, me) {
		t.Fatal("a two-person space is not a dyad")
	}
	// The name a person chose changes what it is CALLED, and nothing else.
	named := displayFor(spaceNaming{LocalTitle: "Anna — work"}, nil, me)
	if named.Key == "space.with_one" {
		t.Fatal("test premise wrong: a named space still projects")
	}
	if !isDisplayDyad(cards, me) {
		t.Fatal("renaming changed what the space IS")
	}
}

func TestDyadCountsPeopleNotTerminals(t *testing.T) {
	me := id.PrincipalID{1}
	anna := id.PrincipalID{2}

	// Anna on a laptop and a phone is one person.
	if !isDisplayDyad([]memberLike{
		{principal: me, name: "alice"},
		{principal: anna, name: "anna"},
		{principal: anna, name: "anna"},
	}, me) {
		t.Fatal("two devices of one person read as two people")
	}
	// Three people is not a dyad.
	if isDisplayDyad([]memberLike{
		{principal: me, name: "alice"},
		{principal: anna, name: "anna"},
		{principal: id.PrincipalID{3}, name: "bob"},
	}, me) {
		t.Fatal("a three-person space reads as a person")
	}
	// Alone is not a dyad either.
	if isDisplayDyad([]memberLike{{principal: me, name: "alice"}}, me) {
		t.Fatal("an empty space reads as a person")
	}
}

// Somebody whose manifest has not arrived yet is still somebody. Under the
// old key-based rule they projected "space.someone_arrived" and silently
// were not a person.
func TestSomebodyWhoseNameHasNotArrivedIsStillAPerson(t *testing.T) {
	me := id.PrincipalID{1}
	cards := []memberLike{{principal: me, name: "alice"}, {principal: id.PrincipalID{2}}}
	if !isDisplayDyad(cards, me) {
		t.Fatal("a person without a name yet is not counted as a person")
	}
	if d := projectDisplay(cards, me); d.Key != "space.someone_arrived" {
		t.Fatalf("premise wrong: %+v", d)
	}
}

// A card claiming no principal at all must not collapse with every other
// such card into one imaginary person.
func TestCardsWithoutAPrincipalDoNotCollapse(t *testing.T) {
	me := id.PrincipalID{1}
	cards := []memberLike{
		{principal: me, name: "alice"},
		{name: "sensor one"},
		{name: "sensor two"},
	}
	if isDisplayDyad(cards, me) {
		t.Fatal("two principal-less terminals collapsed into one person")
	}
	names, unnamed := peerCount(cards, me)
	if len(names) != 2 || unnamed != 0 {
		t.Fatalf("counted %v / %d", names, unnamed)
	}
}

// The live path: the flag reaches the space list.
func TestTheSpaceListCarriesTheDyadFlag(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpaceUnnamed()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range rt.Spaces() {
		if s.ID == tid && s.Dyad {
			t.Fatal("a space with nobody else in it is not a conversation with a person")
		}
	}
}

// ---- the Navigator ----

func navFixture(a, b id.TerminalID) storage.NavState {
	return storage.NavState{
		Pins: []storage.NavRef{{Terminal: a, Label: "Anna"}, {Terminal: b}},
		Groups: []storage.NavGroup{
			{ID: "projects", Title: "Projects", Children: []storage.NavRef{{Terminal: b}}},
			{ID: "music", Title: "Music", Children: []storage.NavRef{{Terminal: b}}},
		},
		Collapsed: []string{"people"},
	}
}

// The headline for NAV-0: an arrangement survives a restart, in order.
func TestTheNavigatorSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	a, b := id.TerminalID{7}, id.TerminalID{8}

	saved, err := rt.SetNavigator(navFixture(a, b), 0)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != 1 {
		t.Fatalf("version did not advance: %d", saved.Version)
	}
	rt.Close()

	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	got := rt2.Navigator()
	if got.Version != 1 {
		t.Fatalf("version lost: %d", got.Version)
	}
	if len(got.Pins) != 2 || got.Pins[0].Terminal != a || got.Pins[1].Terminal != b {
		t.Fatalf("pin order lost: %+v", got.Pins)
	}
	if got.Pins[0].Label != "Anna" {
		t.Fatal("the fallback label is gone, so a dangling pin would show hex")
	}
	if len(got.Groups) != 2 || got.Groups[0].ID != "projects" {
		t.Fatalf("group order lost: %+v", got.Groups)
	}
}

// One terminal in two groups is still one terminal, and deleting a group
// touches neither the other group nor the space itself.
func TestOneItemLivesInSeveralGroups(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	a, b := id.TerminalID{7}, id.TerminalID{8}
	before := len(rt.Spaces())

	st, err := rt.SetNavigator(navFixture(a, b), 0)
	if err != nil {
		t.Fatal(err)
	}
	st.Groups = st.Groups[:1] // delete "music"
	st, err = rt.SetNavigator(st, st.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Groups) != 1 || len(st.Groups[0].Children) != 1 {
		t.Fatalf("deleting one group disturbed the other: %+v", st.Groups)
	}
	if len(rt.Spaces()) != before {
		t.Fatal("organising links changed the spaces themselves")
	}
}

// A concurrent write is refused LOUDLY. The Settings blob reverts silently
// and nobody notices; an ordered document must not.
func TestAStaleNavigatorWriteIsRefused(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	a := id.TerminalID{7}

	first, err := rt.SetNavigator(storage.NavState{Pins: []storage.NavRef{{Terminal: a}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Somebody else already moved on; this write is based on version 0.
	if _, err := rt.SetNavigator(storage.NavState{}, 0); err == nil {
		t.Fatal("a write based on a stale version was accepted")
	}
	if got := rt.Navigator(); len(got.Pins) != 1 || got.Version != first.Version {
		t.Fatalf("the refused write changed something: %+v", got)
	}
}

// A local API is still an API.
func TestTheNavigatorRefusesNonsense(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	tooMany := storage.NavState{Pins: make([]storage.NavRef, maxNavPins+1)}
	if _, err := rt.SetNavigator(tooMany, 0); err == nil {
		t.Fatal("an unbounded pin list was accepted")
	}
	dup := storage.NavState{Groups: []storage.NavGroup{{ID: "x", Title: "A"}, {ID: "x", Title: "B"}}}
	if _, err := rt.SetNavigator(dup, 0); err == nil {
		t.Fatal("two groups with one id were accepted")
	}
	noID := storage.NavState{Groups: []storage.NavGroup{{Title: "A"}}}
	if _, err := rt.SetNavigator(noID, 0); err == nil {
		t.Fatal("a group with no id was accepted")
	}
	// And a refusal must not have written anything.
	if rt.Navigator().Version != 0 {
		t.Fatal("a refused write still advanced the version")
	}
}

// A reference to a space this node does not have is NOT an error. It is a
// dangling row with a remove action, and a space that comes back finds its
// pin waiting — so nothing is ever swept.
func TestAPinToAnUnknownSpaceIsKept(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	ghost := id.TerminalID{0xAB}

	if _, err := rt.SetNavigator(storage.NavState{
		Pins: []storage.NavRef{{Terminal: ghost, Label: "Anna"}},
	}, 0); err != nil {
		t.Fatalf("a reference to an absent space was refused: %v", err)
	}
	got := rt.Navigator()
	if len(got.Pins) != 1 || got.Pins[0].Label != "Anna" {
		t.Fatalf("the dangling pin was swept or stripped: %+v", got.Pins)
	}
}

// The handed-out state must not alias the keystore.
func TestNavigatorReadsAreCopies(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	if _, err := rt.SetNavigator(navFixture(id.TerminalID{1}, id.TerminalID{2}), 0); err != nil {
		t.Fatal(err)
	}
	got := rt.Navigator()
	got.Pins[0].Label = "clobbered"
	got.Groups[0].Children[0].Terminal = id.TerminalID{9}
	if again := rt.Navigator(); again.Pins[0].Label != "Anna" ||
		again.Groups[0].Children[0].Terminal != (id.TerminalID{2}) {
		t.Fatal("a caller mutated the keystore through a returned slice")
	}
}
