package storage

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func navSample() NavState {
	a := id.TerminalID{1}
	b := id.TerminalID{2}
	c := id.TerminalID{3}
	return NavState{
		Version: 7,
		Pins: []NavRef{
			{Terminal: a, Label: "Anna"},
			{Terminal: b, Label: "Quiet Spaces"},
		},
		Groups: []NavGroup{
			{ID: "projects", Title: "Projects", Collapsed: true,
				Children: []NavRef{{Terminal: b, Label: "Quiet Spaces"}, {Terminal: c}}},
			{ID: "music", Title: "Music"},
		},
		Catalogs:  []NavRef{{Terminal: c, Label: "Official"}},
		Recent:    []NavRef{{Terminal: a}},
		Collapsed: []string{"people", "catalogs"},
	}
}

// Order is the whole point of this structure: it is what somebody dragged
// into place. A round trip that preserved the SET but not the SEQUENCE
// would look like it worked and lose the only thing being stored.
func TestKeystoreCarriesTheNavigator(t *testing.T) {
	k := &Keystore{DisplayName: "alice", Navigator: navSample()}
	got, err := decodeKeystore(k.encode())
	if err != nil {
		t.Fatal(err)
	}
	n := got.Navigator
	if n.Version != 7 {
		t.Fatalf("version lost: %d", n.Version)
	}
	if len(n.Pins) != 2 || n.Pins[0].Terminal != (id.TerminalID{1}) ||
		n.Pins[1].Terminal != (id.TerminalID{2}) {
		t.Fatalf("pin order changed: %+v", n.Pins)
	}
	if n.Pins[0].Label != "Anna" {
		t.Fatalf("the fallback label is gone: %+v", n.Pins[0])
	}
	if len(n.Groups) != 2 || n.Groups[0].ID != "projects" || n.Groups[1].ID != "music" {
		t.Fatalf("group order changed: %+v", n.Groups)
	}
	if !n.Groups[0].Collapsed || n.Groups[1].Collapsed {
		t.Fatalf("collapse state changed: %+v", n.Groups)
	}
	if len(n.Groups[0].Children) != 2 ||
		n.Groups[0].Children[1].Terminal != (id.TerminalID{3}) {
		t.Fatalf("group children changed: %+v", n.Groups[0].Children)
	}
	if len(n.Catalogs) != 1 || n.Catalogs[0].Label != "Official" {
		t.Fatalf("catalogs changed: %+v", n.Catalogs)
	}
	if len(n.Recent) != 1 || n.Recent[0].Terminal != (id.TerminalID{1}) {
		t.Fatalf("recent changed: %+v", n.Recent)
	}
	if len(n.Collapsed) != 2 || n.Collapsed[0] != "people" {
		t.Fatalf("collapsed sections changed: %+v", n.Collapsed)
	}

	// Encoding is deterministic — every field here is an ordered slice, so
	// there is no map iteration to sort and no excuse for drift.
	if string(k.encode()) != string(got.encode()) {
		t.Fatal("re-encoding the decoded keystore produced different bytes")
	}
}

// A keystore written before this wave has no Navigator at all and must open
// exactly as before. An upgrade is not a reason to lose someone's spaces.
func TestAKeystoreWithoutANavigatorStillOpens(t *testing.T) {
	k := &Keystore{DisplayName: "alice"}
	got, err := decodeKeystore(k.encode())
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "alice" {
		t.Fatalf("display name lost: %q", got.DisplayName)
	}
	if len(got.Navigator.Pins) != 0 || len(got.Navigator.Groups) != 0 {
		t.Fatal("a Navigator appeared from nowhere")
	}
}

// The arity tail, in the direction that actually bites: a NEWER build has
// appended fields this one does not know, and this one must skip them
// rather than stop mid-record. Every wave that forgot this made itself a
// one-way upgrade.
func TestNavigatorRecordWithExtraFieldsStillDecodes(t *testing.T) {
	got, err := readNavState(codec.NewDecoder(appendNavStateWide(nil, navSample())))
	if err != nil {
		t.Fatalf("a record from a newer build did not decode: %v", err)
	}
	if len(got.Pins) != 2 || got.Pins[0].Label != "Anna" {
		t.Fatalf("known fields were lost while skipping unknown ones: %+v", got)
	}
	if got.Pins[1].Terminal != (id.TerminalID{2}) {
		t.Fatalf("a wider ref decoded wrong: %+v", got.Pins[1])
	}
	if len(got.Groups) != 2 || got.Groups[0].Title != "Projects" ||
		len(got.Groups[0].Children) != 2 {
		t.Fatalf("group fields lost: %+v", got.Groups)
	}
	if len(got.Collapsed) != 2 || got.Version != 7 {
		t.Fatalf("state fields lost: %+v", got)
	}
}

// appendNavStateWide writes the same state exactly as a LATER build would:
// one extra element appended to every record. It deliberately does not
// reuse the production writer — the point is to produce bytes this build
// has never seen.
func appendNavStateWide(buf []byte, s NavState) []byte {
	refs := func(b []byte, rs []NavRef) []byte {
		b = codec.AppendArray(b, len(rs))
		for _, r := range rs {
			b = codec.AppendArray(b, navRefFields+1)
			b = codec.AppendBytes(b, r.Terminal[:])
			b = codec.AppendText(b, r.Label)
			b = codec.AppendText(b, "a field from the future")
		}
		return b
	}
	buf = codec.AppendArray(buf, navStateFields+1)
	buf = codec.AppendUint(buf, s.Version)
	buf = refs(buf, s.Pins)
	buf = codec.AppendArray(buf, len(s.Groups))
	for _, g := range s.Groups {
		buf = codec.AppendArray(buf, navGroupFields+1)
		buf = codec.AppendText(buf, g.ID)
		buf = codec.AppendText(buf, g.Title)
		buf = codec.AppendBool(buf, g.Collapsed)
		buf = refs(buf, g.Children)
		buf = codec.AppendUint(buf, 99)
	}
	buf = refs(buf, s.Catalogs)
	buf = refs(buf, s.Recent)
	buf = codec.AppendArray(buf, len(s.Collapsed))
	for _, c := range s.Collapsed {
		buf = codec.AppendText(buf, c)
	}
	return codec.AppendText(buf, "a whole section from the future")
}
