package node

import (
	"fmt"
	"testing"

	"github.com/drrainlab/quiet_places/attention"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// A direct question from someone else surfaces; the same words from yourself
// do not.
func TestAttentionSurfacesQuestionsFromOthers(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	tid := pairWithHistory(t, alice, bob, "Workshop", func(tid id.TerminalID) {
		if _, err := alice.Say(tid, "посмотришь конфиг реле до вечера?", SayOptions{}); err != nil {
			t.Fatal(err)
		}
	})

	sigs := bob.Signals()
	if len(sigs) == 0 {
		t.Fatal("a direct question produced no signal")
	}
	if sigs[0].Excerpt == "" || sigs[0].SpaceHex != tid.Hex() {
		t.Fatalf("signal does not point back at the source: %+v", sigs[0])
	}
	// Alice's own words are not news to Alice.
	if got := alice.Signals(); len(got) != 0 {
		t.Fatalf("own message became a signal: %+v", got)
	}
}

// A signed mention is a hard signal and reaches priority.
func TestAttentionMentionIsHard(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	pairWithHistory(t, alice, bob, "Workshop", func(tid id.TerminalID) {
		if _, err := alice.Say(tid, "нужен взгляд на это", SayOptions{
			Mentions: []id.PrincipalID{bob.Principal.ID},
		}); err != nil {
			t.Fatal(err)
		}
	})

	sigs := bob.Signals()
	if len(sigs) == 0 {
		t.Fatal("mention produced no signal")
	}
	s := sigs[0]
	if !s.Hard {
		t.Fatalf("mention not marked hard: %+v", s)
	}
	if s.Delivery != attention.DeliveryPriority {
		t.Fatalf("hard mention did not reach priority: %s", s.Delivery)
	}
	if !hasCode(s.Reasons, attention.ReasonMention) {
		t.Fatalf("mention reason missing: %+v", s.Reasons)
	}
}

// Off means off, and a muted space is not scanned at all.
func TestAttentionOffAndMutedSpace(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	pol := bob.AttentionPolicy()
	pol.Mode = attention.ModeOff
	if err := bob.SetAttentionPolicy(pol); err != nil {
		t.Fatal(err)
	}
	tid := pairWithHistory(t, alice, bob, "Workshop", func(tid id.TerminalID) {
		if _, err := alice.Say(tid, "@bob посмотришь?", SayOptions{
			Mentions: []id.PrincipalID{bob.Principal.ID},
		}); err != nil {
			t.Fatal(err)
		}
	})
	if got := bob.Signals(); len(got) != 0 {
		t.Fatalf("Off still produced signals: %+v", got)
	}

	// Back on, but this space muted: still nothing.
	pol.Mode = attention.ModeMinimal
	pol.Spaces = map[string]attention.Scope{tid.Hex(): attention.ScopeOff}
	if err := bob.SetAttentionPolicy(pol); err != nil {
		t.Fatal(err)
	}
	if got := bob.Signals(); len(got) != 0 {
		t.Fatalf("muted space still produced signals: %+v", got)
	}
}

// The signal store survives a restart, and received_at does not drift.
func TestAttentionSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, dir, "bob")
	pairWithHistory(t, alice, bob, "Workshop", func(tid id.TerminalID) {
		if _, err := alice.Say(tid, "подтвердишь встречу?", SayOptions{}); err != nil {
			t.Fatal(err)
		}
	})
	before := bob.Signals()
	if len(before) == 0 {
		t.Fatal("no signal before restart")
	}
	bob.MarkSignalsSeen("") // force a synchronous persist
	bob.Close()

	bob2 := openRuntime(t, dir, "bob")
	defer bob2.Close()
	after := bob2.Signals()
	if len(after) == 0 {
		t.Fatal("signals lost across restart")
	}
	if after[0].ReceivedAt != before[0].ReceivedAt {
		t.Fatalf("received_at drifted across restart: %d → %d",
			before[0].ReceivedAt, after[0].ReceivedAt)
	}
	// And it is not re-judged into a duplicate.
	if len(bob2.Signals()) != len(after) {
		t.Fatal("rescan duplicated a signal")
	}
}

// Deleting the profile really deletes it.
func TestForgetAttentionClearsEverything(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	pairWithHistory(t, alice, bob, "Workshop", func(tid id.TerminalID) {
		if _, err := alice.Say(tid, "проверишь реле?", SayOptions{}); err != nil {
			t.Fatal(err)
		}
	})
	if len(bob.Signals()) == 0 {
		t.Fatal("nothing to forget")
	}
	if err := bob.ForgetAttention(); err != nil {
		t.Fatal(err)
	}
	st := bob.attn()
	st.mu.Lock()
	n := len(st.engine.Inbox.Signals)
	trained := st.engine.Model.Trained()
	st.mu.Unlock()
	if n != 0 || trained {
		t.Fatalf("forget left state behind: %d signals, trained=%v", n, trained)
	}
}

func hasCode(rs []attention.Reason, code string) bool {
	for _, r := range rs {
		if r.Code == code {
			return true
		}
	}
	return false
}

// pairWithHistory: alice creates a space, bob joins it, alice writes, and the
// events cross through an in-process blind relay. Attention tests are about
// ranking, so this keeps transport out of the way while still exercising the
// real path an event travels before it can ever become a signal.
func pairWithHistory(t *testing.T, alice, bob *Runtime, title string, write func(tid id.TerminalID)) id.TerminalID {
	t.Helper()
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	tid, err := alice.CreateSpace(title)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	write(tid)
	if _, _, err := alice.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	return tid
}
