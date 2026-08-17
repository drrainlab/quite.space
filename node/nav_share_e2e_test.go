// The NAV+SHARE wave, end to end.
//
// Every gate has its own tests; this one asserts the sentences the wave
// exists for, on real nodes talking over a real relay:
//
//	one act sends one independent copy to each place, and the copies
//	carry no way back to where they came from and no sign of each other
//
//	the assistant is a destination like any other, and nothing that
//	reaches it leaves the device
//
//	the arrangement a person made is theirs and survives a restart
package node

import (
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// waitForText polls a space until something says what we are waiting for.
// Real nodes over a real relay do not arrive on a schedule.
func waitForText(t *testing.T, rt *Runtime, tid id.TerminalID, want string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range textsOf(t, rt, tid) {
			if strings.Contains(s, want) {
				return s
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("%q never arrived; the space holds %q", want, textsOf(t, rt, tid))
	return ""
}

// shareTogether builds a two-person space over the relay and returns it.
func shareTogether(t *testing.T, owner, guest *Runtime, addr, title string) id.TerminalID {
	t.Helper()
	tid, err := owner.CreateSpace(title)
	if err != nil {
		t.Fatal(err)
	}
	pass, err := owner.MintPass(tid, 1, 1, addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := guest.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, guest, req, JoinReady)
	return tid
}

func TestTheWaveEndToEnd(t *testing.T) {
	aliceDir, carolDir := t.TempDir(), t.TempDir()
	alice := openRuntime(t, aliceDir, "alice")
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	carol := openRuntime(t, carolDir, "carol")
	srv, addr := setUpRelay(t, alice, bob, carol)
	defer srv.Close()

	// A is where bob says something. P is a second place alice shares with
	// bob — a dyad, which is what "a person" means in the Navigator. C is
	// carol's, and carol has never heard of A.
	A := shareTogether(t, alice, bob, addr, "field notes")
	P := shareTogether(t, alice, bob, addr, "just us")
	C := shareTogether(t, alice, carol, addr, "the reading group")
	ai, err := alice.EnsureAISpace()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := bob.Say(A, "the tide comes in twice a day", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForText(t, alice, A, "the tide comes in")
	// Through withSpace, like everything else here: the relay sync goroutine
	// is live and holds r.mu while it applies what bob sent.
	var srcEvent id.EventID
	if err := alice.withSpace(A, func(st *spaceState) error {
		for _, e := range st.space.State.Entries() {
			if e.Content.Text != nil && strings.Contains(e.Content.Text.Text, "the tide") {
				srcEvent = e.ID
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if srcEvent == (id.EventID{}) {
		t.Fatal("bob's message never reached alice")
	}

	// ---- one act, three destinations ----

	res, err := alice.Share(A, srcEvent, []id.TerminalID{C, P, ai}, ShareOptions{
		Comment: "worth remembering", NameAuthor: true, NameSource: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Fatalf("three destinations need three answers: %+v", res)
	}
	for _, r := range res {
		if !r.OK {
			t.Fatalf("a destination was missed: %+v", r)
		}
		if r.Comment == "" || r.Copy == "" {
			t.Fatalf("a destination did not get both events: %+v", r)
		}
	}

	// Two events per place, in order, everywhere — including the assistant.
	for _, place := range []id.TerminalID{C, P, ai} {
		texts := textsOf(t, alice, place)
		if len(texts) < 2 {
			t.Fatalf("%s holds %d messages", place.Hex()[:8], len(texts))
		}
		if texts[len(texts)-2] != "worth remembering" {
			t.Fatalf("the comment is not the message before the copy: %q", texts[len(texts)-2])
		}
		o, _ := originOf(t, alice, place)
		if o == nil || o.AuthorLabel != "bob" {
			t.Fatalf("the quotation does not name bob: %+v", o)
		}
		if o.SourceLabel != "" {
			t.Fatalf("the source space was named without being asked: %q", o.SourceLabel)
		}
	}

	// ---- what carol actually receives ----

	carolCopy := waitForText(t, carol, C, "the tide comes in twice a day")
	if !strings.Contains(carolCopy, "> bob") {
		t.Fatalf("carol cannot see whose words they are: %q", carolCopy)
	}
	waitForText(t, carol, C, "worth remembering")

	// Carol is a node holding ONLY space C. Nothing that reached her names
	// the space it came from, the message it was, or anybody else it went
	// to — the three things a quotation deliberately does not carry.
	forbidden := map[string]string{
		A.Hex():        "the source space id",
		P.Hex():        "another recipient",
		ai.Hex():       "the assistant's space",
		srcEvent.Hex(): "the original event id",
		"field notes":  "the source space's name",
		"just us":      "another recipient's name",
	}
	for _, said := range textsOf(t, carol, C) {
		for needle, what := range forbidden {
			if strings.Contains(said, needle) {
				t.Fatalf("carol's copy leaks %s: %q", what, said)
			}
		}
	}
	// And carol has no replica of the source at all.
	for _, s := range carol.Spaces() {
		if s.ID == A || s.ID == P || s.ID == ai {
			t.Fatalf("carol ended up holding %s", s.ID.Hex()[:8])
		}
	}

	// ---- the assistant's copy never left the device ----

	for _, tid := range alice.announcedSpaces() {
		if tid == ai {
			t.Fatal("the assistant's space was announced on the LAN")
		}
	}
	for _, tid := range alice.relayMailboxSpaces() {
		if tid == ai {
			t.Fatal("the relay would be asked about the assistant's space")
		}
	}
	client, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	bucket := relay.Bucket(uint64(time.Now().Unix()))
	var hints [][]byte
	for b := bucket - 1; b <= bucket+1; b++ {
		hints = append(hints, relay.HintFor(ai, alice.Device.ID, b),
			relay.HintPublicOutbox(ai, b))
	}
	items, err := client.Fetch(hints)
	client.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("the relay holds %d items for the assistant's space", len(items))
	}

	// ---- the arrangement is alice's, and it is hers ----

	nav, err := alice.SetNavigator(storage.NavState{
		Pins: []storage.NavRef{{Terminal: C, Label: "the reading group"}},
		Groups: []storage.NavGroup{{ID: "g1", Title: "Reading",
			Children: []storage.NavRef{{Terminal: C}, {Terminal: P}}}},
		Collapsed: []string{"people"},
		Recent:    alice.Navigator().Recent,
	}, alice.Navigator().Version)
	if err != nil {
		t.Fatal(err)
	}
	// Sharing put every destination in Recent, newest first.
	if len(nav.Recent) < 3 {
		t.Fatalf("the places just sent to are not remembered: %+v", nav.Recent)
	}

	// ---- both restart ----

	alice.Close()
	carol.Close()
	alice2 := openRuntime(t, aliceDir, "alice")
	defer alice2.Close()
	s := alice2.GetSettings()
	s.Relay = addr
	if err := alice2.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	carol2 := openRuntime(t, carolDir, "carol")
	defer carol2.Close()

	got := alice2.Navigator()
	if len(got.Pins) != 1 || got.Pins[0].Terminal != C {
		t.Fatalf("the pin did not survive: %+v", got.Pins)
	}
	if len(got.Groups) != 1 || len(got.Groups[0].Children) != 2 {
		t.Fatalf("the group did not survive: %+v", got.Groups)
	}
	if len(got.Recent) < 3 {
		t.Fatalf("Recent did not survive: %+v", got.Recent)
	}
	// The quotation is still a quotation after a restart, on both sides.
	if o, _ := originOf(t, alice2, C); o == nil || o.AuthorLabel != "bob" {
		t.Fatalf("alice lost the provenance across a restart: %+v", o)
	}
	if o, _ := originOf(t, carol2, C); o == nil || o.AuthorLabel != "bob" {
		t.Fatalf("carol lost the provenance across a restart: %+v", o)
	}
	// And the assistant is still local-only, still hers, still an agent.
	if !alice2.ks.Spaces[ai].LocalOnly {
		t.Fatal("the assistant's space stopped being local-only")
	}
	if alice2.agent == nil || alice2.agent.Principal != alice2.Principal.ID {
		t.Fatal("the assistant came back as somebody else")
	}
}
