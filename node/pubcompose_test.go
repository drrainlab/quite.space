package node

import (
	"context"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// A valid AI proposal becomes a validated draft document (never published);
// a grammar-violating proposal is rejected outright.
func TestAIDocumentProposal(t *testing.T) {
	reply := `{"kind":"release","title":"Grain & Silk","summary":"EP notes.",
	  "tags":["music"],"blocks":[
	  {"id":"b1","type":"heading","props":{"text":"Grain & Silk"}},
	  {"id":"b2","type":"text","props":{"text":"Recorded at night."}},
	  {"id":"b3","type":"credits","props":{"items":["music","Robert"]}}]}`
	rt, hexID := aiRuntime(t, reply)
	defer rt.Close()
	tid, _ := id.ParseTerminalID(hexID)

	doc, err := rt.ProposeDocument(context.Background(), tid, "release post")
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if doc.Title != "Grain & Silk" || len(doc.Blocks) != 3 {
		t.Fatalf("proposal not parsed: %+v", doc)
	}
	// Not published: the projection stays empty until the user publishes.
	sp, _ := rt.Space(tid)
	if len(sp.State.Publications()) != 0 {
		t.Fatal("AI proposal auto-published")
	}

	// A proposal violating the grammar (unknown block type) is rejected.
	rt2, hexID2 := aiRuntime(t, `{"kind":"note","title":"X","blocks":[{"id":"b1","type":"iframe","props":{"text":"x"}}]}`)
	defer rt2.Close()
	tid2, _ := id.ParseTerminalID(hexID2)
	if _, err := rt2.ProposeDocument(context.Background(), tid2, "sneaky"); err == nil ||
		!strings.Contains(err.Error(), "unknown block type") {
		t.Fatalf("grammar violation accepted: %v", err)
	}
}
