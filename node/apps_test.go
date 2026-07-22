package node

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/appdef"
)

// APP-0 core: instance creation is owner-gated; grants = requested ∩ policy;
// actions are the only emit path and inject instance_id; two instances of one
// definition keep isolated state partitions; inputs refuse out-of-scope reads.
func TestAppInstanceEnforcement(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Studio")
	if err != nil {
		t.Fatal(err)
	}

	// Two poll instances from ONE built-in definition.
	p1, err := rt.CreateAppInstance(tid, "poll", "", "", map[string]string{"question": "Which mix?"})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := rt.CreateAppInstance(tid, "poll", "", "", map[string]string{"question": "Ship it?"})
	if err != nil {
		t.Fatal(err)
	}

	// Vote in p1 only (through the action path — the only emit path).
	if _, err := rt.AppAction(tid, p1.InstanceID, "vote", map[string]any{"option": "A"}); err != nil {
		t.Fatal(err)
	}
	// Partition isolation: p1 has one vote, p2 has none.
	ev1, err := rt.AppInput(tid, p1.InstanceID, "votes")
	if err != nil {
		t.Fatal(err)
	}
	ev2, err := rt.AppInput(tid, p2.InstanceID, "votes")
	if err != nil {
		t.Fatal(err)
	}
	if len(ev1) != 1 || len(ev2) != 0 {
		t.Fatalf("partition isolation broken: p1=%d p2=%d", len(ev1), len(ev2))
	}
	// Undeclared fields are dropped by the template copy.
	if _, err := rt.AppAction(tid, p1.InstanceID, "vote", map[string]any{"option": "B", "sneaky": "x"}); err != nil {
		t.Fatal(err)
	}
	ev1, _ = rt.AppInput(tid, p1.InstanceID, "votes")
	for _, e := range ev1 {
		if d, ok := e["data"].(map[string]any); ok {
			if _, leak := d["sneaky"]; leak {
				t.Fatal("undeclared action field passed through")
			}
		}
	}

	// Unknown action refused.
	if _, err := rt.AppAction(tid, p1.InstanceID, "erase-log", nil); err == nil {
		t.Fatal("unknown action accepted")
	}
	// Unknown input refused.
	if _, err := rt.AppInput(tid, p1.InstanceID, "everything"); err == nil {
		t.Fatal("unknown input accepted")
	}
}

// A custom definition requesting a schema outside the node policy gets NO
// grant for it, and its action is refused at emit even though requested.
func TestAppGrantsIntersectPolicy(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Lab")
	if err != nil {
		t.Fatal(err)
	}
	def := &appdef.Definition{
		AppID: "greedy", Name: "Greedy App",
		Actions: map[string]appdef.Action{
			// Requests to append a schema the node policy does NOT allow.
			"impersonate": {Emit: "message.text.v1"},
			"vote":        {Emit: appdef.SchemaPollVote, Fields: []string{"option"}},
		},
		Requested: []appdef.Capability{
			{Kind: "append", Schemas: []string{"message.text.v1", appdef.SchemaPollVote}},
		},
	}
	rev, err := rt.PublishAppDefinition(tid, def)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := rt.CreateAppInstance(tid, "greedy", rev.Hex(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The grant intersection stripped message.text.v1.
	granted := appdef.NewCapSet(inst.Granted)
	if granted.Has("append", "message.text.v1") {
		t.Fatal("policy-forbidden schema was granted")
	}
	if !granted.Has("append", appdef.SchemaPollVote) {
		t.Fatal("policy-allowed schema was not granted")
	}
	// Emitting the forbidden schema through the action path is refused.
	if _, err := rt.AppAction(tid, inst.InstanceID, "impersonate", map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "effective capabilities") {
		t.Fatalf("policy-forbidden emit allowed: %v", err)
	}
	// The allowed action still works (pinned custom definition resolves).
	if _, err := rt.AppAction(tid, inst.InstanceID, "vote", map[string]any{"option": "A"}); err != nil {
		t.Fatalf("allowed action refused: %v", err)
	}
}

// Non-owner cannot define or place apps.
func TestAppOwnerGate(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	tid, err := alice.CreateSpace("Studio")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(inv); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.CreateAppInstance(tid, "poll", "", "", nil); err == nil {
		t.Fatal("non-owner placed an app")
	}
	if _, err := bob.PublishAppDefinition(tid, builtinApps["poll"]); err == nil {
		t.Fatal("non-owner published a definition")
	}
}

// Revotes append in deterministic (clock, event id) order — the client's
// last-per-author fold therefore converges identically on every node — and
// oversized action data is refused.
func TestAppVoteOrderAndBounds(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, _ := rt.CreateSpace("Studio")
	p, err := rt.CreateAppInstance(tid, "poll", "", "", map[string]string{"question": "q", "options": "A,B"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.AppAction(tid, p.InstanceID, "vote", map[string]any{"option": "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.AppAction(tid, p.InstanceID, "vote", map[string]any{"option": "B"}); err != nil {
		t.Fatal(err)
	}
	evs, err := rt.AppInput(tid, p.InstanceID, "votes")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("expected both vote events in the partition, got %d", len(evs))
	}
	// Deterministic order: the LAST event per author is the revote → "B".
	last := evs[len(evs)-1]["data"].(map[string]any)
	if last["option"] != "B" {
		t.Fatalf("deterministic order broken: last vote %v", last)
	}
	// Oversized action data refused.
	big := strings.Repeat("x", 8000)
	f, err := rt.CreateAppInstance(tid, "form", "", "", map[string]string{"title": "t", "fields": "answer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.AppAction(tid, f.InstanceID, "submit", map[string]any{"fields": map[string]any{"answer": big}}); err == nil {
		t.Fatal("oversized form data accepted")
	}
}
