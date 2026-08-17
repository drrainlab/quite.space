package node

import (
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/agent"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// ---- the identity decisions ----

// The reason a second participant exists at all: authorship is enforced
// against the EMITTING participant's own manifest, so a human terminal
// cannot sign as a machine and a machine terminal cannot sign as a person.
func TestNeitherTerminalCanSignAsTheOther(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("room")
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)

	payload, err := (&schemas.TextMessage{Text: "hello"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Self.Emit(sp, schemas.MessageText, payload,
		signal.AuthorshipAIAgent, now()); err == nil {
		t.Fatal("a human terminal signed as a machine")
	}

	ag, err := terminals.NewParticipant(agent.Template("Quite AI"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Emit(sp, schemas.MessageText, payload,
		signal.AuthorshipHuman, now()); err == nil {
		t.Fatal("an agent terminal signed as a person")
	}
}

// The assistant is controlled by the person's own principal — it is an
// assistant somebody runs, not an independent subject.
func TestTheAssistantIsControlledByYouAndIsNotYou(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	if _, err := rt.EnsureAISpace(); err != nil {
		t.Fatal(err)
	}
	if rt.agent == nil {
		t.Fatal("no assistant was built")
	}
	if rt.agent.Principal != rt.PrincipalID {
		t.Fatal("the assistant was given a principal of its own")
	}
	if rt.agent.Device.ID == rt.Device.ID {
		t.Fatal("the assistant shares your device, which forks the chain")
	}
	if rt.agent.TerminalID == rt.Self.TerminalID {
		t.Fatal("the assistant shares your terminal, which breaks authorship")
	}
	if rt.agent.Manifest.AgencyMode != manifest.AgencyAIAgent || !rt.agent.Manifest.AIPresent {
		t.Fatalf("the manifest does not declare what it is: %+v", rt.agent.Manifest)
	}
	if rt.agent.Manifest.Autonomy != manifest.AutonomyAssistive {
		t.Fatal("autonomy claims more than the code can back")
	}
}

// Two participants writing into one space with two devices must not fork
// the log — before OR after a restart. Forgetting to resume the agent's
// chain quarantines it permanently, which is why this runs both halves.
func TestTheTwoWritersNeverForkTheChain(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	tid, err := rt.EnsureAISpace()
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)
	for i := range 10 {
		if _, err := rt.Say(tid, "q"+itoa(i), SayOptions{}); err != nil {
			t.Fatalf("human emit %d: %v", i, err)
		}
		if _, err := agent.Say(rt.agent, sp, "a"+itoa(i), "test/model", now()); err != nil {
			t.Fatalf("agent emit %d: %v", i, err)
		}
	}
	rt.Close()

	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	sp2, _ := rt2.spaceForTest(tid)
	if _, err := rt2.Say(tid, "after restart", SayOptions{}); err != nil {
		t.Fatalf("human emit after restart: %v", err)
	}
	if _, err := agent.Say(rt2.agent, sp2, "answer after restart", "test/model", now()); err != nil {
		t.Fatalf("agent emit after restart forked the chain: %v", err)
	}
}

// An answer is machine-authored, names its model, and is NOT "mine" — even
// though this principal controls the signer.
func TestAnAnswerIsNotYours(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.EnsureAISpace()
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)
	if _, err := rt.Say(tid, "what is this", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Say(rt.agent, sp, "an answer", "anthropic/model-x", now()); err != nil {
		t.Fatal(err)
	}
	var sawAnswer bool
	for _, e := range sp.State.Entries() {
		if e.ProducedBy == signal.AuthorshipHuman {
			continue
		}
		sawAnswer = true
		if e.Author != rt.PrincipalID {
			t.Fatal("the controller claim is not the person's after all")
		}
		if e.Content.Text == nil || e.Content.Text.Model != "anthropic/model-x" {
			t.Fatalf("the model claim did not survive: %+v", e.Content.Text)
		}
	}
	if !sawAnswer {
		t.Fatal("the answer is not in the log")
	}
}

// ---- LocalOnly, the network invariant ----

func TestLocalAgentNeverAnnounces(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	ai, err := rt.EnsureAISpace()
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := rt.CreateSpace("an ordinary room")
	if err != nil {
		t.Fatal(err)
	}
	// announcedSpaces is the function the announcer AND the inbound matcher
	// both call — not a copy of the rule, the rule itself.
	visible := map[id.TerminalID]bool{}
	for _, tid := range rt.announcedSpaces() {
		visible[tid] = true
	}
	if len(visible) == 0 {
		t.Fatal("nothing was announced at all — the test would prove nothing")
	}
	if visible[ai] {
		t.Fatal("the assistant's space was announced on the LAN")
	}
	if !visible[ordinary] {
		t.Fatal("an ordinary space stopped being announced")
	}
}

func TestLocalAgentNeverPublishesToRelay(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	srv, addr := setUpRelay(t, rt)
	defer srv.Close()

	ai, err := rt.EnsureAISpace()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Say(ai, "something private", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	ordinary, err := rt.CreateSpace("an ordinary room")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Say(ordinary, "something ordinary", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	// The assertion that carries this test: relayMailboxSpaces is the one
	// function both the pull path and the sync loop derive addresses from,
	// and the leak it guards is not "the relay was handed something" but
	// "the relay was ASKED about an address" — a mailbox poll tells a relay
	// the hint exists even when the answer is empty.
	polled := map[id.TerminalID]bool{}
	for _, tid := range rt.relayMailboxSpaces() {
		polled[tid] = true
	}
	if polled[ai] {
		t.Fatal("the relay would be asked about the assistant's space")
	}
	if !polled[ordinary] {
		t.Fatal("an ordinary space stopped being synced — the test would prove nothing")
	}

	// And the weaker but end-to-end half: after several sync cycles the
	// relay is holding nothing at any address derivable from that space.
	// On its own this would pass vacuously — a space with one member has no
	// recipients to push to — which is exactly why it is the SECOND check
	// here and not the only one.
	time.Sleep(9 * time.Second)

	client, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	bucket := relay.Bucket(now())
	var hints [][]byte
	for b := bucket - 1; b <= bucket+1; b++ {
		hints = append(hints,
			relay.HintFor(ai, rt.Device.ID, b),
			relay.HintPublicOutbox(ai, b))
	}
	items, err := client.Fetch(hints)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("the relay is holding %d items for the assistant's space", len(items))
	}
}

func TestLocalAgentCannotMintInvite(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	srv, addr := setUpRelay(t, rt)
	defer srv.Close()
	ai, err := rt.EnsureAISpace()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.MintPass(ai, 1, 1, addr); err == nil {
		t.Fatal("a pass into the assistant's space was minted")
	}
	if _, err := rt.InviteToSpace(ai, QuickLinkOptions{}); err == nil {
		t.Fatal("a quick link into the assistant's space was minted")
	}
	vis := "public"
	if err := rt.RevisePolicy(ai, PolicyDelta{Visibility: &vis}); err == nil {
		t.Fatal("the assistant's space was made public")
	}
}

// The one that stops LocalOnly from quietly meaning "broken": the space
// must remain fully usable where it is.
func TestLocalAgentCanReceiveLocalShare(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	ai, err := rt.EnsureAISpace()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Say(ai, "a question from elsewhere", SayOptions{}); err != nil {
		t.Fatalf("the assistant's space refuses ordinary writes: %v", err)
	}
	sp, _ := rt.spaceForTest(ai)
	if _, err := agent.Say(rt.agent, sp, "and an answer", "test/model", now()); err != nil {
		t.Fatalf("the assistant cannot answer in its own space: %v", err)
	}
	if n := len(sp.State.Entries()); n != 2 {
		t.Fatalf("the exchange is not there: %d entries", n)
	}
}

// ---- asking ----

// No provider is not a reason to leave a question in the log forever.
func TestAskWithNoProviderWritesNothing(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	before := len(rt.Spaces())
	if _, _, err := rt.Ask("are you there"); err == nil {
		t.Fatal("a question was accepted with nowhere to send it")
	} else if !strings.Contains(err.Error(), "provider") {
		t.Fatalf("the refusal does not say what to do: %v", err)
	}
	if len(rt.Spaces()) != before {
		t.Fatal("a refused question still created a space")
	}
}

// The window is bounded and reads oldest-first, so the model sees a
// conversation rather than a reversed one.
func TestThePromptWindowIsBoundedAndInOrder(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.EnsureAISpace()
	if err != nil {
		t.Fatal(err)
	}
	for i := range aiWindowEntries + 12 {
		if _, err := rt.Say(tid, "line "+itoa(i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	sys, user := rt.aiPrompt(tid)
	if !strings.Contains(sys, "nothing else") {
		t.Fatalf("the system prompt does not bound what it knows: %q", sys)
	}
	lines := strings.Split(user, "\n")
	if len(lines) > aiWindowEntries {
		t.Fatalf("the window is unbounded: %d lines", len(lines))
	}
	if !strings.Contains(lines[len(lines)-1], "line "+itoa(aiWindowEntries+11)) {
		t.Fatalf("the newest line is not last: %q", lines[len(lines)-1])
	}
	if strings.Contains(user, "line 0:") {
		t.Fatal("the oldest lines were not dropped")
	}
}

// ---- helpers ----

func now() uint64 { return uint64(time.Now().Unix()) }

// A local-only space has no delivery to track: nothing carries its events
// anywhere. Found live — a copy shared into the assistant's space reported
// "carrying it over lan", a route the node will never take (SHARE-2).
func TestALocalOnlySpaceHasNoDeliveryToTrack(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	ai, err := rt.EnsureAISpace()
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := rt.CreateSpace("an ordinary room")
	if err != nil {
		t.Fatal(err)
	}
	local, err := rt.Say(ai, "a question", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := rt.Say(ordinary, "something that could travel", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rt.Delivery(local); ok {
		t.Fatal("the assistant's space claims a delivery it will never make")
	}
	// And the ordinary one still is, so this is not vacuous.
	if _, ok := rt.Delivery(out); !ok {
		t.Fatal("an ordinary space stopped being tracked")
	}
}
