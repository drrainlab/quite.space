// Discover's own list, and the round trip that has to keep it (CAT-0b).
//
// The Navigator is written as a WHOLE DOCUMENT: the client PUTs everything
// it holds and takes back whatever the node returns. That shape is right —
// order is the substance of an arrangement — but it has one hazard, and it
// is the one this file exists for: a field the node forgets to carry
// through is not merely unsaved, it is ACTIVELY ERASED by the next
// unrelated write. A person adds a directory, drags a pin an hour later,
// and their directory is gone with no error anywhere.
package node

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func TestADirectorySourceSurvivesTheNodeAndARestart(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")

	saved, err := rt.SetNavigator(storage.NavState{
		Sources: []storage.NavSource{
			{Link: "qs:AAAA", Label: "community.radio", AddedAt: 1700},
		},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Sources) != 1 || saved.Sources[0].Label != "community.radio" {
		t.Fatalf("the source did not come back from the write: %+v", saved.Sources)
	}
	rt.Close()

	again := openRuntime(t, dir, "alice")
	defer again.Close()
	got := again.Navigator()
	if len(got.Sources) != 1 || got.Sources[0].Link != "qs:AAAA" ||
		got.Sources[0].AddedAt != 1700 {
		t.Fatalf("the source did not survive a restart: %+v", got.Sources)
	}
	if got.OfficialOff {
		t.Fatal("a node that expressed no preference came up with the official directory switched OFF")
	}
}

// THE HAZARD ITSELF, exercised where it actually lives: the JSON layer.
//
// The client GETs the whole document, changes one thing, and PUTs it back.
// So a field the node fails to SEND is erased on the person's next
// unrelated write, and a field it fails to READ is erased immediately —
// both silently, both with a 200. Driving this through the runtime API
// alone would prove nothing: the Go struct round-trips by definition.
func TestPinningSomethingLaterDoesNotEraseTheDirectories(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	api := &APIServer{rt: rt}

	putNav := func(body string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		api.handleSetNavigator(w, httptest.NewRequest("PUT", "/api/navigator",
			strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("navigator PUT returned %d: %s", w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	first := putNav(`{"version":0,"sources":[{"link":"qs:AAAA","label":"community.radio"}],"official_off":true}`)
	if got, _ := first["sources"].([]any); len(got) != 1 {
		t.Fatalf("the write did not answer with the directory: %v", first["sources"])
	}
	if first["official_off"] != true {
		t.Fatalf("the preference did not come back out: %v", first["official_off"])
	}

	// Now the client's next write: the document it just read back, plus a
	// pin. Whatever the node dropped on the way out is gone here.
	next, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(next, &doc); err != nil {
		t.Fatal(err)
	}
	doc["pins"] = []map[string]string{{"terminal": id.TerminalID{9}.Hex(), "label": "a space"}}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	after := putNav(string(body))

	if got, _ := after["sources"].([]any); len(got) != 1 {
		t.Fatalf("a pin write erased the directories: %v", after["sources"])
	}
	if after["official_off"] != true {
		t.Fatal("a pin write switched the official directory back on")
	}
	if got, _ := after["pins"].([]any); len(got) != 1 {
		t.Fatalf("the pin itself did not save: %v", after["pins"])
	}
}

// Switching the official directory off is a PREFERENCE, and it persists.
// Nothing here can delete a compiled-in address, so nothing here stores
// one: the only durable fact is that the person said no.
func TestTheOfficialDirectoryStaysSwitchedOff(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	if _, err := rt.SetNavigator(storage.NavState{OfficialOff: true}, 0); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	again := openRuntime(t, dir, "alice")
	defer again.Close()
	if !again.Navigator().OfficialOff {
		t.Fatal("the preference did not survive a restart")
	}
}

// Normalising rather than refusing: a person who adds a directory they
// already have should see one row, not an error about their whole
// arrangement.
func TestAddingTheSameDirectoryTwiceKeepsOneRow(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	saved, err := rt.SetNavigator(storage.NavState{
		Sources: []storage.NavSource{
			{Link: "  qs:AAAA  ", Label: "first"},
			{Link: "qs:AAAA", Label: "second"},
			{Link: "   ", Label: "nothing at all"},
			{Link: "qs:BBBB", Label: "another relay"},
		},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Sources) != 2 {
		t.Fatalf("wanted two rows, got %+v", saved.Sources)
	}
	// The FIRST of a duplicate pair wins, and the link is stored trimmed.
	if saved.Sources[0].Link != "qs:AAAA" || saved.Sources[0].Label != "first" {
		t.Fatalf("the duplicate rule kept the wrong row: %+v", saved.Sources[0])
	}
	if saved.Sources[1].Link != "qs:BBBB" {
		t.Fatalf("a different link was treated as a duplicate: %+v", saved.Sources)
	}
}

func TestTooManyDirectoriesIsRefused(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	var many []storage.NavSource
	for i := range maxNavSources + 1 {
		many = append(many, storage.NavSource{Link: string(rune('a'+i%26)) + "qs:" + string(rune('0'+i%10))})
	}
	if _, err := rt.SetNavigator(storage.NavState{Sources: many}, 0); err == nil {
		t.Fatal("an unbounded directory list was accepted")
	}
}
