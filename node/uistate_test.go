package node

import (
	"strings"
	"testing"
)

func TestTheInterfaceStateSurvivesAReopenAndIsSealed(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "ann")
	if got := string(rt.UIState()); got != "{}" {
		t.Fatalf("no state yet must read as an empty object, got %q", got)
	}
	doc := `{"place":{"last":"abc","rooms":{"abc":{"end":false,"eid":"e1"}}}}`
	if err := rt.SetUIState([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	if err := rt.SetUIState([]byte(`["not","an","object"]`)); err == nil {
		t.Fatal("only an object may be stored")
	}
	if err := rt.SetUIState([]byte(`{"x":"` + strings.Repeat("a", uiStateMaxBytes) + `"}`)); err == nil {
		t.Fatal("the document is bounded")
	}
	rt.Close()

	// A NEW PROCESS, as far as the node can tell — which on Android is also a
	// new browser origin, the reason this exists.
	rt2 := openRuntime(t, dir, "ann")
	defer rt2.Close()
	if got := string(rt2.UIState()); got != doc {
		t.Fatalf("the state did not survive a reopen: %q", got)
	}
}
