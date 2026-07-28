package node

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// AT-0A.1: mentions and reply edges survive the whole path — Say → signed
// envelope → reducer → entry projection — and the viewer-relative
// mentions_me flag is resolved on the node, not guessed by the client.
func TestMentionsAndReplyReachTheProjection(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Workshop")
	if err != nil {
		t.Fatal(err)
	}

	first, err := rt.Say(tid, "конфиг реле лежит в репозитории", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	me := rt.Principal.ID
	second, err := rt.Say(tid, "посмотришь до вечера?", SayOptions{
		ReplyTo: &first, Mentions: []id.PrincipalID{me},
	})
	if err != nil {
		t.Fatal(err)
	}

	sp, _ := rt.spaceForTest(tid)
	api, err := NewAPIServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[id.PrincipalID]string{me: rt.DisplayName()}

	var got entryResp
	for _, e := range sp.State.Entries() {
		if e.ID == second {
			got = api.projectEntry(tid, sp, &e, me, names)
		}
	}
	if got.ID == "" {
		t.Fatal("reply entry not found in the projection")
	}
	if got.ReplyTo != first.Hex() {
		t.Fatalf("reply_to lost: %q", got.ReplyTo)
	}
	if len(got.Mentions) != 1 || got.Mentions[0] != me.Hex() {
		t.Fatalf("mentions lost: %+v", got.Mentions)
	}
	if !got.MentionsMe {
		t.Fatal("mentions_me not resolved for the addressed viewer")
	}
	if got.MentionNames[0] != rt.DisplayName() {
		t.Fatalf("mention name unresolved: %q", got.MentionNames[0])
	}
	if got.CreatedAt == 0 {
		t.Fatal("created_at not serialized")
	}
}

// The revision schema REUSES the wire field that carries reply_to. A revised
// message must therefore never surface a phantom reply edge.
func TestRevisionDoesNotForgeAReplyEdge(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Workshop")
	if err != nil {
		t.Fatal(err)
	}
	eid, err := rt.Say(tid, "первая версия", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Emit a revision the way the protocol does it: message.revised.v1
	// reuses the reply_to wire field as its revision TARGET.
	sp0, _ := rt.spaceForTest(tid)
	rev, err := (&schemas.TextMessage{Text: "вторая версия", ReplyTo: &eid}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Self.Emit(sp0, schemas.MessageRevised, rev,
		signal.AuthorshipHuman, 1_700_000_100); err != nil {
		t.Fatal(err)
	}

	sp, _ := rt.spaceForTest(tid)
	me := rt.Principal.ID
	api, err := NewAPIServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range sp.State.Entries() {
		got := api.projectEntry(tid, sp, &e, me, nil)
		if got.ReplyTo != "" {
			t.Fatalf("revision leaked a reply edge: %q on %s", got.ReplyTo, got.ID)
		}
	}
}

// A viewer who is NOT addressed must not see mentions_me.
func TestMentionsMeIsViewerRelative(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Workshop")
	if err != nil {
		t.Fatal(err)
	}
	var someoneElse id.PrincipalID
	someoneElse[0] = 0xEE
	eid, err := rt.Say(tid, "адресовано другому", SayOptions{
		Mentions: []id.PrincipalID{someoneElse},
	})
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)
	api, err := NewAPIServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range sp.State.Entries() {
		if e.ID != eid {
			continue
		}
		got := api.projectEntry(tid, sp, &e, rt.Principal.ID, nil)
		if got.MentionsMe {
			t.Fatal("mentions_me set for a viewer who was not addressed")
		}
		if len(got.Mentions) != 1 {
			t.Fatalf("mention list mangled: %+v", got.Mentions)
		}
	}
}
