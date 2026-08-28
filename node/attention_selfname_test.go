package node

import (
	"testing"

	"github.com/drrainlab/quiet_places/attention"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// The person's own display name is ALWAYS watched — no alias setup needed.
// The first thing a live user hit: "@bob" typed as plain text (no signed
// mention) fired nothing, because aliases default to empty. The tokenizer
// strips the @, the seeded name matches, and the reason says name_in_text —
// soft, not personal: typed text is a hint, a signed mention is a fact.
func TestOwnDisplayNameIsWatchedWithoutAliasSetup(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	pairWithHistory(t, alice, bob, "Workshop", func(tid id.TerminalID) {
		if _, err := alice.Say(tid, "@bob взгляни на это, пожалуйста", SayOptions{}); err != nil {
			t.Fatal(err)
		}
	})

	sigs := bob.Signals()
	if len(sigs) == 0 {
		t.Fatal("a typed @name produced no signal with default settings")
	}
	if !hasCode(sigs[0].Reasons, attention.ReasonNameInText) {
		t.Fatalf("the reason is not name_in_text: %+v", sigs[0].Reasons)
	}
	if sigs[0].Personal {
		t.Fatal("typed text must stay a hint — personal is for signed mentions")
	}
}
