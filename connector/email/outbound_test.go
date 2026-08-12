// TR-0e — the reply direction, pinned: a reply to an imported message
// becomes exactly one RFC email with honest thread headers, egress
// authority belongs to the ACTIVE binding alone, and nothing that is not a
// reply to an import can leave at all.
package email

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/node"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// submission support for the fake box: Identity/get, Mailbox/query,
// Email/set, EmailSubmission/set — recorded like everything else.
func (f *fakeJMAP) enableSubmission() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submissionOK = true
}

func TestReplyBecomesOneRFCEmailWithThreadHeaders(t *testing.T) {
	f := newFakeJMAP(t, []map[string]any{{
		"id": "jmap-200", "messageId": []string{"<orig-1@example.org>"},
		"from":       []map[string]any{{"email": "alice@example.org"}},
		"subject":    "Can I try the beta?",
		"receivedAt": "2026-08-12T12:00:00Z",
		"textBody":   []map[string]any{{"partId": "p1", "type": "text/plain"}},
		"bodyValues": map[string]any{"p1": map[string]any{"value": "Hey! Saw the announcement."}},
	}})
	f.enableSubmission()

	rt, err := node.Open(t.TempDir(), []byte("e2e-pass-1234"), "term")
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	tid, err := rt.CreateSpace("✉ mailbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.ConnectorRoute("public-mail", tid); err != nil {
		t.Fatal(err)
	}
	c := &client{cfg: f.config(), http: f.srv.Client()}
	sink := func(env node.ExternalEnvelope) error {
		return rt.ConnectorIngest("public-mail", env)
	}
	if err := c.PollOnce(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	// Wait for the projection, find its EventID, reply to it — exactly what
	// a person does in the UI.
	var importedID string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range rt.SpaceMessages(tid) {
			if strings.Contains(m.Text, "Saw the announcement") {
				importedID = m.ID.Hex()
			}
		}
		if importedID != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if importedID == "" {
		t.Fatal("the import never landed")
	}
	parent := mustEventID(t, importedID)
	if _, err := rt.Say(tid, "Sure! Link coming today.", node.SayOptions{ReplyTo: &parent}); err != nil {
		t.Fatal(err)
	}

	// One outbound tick, by hand: outbox → submit → settle.
	outs, err := rt.ConnectorOutbox("public-mail")
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 1 {
		t.Fatalf("outbox = %d candidates", len(outs))
	}
	out := outs[0]
	if out.To != "alice@example.org" || out.InReplyTo != "<orig-1@example.org>" ||
		out.Subject != "Re: Can I try the beta?" {
		t.Fatalf("reply envelope dishonest: %+v", out)
	}
	if err := c.SubmitReply(context.Background(), out); err != nil {
		t.Fatal(err)
	}
	if err := rt.ConnectorOutboundResult("public-mail", out.EventID, true, ""); err != nil {
		t.Fatal(err)
	}

	// The fake saw exactly one submission, with the right message.
	f.mu.Lock()
	subs := append([]map[string]any(nil), f.submitted...)
	f.mu.Unlock()
	if len(subs) != 1 {
		t.Fatalf("submissions = %d", len(subs))
	}
	sent := subs[0]
	if !strings.Contains(mustString(sent, "subject"), "Re: Can I try the beta?") {
		t.Fatalf("subject mangled: %v", sent["subject"])
	}

	// A second scan finds nothing: the claim settled, the authority spent.
	outs, _ = rt.ConnectorOutbox("public-mail")
	if len(outs) != 0 {
		t.Fatalf("a settled reply re-entered the outbox: %d", len(outs))
	}
	// And a plain message (no ReplyTo) never becomes a candidate.
	if _, err := rt.Say(tid, "заметка себе", node.SayOptions{}); err != nil {
		t.Fatal(err)
	}
	outs, _ = rt.ConnectorOutbox("public-mail")
	if len(outs) != 0 {
		t.Fatalf("a non-reply escaped: %d", len(outs))
	}
}

// THE DEATH TEST (plan rev 3): a closed binding cannot egress. Reply to an
// old import after the route moved — no email, ever; the new binding's own
// thread works.
func TestClosedBindingCannotEgressReply(t *testing.T) {
	rt, err := node.Open(t.TempDir(), []byte("e2e-pass-1234"), "term")
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	spaceA, err := rt.CreateSpace("Mailbox A")
	if err != nil {
		t.Fatal(err)
	}
	spaceB, err := rt.CreateSpace("Mailbox B")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.ConnectorRoute("public-mail", spaceA); err != nil {
		t.Fatal(err)
	}
	ingest := func(extID, from, text string) {
		if err := rt.ConnectorIngest("public-mail", node.ExternalEnvelope{
			ExternalID: extID, Kind: "email", Address: from,
			ExternalRef: "<" + extID + "@x>", Subject: "s",
			Text: text, ObservedAt: time.Now().Unix(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	ingest("mail-1", "alice@example.org", "первое письмо")
	mail1 := waitImportedID(t, rt, spaceA, "первое письмо")

	// The room moves: binding #2 → B.
	if _, err := rt.ConnectorRoute("public-mail", spaceB); err != nil {
		t.Fatal(err)
	}

	// A reply to mail-1 in the OLD space: the journal knows the EventID,
	// and that is precisely not enough — its binding is closed.
	if _, err := rt.Say(spaceA, "поздний ответ", node.SayOptions{ReplyTo: &mail1}); err != nil {
		t.Fatal(err)
	}
	outs, err := rt.ConnectorOutbox("public-mail")
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 0 {
		t.Fatalf("a closed binding egressed: %+v", outs)
	}

	// The new binding's own conversation flows.
	ingest("mail-2", "bob@example.org", "второе письмо")
	mail2 := waitImportedID(t, rt, spaceB, "второе письмо")
	if _, err := rt.Say(spaceB, "свежий ответ", node.SayOptions{ReplyTo: &mail2}); err != nil {
		t.Fatal(err)
	}
	outs, _ = rt.ConnectorOutbox("public-mail")
	if len(outs) != 1 || outs[0].To != "bob@example.org" {
		t.Fatalf("the active binding's reply did not egress: %+v", outs)
	}
}

func waitImportedID(t *testing.T, rt *node.Runtime, tid interface{ Hex() string }, text string) (out id.EventID) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range rt.Spaces() {
			if s.ID.Hex() != tid.Hex() {
				continue
			}
			for _, m := range rt.SpaceMessages(s.ID) {
				if strings.Contains(m.Text, text) {
					return m.ID
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("import never landed: %s", text)
	return
}

func mustEventID(t *testing.T, hexStr string) (out id.EventID) {
	t.Helper()
	decoded, err := hex.DecodeString(hexStr)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("bad event id %q", hexStr)
	}
	copy(out[:], decoded)
	return
}

func mustString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
