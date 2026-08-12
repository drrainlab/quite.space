// TR-0d end-to-end, no mail server required: a fake JMAP box feeds the
// real poller, the real journal, the real projector, a real space — and a
// restart plus a re-poll produce nothing twice.
package email

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/node"
)

func TestEmailInboundEndToEnd(t *testing.T) {
	f := newFakeJMAP(t, []map[string]any{{
		"id": "jmap-100", "messageId": []string{"<hello-1@example.org>"},
		"from":          []map[string]any{{"name": "Alice", "email": "alice@example.org"}},
		"subject":       "Can I try the beta?",
		"receivedAt":    "2026-08-12T12:00:00Z",
		"hasAttachment": true,
		"textBody":      []map[string]any{{"partId": "p1", "type": "text/plain"}},
		"attachments":   []map[string]any{{"partId": "a1", "type": "image/png"}},
		"bodyValues":    map[string]any{"p1": map[string]any{"value": "Hey! Saw the announcement."}},
	}})

	dir := t.TempDir()
	rt, err := node.Open(dir, []byte("e2e-pass-1234"), "term")
	if err != nil {
		t.Fatal(err)
	}
	tid, err := rt.CreateSpace("✉ mailbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.ConnectorRoute("public-mail", tid); err != nil {
		t.Fatal(err)
	}
	sink := func(env node.ExternalEnvelope) error {
		return rt.ConnectorIngest("public-mail", env)
	}
	c := &client{cfg: f.config(), http: f.srv.Client()}
	if err := c.PollOnce(context.Background(), sink); err != nil {
		t.Fatal(err)
	}

	imported := func(rt *node.Runtime) int {
		n := 0
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			st, err := rt.ConnectorStatus("public-mail")
			if err == nil && st.Published >= 1 {
				n = st.Published
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		return n
	}
	if n := imported(rt); n != 1 {
		t.Fatalf("published = %d", n)
	}

	// A second poll of the same window: the journal absorbs the overlap.
	if err := c.PollOnce(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	st, _ := rt.ConnectorStatus("public-mail")
	if st.Published != 1 || st.Pending != 0 {
		t.Fatalf("re-poll duplicated: %+v", st)
	}

	// Restart, then poll the same window AGAIN: still exactly one.
	rt.Close()
	rt, err = node.Open(dir, []byte("e2e-pass-1234"), "term")
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	sink = func(env node.ExternalEnvelope) error {
		return rt.ConnectorIngest("public-mail", env)
	}
	c2 := &client{cfg: f.config(), http: f.srv.Client()}
	if err := c2.PollOnce(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	st, _ = rt.ConnectorStatus("public-mail")
	if st.Published != 1 || st.Pending != 0 {
		t.Fatalf("restart+re-poll duplicated: %+v", st)
	}

	// And what landed says everything it should: subject + body + the
	// omission note, from the address the gateway observed.
	found := false
	for _, txt := range spaceTexts(t, rt, tid.Hex()) {
		if strings.Contains(txt, "Can I try the beta?") &&
			strings.Contains(txt, "Hey! Saw the announcement.") &&
			strings.Contains(txt, "1 attachment omitted by Mailbox policy") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the projected message is not honest: %v", spaceTexts(t, rt, tid.Hex()))
	}
}

// spaceTexts reads the space's visible texts through the same projection
// the API serves — no internal reach into package node.
func spaceTexts(t *testing.T, rt *node.Runtime, tidHex string) []string {
	t.Helper()
	var out []string
	for _, s := range rt.Spaces() {
		if s.ID.Hex() != tidHex {
			continue
		}
		msgs := rt.SpaceMessages(s.ID)
		for _, m := range msgs {
			out = append(out, m.Text)
		}
	}
	return out
}
