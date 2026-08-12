// TR-0d — the text-only profile's promises, pinned against a fake JMAP
// server that records every method the client dares to call.
package email

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/drrainlab/quiet_places/node"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// fakeJMAP serves a session and a fixed mailbox, and remembers every JMAP
// method name and every URL path it was asked for.
type fakeJMAP struct {
	mu           sync.Mutex
	methods      []string
	paths        []string
	emails       []map[string]any
	submissionOK bool
	// submitted records every Email/set creation that a submission then
	// referenced — the outbound assertions read it.
	submitted []map[string]any
	srv       *httptest.Server
}

func newFakeJMAP(t *testing.T, emails []map[string]any) *fakeJMAP {
	f := &fakeJMAP{emails: emails}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jmap", func(w http.ResponseWriter, r *http.Request) {
		f.note("", r.URL.Path)
		if !f.requireAuth(w, r) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"apiUrl":          f.srv.URL + "/api",
			"primaryAccounts": map[string]string{"urn:ietf:params:jmap:mail": "acc1"},
		})
	})
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		if !f.requireAuth(w, r) {
			return
		}
		var req struct {
			Using       []string             `json:"using"`
			MethodCalls [][3]json.RawMessage `json:"methodCalls"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// A real server refuses a method whose capability was not declared.
		declared := map[string]bool{}
		for _, u := range req.Using {
			declared[u] = true
		}
		var responses []any
		for _, call := range req.MethodCalls {
			var name string
			_ = json.Unmarshal(call[0], &name)
			f.note(name, r.URL.Path)
			switch name {
			case "Mailbox/query":
				var args struct {
					Filter struct {
						Role string `json:"role"`
					} `json:"filter"`
				}
				_ = json.Unmarshal(call[1], &args)
				box := "inbox1"
				if args.Filter.Role == "drafts" {
					box = "drafts1"
				}
				responses = append(responses, []any{"Mailbox/query", map[string]any{
					"ids": []string{box},
				}, "m"})
			case "Email/query":
				// A real server honours inMailbox. Messages the fake marks
				// as living elsewhere (Sent) must not come back — that is
				// the echo the live deployment produced.
				var args struct {
					Filter struct {
						InMailbox string `json:"inMailbox"`
					} `json:"filter"`
				}
				_ = json.Unmarshal(call[1], &args)
				ids := []string{}
				for _, e := range f.emails {
					if box, ok := e["_mailbox"].(string); ok && args.Filter.InMailbox != "" &&
						box != args.Filter.InMailbox {
						continue
					}
					ids = append(ids, e["id"].(string))
				}
				responses = append(responses, []any{"Email/query", map[string]any{"ids": ids}, "q"})
			case "Email/get":
				var got []map[string]any
				for _, e := range f.emails {
					if box, ok := e["_mailbox"].(string); ok && box != "inbox1" {
						continue
					}
					got = append(got, e)
				}
				responses = append(responses, []any{"Email/get", map[string]any{"list": got}, "g"})
			case "Identity/get":
				responses = append(responses, []any{"Identity/get", map[string]any{
					"list": []map[string]any{{"id": "ident1", "email": "hello@quite.space"}},
				}, "i"})
			case "Email/set":
				var args struct {
					Create map[string]map[string]any `json:"create"`
				}
				_ = json.Unmarshal(call[1], &args)
				f.mu.Lock()
				for _, created := range args.Create {
					f.submitted = append(f.submitted, created)
				}
				f.mu.Unlock()
				responses = append(responses, []any{"Email/set", map[string]any{
					"created": map[string]any{"e1": map[string]any{"id": "sent-1"}},
				}, "e"})
			case "EmailSubmission/set":
				if !declared["urn:ietf:params:jmap:submission"] {
					responses = append(responses, []any{"error", map[string]any{
						"type": "unknownMethod", "description": "capability not in using",
					}, "s"})
					break
				}
				responses = append(responses, []any{"EmailSubmission/set", map[string]any{
					"created": map[string]any{"s1": map[string]any{"id": "sub-1"}},
				}, "s"})
			default:
				responses = append(responses, []any{"error", map[string]any{"type": "unknownMethod"}, "x"})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"methodResponses": responses})
	})
	// Any OTHER path — blob downloads above all — is a profile violation.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.note("", r.URL.Path)
		http.Error(w, "the text-only profile has no business here", 403)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// requireAuth mirrors a real mail server: no credential, no answer. Before
// this, the suite proved the protocol and left authentication untested —
// which is how a Bearer-instead-of-Basic bug reached a live server.
func (f *fakeJMAP) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	user, pass, ok := r.BasicAuth()
	if !ok || user != "hello@quite.space" || pass != "t" {
		w.Header().Set("WWW-Authenticate", `Basic realm="jmap"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func (f *fakeJMAP) note(method, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if method != "" {
		f.methods = append(f.methods, method)
	}
	f.paths = append(f.paths, path)
}

func (f *fakeJMAP) config() Config {
	return Config{URL: f.srv.URL + "/.well-known/jmap", Account: "hello@quite.space", Token: "t"}
}

func collect(t *testing.T, f *fakeJMAP) []node.ExternalEnvelope {
	t.Helper()
	c := &client{cfg: f.config(), http: f.srv.Client()}
	var got []node.ExternalEnvelope
	if err := c.PollOnce(context.Background(), func(env node.ExternalEnvelope) error {
		got = append(got, env)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

// A message with attachments: the text arrives, the attachments are named
// and NEVER fetched — the server would have refused the download anyway,
// and the method log proves it was never asked.
func TestTextOnlyProfileNeverFetchesAttachmentParts(t *testing.T) {
	f := newFakeJMAP(t, []map[string]any{{
		"id": "jmap-1", "messageId": []string{"<m1@x>"},
		"from":    []map[string]any{{"email": "alice@example.org"}},
		"subject": "Документ", "receivedAt": "2026-08-12T10:00:00Z",
		"hasAttachment": true,
		"textBody":      []map[string]any{{"partId": "p1", "type": "text/plain"}},
		"attachments": []map[string]any{
			{"partId": "a1", "type": "application/pdf"},
			{"partId": "a2", "type": "image/png"},
		},
		"bodyValues": map[string]any{"p1": map[string]any{"value": "Привет, посмотри документ."}},
	}})
	got := collect(t, f)
	if len(got) != 1 {
		t.Fatalf("expected one envelope, got %d", len(got))
	}
	env := got[0]
	if !strings.Contains(env.Text, "Привет, посмотри документ.") {
		t.Fatalf("text lost: %q", env.Text)
	}
	if !strings.Contains(env.Text, "2 attachments omitted by Mailbox policy") {
		t.Fatalf("omission not named: %q", env.Text)
	}
	if !hasFlag(env.LossFlags, "attachments_omitted") {
		t.Fatalf("loss flag missing: %v", env.LossFlags)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Mailbox/query is how the connector finds the INBOX; everything else
	// must stay within reading messages. Any blob or download call fails
	// the test below on the path list.
	for _, m := range f.methods {
		if m != "Email/query" && m != "Email/get" && m != "Mailbox/query" {
			t.Fatalf("the profile called %q", m)
		}
	}
	for _, p := range f.paths {
		if strings.Contains(p, "blob") || strings.Contains(p, "download") {
			t.Fatalf("the profile touched %q", p)
		}
	}
}

// HTML-only mail: extracted to bare text, nothing executable survives, and
// the extraction is named in the loss flags.
func TestHTMLBecomesBareTextWithNamedLoss(t *testing.T) {
	f := newFakeJMAP(t, []map[string]any{{
		"id": "jmap-2", "messageId": []string{"<m2@x>"},
		"from":    []map[string]any{{"email": "bob@example.org"}},
		"subject": "Новости", "receivedAt": "2026-08-12T10:01:00Z",
		"htmlBody":   []map[string]any{{"partId": "h1", "type": "text/html"}},
		"bodyValues": map[string]any{"h1": map[string]any{"value": `<html><head><style>x{}</style><script>evil()</script></head><body><p>Привет!</p><img src="https://tracker.example/p.gif"><p>Вот &amp; новости.</p></body></html>`}},
	}})
	got := collect(t, f)
	if len(got) != 1 {
		t.Fatalf("expected one envelope, got %d", len(got))
	}
	env := got[0]
	for _, forbidden := range []string{"<", ">", "script", "evil", "tracker.example", "style"} {
		if strings.Contains(env.Text, forbidden) {
			t.Fatalf("markup leaked %q: %q", forbidden, env.Text)
		}
	}
	if !strings.Contains(env.Text, "Привет!") || !strings.Contains(env.Text, "Вот & новости.") {
		t.Fatalf("the words themselves were lost: %q", env.Text)
	}
	if !hasFlag(env.LossFlags, "html_extracted") {
		t.Fatalf("extraction not named: %v", env.LossFlags)
	}
}

// The bound is enforced during decoding, whatever the server's manners: a
// body value far past maxBodyValueBytes arrives truncated locally, flagged.
func TestIngressLimitsAreEnforcedWhileDecoding(t *testing.T) {
	huge := strings.Repeat("а", schemas.MaxTextLen*2) // ignores our maxBodyValueBytes
	f := newFakeJMAP(t, []map[string]any{{
		"id": "jmap-3", "messageId": []string{"<m3@x>"},
		"from":       []map[string]any{{"email": "c@example.org"}},
		"receivedAt": "2026-08-12T10:02:00Z",
		"textBody":   []map[string]any{{"partId": "p1", "type": "text/plain"}},
		"bodyValues": map[string]any{"p1": map[string]any{"value": huge}},
	}})
	got := collect(t, f)
	if len(got) != 1 {
		t.Fatalf("expected one envelope, got %d", len(got))
	}
	env := got[0]
	if len(env.Text) > schemas.MaxTextLen {
		t.Fatalf("a rude server blew the budget: %d bytes", len(env.Text))
	}
	if !hasFlag(env.LossFlags, "text_truncated") {
		t.Fatalf("truncation not named: %v", env.LossFlags)
	}
}

// No acceptable text at all: nothing is projected, and that is a policy
// outcome, not an accident. (The journal-side RefusedNoText lives with the
// node; here the gate simply declines.)
func TestNoTextMeansNoEnvelope(t *testing.T) {
	f := newFakeJMAP(t, []map[string]any{{
		"id": "jmap-4", "receivedAt": "2026-08-12T10:03:00Z",
		"from": []map[string]any{{"email": "d@example.org"}},
	}})
	if got := collect(t, f); len(got) != 0 {
		t.Fatalf("an empty message produced an envelope: %+v", got)
	}
}

// Threading survives the gate: Message-ID and In-Reply-To ride as
// provenance refs; the JMAP id — not the Message-ID — is the identity.
func TestThreadingRefsSurviveTheGate(t *testing.T) {
	f := newFakeJMAP(t, []map[string]any{{
		"id": "jmap-5", "messageId": []string{"<m5@x>"}, "inReplyTo": []string{"<m1@x>"},
		"from":       []map[string]any{{"email": "alice@example.org"}},
		"receivedAt": "2026-08-12T10:04:00Z",
		"textBody":   []map[string]any{{"partId": "p1", "type": "text/plain"}},
		"bodyValues": map[string]any{"p1": map[string]any{"value": "ответ"}},
	}})
	got := collect(t, f)
	if len(got) != 1 {
		t.Fatal("expected one envelope")
	}
	env := got[0]
	if env.ExternalID != "jmap-5" || env.ExternalRef != "<m5@x>" || env.ThreadRef != "<m1@x>" {
		t.Fatalf("refs mangled: %+v", env)
	}
}

func hasFlag(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}
