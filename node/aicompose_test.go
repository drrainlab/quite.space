package node

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drrainlab/quiet_places/node/llm"
	"github.com/drrainlab/quiet_places/protocol/composition"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
)

// stubProvider returns a fixed model reply in OpenAI chat shape.
func stubProvider(t *testing.T, reply string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": reply}}},
		})
	}))
}

func aiRuntime(t *testing.T, reply string) (*Runtime, string) {
	srv := stubProvider(t, reply)
	t.Cleanup(srv.Close)
	rt := openRuntime(t, t.TempDir(), "alice")
	rt.llmClient = llm.New()
	if err := rt.SetSettings(Settings{LLM: llm.Config{
		Provider: llm.OpenAICompatible, Model: "m", BaseURL: srv.URL}}); err != nil {
		t.Fatal(err)
	}
	tid, err := rt.CreateSpaceWithCharacter("Studio", terminals.DefaultCharacter("studio"))
	if err != nil {
		t.Fatal(err)
	}
	return rt, tid.Hex()
}

func TestAIProposalValidAcceptsIntoRevision(t *testing.T) {
	rt, hexID := aiRuntime(t, `Sure! {"accent":"#8ea9ff","tint":"#0e1230","dim":520,"motion":"quiet","density":"calm"}`)
	defer rt.Close()
	tid, _ := id.ParseTerminalID(hexID)

	ov, ap, err := rt.ProposeAppearance(context.Background(), tid, "cool night sky", nil)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if ov.Accent != "#8ea9ff" || ov.Dim != 520 {
		t.Fatalf("proposal not parsed: %+v", ov)
	}
	// The proposal is valid but NOT yet committed.
	f0, _ := rt.AppearanceFrame(tid)
	s0, _ := composition.DecodeSnapshot(f0)
	if s0.Revision != 1 {
		t.Fatal("proposal auto-committed — it must stay a preview")
	}
	if err := composition.ValidateAppearance(ap); err != nil {
		t.Fatalf("proposed appearance invalid: %v", err)
	}
	// Accept = the SC-3 patch path → a new signed revision.
	f1, err := rt.PatchAppearance(tid, ov)
	if err != nil {
		t.Fatal(err)
	}
	s1, _ := composition.DecodeSnapshot(f1)
	if s1.Revision != 2 {
		t.Fatalf("accept did not bump the revision: %d", s1.Revision)
	}
}

func TestAIProposalGrammarViolationRejected(t *testing.T) {
	rt, hexID := aiRuntime(t, `{"accent":"neon-danger","motion":"strobe"}`)
	defer rt.Close()
	tid, _ := id.ParseTerminalID(hexID)
	if _, _, err := rt.ProposeAppearance(context.Background(), tid, "chaotic", nil); err == nil {
		t.Fatal("invalid proposal accepted")
	}
}

func TestAIProposalRespectsLocks(t *testing.T) {
	rt, hexID := aiRuntime(t, `{"accent":"#ff0000","dim":900}`)
	defer rt.Close()
	tid, _ := id.ParseTerminalID(hexID)
	// Pin an accent via a manual edit first.
	if _, err := rt.PatchAppearance(tid, AppearanceOverride{Accent: "#22cc88"}); err != nil {
		t.Fatal(err)
	}
	ov, _, err := rt.ProposeAppearance(context.Background(), tid, "make it red", []string{"accent"})
	if err != nil {
		t.Fatal(err)
	}
	if ov.Accent != "#22cc88" {
		t.Fatalf("locked accent was overwritten by AI: %s", ov.Accent)
	}
	if ov.Dim != 900 {
		t.Fatal("unlocked field should still come from the proposal")
	}
}
