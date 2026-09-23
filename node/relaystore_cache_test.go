package node

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The relay state is asked for on every route decision; it is read from
// disk once per change, not once per ask — and a change made by another
// process is still seen.
func TestRelayStateIsReadOncePerChange(t *testing.T) {
	dir := t.TempDir()
	if err := UpdateRelayStateAt(dir, func(st *RelayLocalState) { st.SelectedPrimary = "official:one" }); err != nil {
		t.Fatal(err)
	}
	a := LoadRelayStateAt(dir)
	if a.SelectedPrimary != "official:one" {
		t.Fatalf("after the update the state says %q", a.SelectedPrimary)
	}
	// A caller's edit of the returned value never reaches the next load.
	a.SelectedPrimary = "scribbled"
	a.Stats["x"] = &RelayProbeStats{}
	if b := LoadRelayStateAt(dir); b.SelectedPrimary != "official:one" || len(b.Stats) != 0 {
		t.Fatalf("a caller's scribble reached the cache: %+v", b)
	}
	// An update from outside the process (a different size, a later
	// mtime) is picked up by the next load.
	path := filepath.Join(dir, "relays.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"selected_primary":"official:two-from-outside"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if c := LoadRelayStateAt(dir); c.SelectedPrimary != "official:two-from-outside" {
		t.Fatalf("an outside write was not seen: %q", c.SelectedPrimary)
	}
	// And the in-process update after that is exact, whatever the clock.
	if err := UpdateRelayStateAt(dir, func(st *RelayLocalState) { st.SelectedPrimary = "official:three" }); err != nil {
		t.Fatal(err)
	}
	if d := LoadRelayStateAt(dir); d.SelectedPrimary != "official:three" {
		t.Fatalf("the update after the outside write reads %q", d.SelectedPrimary)
	}
}
