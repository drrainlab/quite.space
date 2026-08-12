// The connector reads the INBOX, never the whole account: an account holds
// Sent, and importing one's own replies back into the space is an echo that
// looks — to the person reading it — exactly like the correspondent writing
// again. Found on the first live reply (TR-0d/e).
package email

import (
	"context"
	"strings"
	"testing"
)

func TestSentMailIsNotImportedBack(t *testing.T) {
	f := newFakeJMAP(t, []map[string]any{
		{
			"id": "in-1", "_mailbox": "inbox1",
			"messageId":  []string{"<in-1@x>"},
			"from":       []map[string]any{{"email": "alice@example.org"}},
			"subject":    "вопрос",
			"receivedAt": "2026-08-12T10:00:00Z",
			"textBody":   []map[string]any{{"partId": "p1", "type": "text/plain"}},
			"bodyValues": map[string]any{"p1": map[string]any{"value": "вопрос от алисы"}},
		},
		{
			// Our own reply, sitting in Sent exactly as a mail server files it.
			"id": "out-1", "_mailbox": "sent1",
			"messageId":  []string{"<out-1@x>"},
			"from":       []map[string]any{{"email": "hello@quite.space"}},
			"subject":    "Re: вопрос",
			"receivedAt": "2026-08-12T10:05:00Z",
			"textBody":   []map[string]any{{"partId": "p2", "type": "text/plain"}},
			"bodyValues": map[string]any{"p2": map[string]any{"value": "наш собственный ответ"}},
		},
	})
	got := collect(t, f)
	if len(got) != 1 {
		t.Fatalf("expected only the inbox message, got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Text, "вопрос от алисы") {
		t.Fatalf("wrong message imported: %q", got[0].Text)
	}
	for _, env := range got {
		if strings.Contains(env.Text, "наш собственный ответ") {
			t.Fatal("the connector imported its own sent reply — the echo is back")
		}
	}
	_ = context.Background
}
