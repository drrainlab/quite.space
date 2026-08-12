// The session's apiUrl names a PATH, never a destination (found on the
// first live deployment, TR-0d). A mail server publishes the address its
// outside clients should use; a connector that followed it would leave the
// machine it was pointed at — and an authenticated poller following a
// server-chosen host is the SSRF shape, Bearer token included.
package email

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/node"
)

// The fake box advertises a public apiUrl on a host that does not exist.
// The poller must ignore that host, keep the origin it dialled, and still
// find the mail.
func TestPollerKeepsTheOriginItDialled(t *testing.T) {
	var apiHits int
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/.well-known/jmap", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			// A public name that resolves nowhere useful — exactly what
			// Stalwart returns once a hostname is configured.
			"apiUrl":          "https://mx1.quite.space/jmap/",
			"primaryAccounts": map[string]string{"urn:ietf:params:jmap:mail": "acc1"},
		})
	})
	mux.HandleFunc("/jmap/", func(w http.ResponseWriter, r *http.Request) {
		apiHits++
		var req struct {
			MethodCalls [][3]json.RawMessage `json:"methodCalls"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var responses []any
		for _, call := range req.MethodCalls {
			var name string
			_ = json.Unmarshal(call[0], &name)
			switch name {
			case "Mailbox/query":
				responses = append(responses, []any{"Mailbox/query", map[string]any{"ids": []string{"inbox1"}}, "m"})
			case "Email/query":
				responses = append(responses, []any{"Email/query", map[string]any{"ids": []string{"m1"}}, "q"})
			case "Email/get":
				responses = append(responses, []any{"Email/get", map[string]any{"list": []map[string]any{{
					"id": "m1", "messageId": []string{"<x@y>"},
					"from":       []map[string]any{{"email": "a@b.c"}},
					"receivedAt": "2026-08-12T10:00:00Z",
					"textBody":   []map[string]any{{"partId": "p1", "type": "text/plain"}},
					"bodyValues": map[string]any{"p1": map[string]any{"value": "письмо на месте"}},
				}}}, "g"})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"methodResponses": responses})
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	c := &client{
		cfg:  Config{URL: srv.URL + "/.well-known/jmap", Account: "hello@quite.space", Token: "t"},
		http: srv.Client(),
	}
	var got []node.ExternalEnvelope
	if err := c.PollOnce(context.Background(), func(env node.ExternalEnvelope) error {
		got = append(got, env)
		return nil
	}); err != nil {
		t.Fatalf("the poller followed the advertised host and failed: %v", err)
	}
	if apiHits == 0 {
		t.Fatal("the API call never reached the host we dialled")
	}
	if len(got) != 1 || !strings.Contains(got[0].Text, "письмо на месте") {
		t.Fatalf("mail lost: %+v", got)
	}
	if !strings.HasPrefix(c.apiURL, srv.URL) {
		t.Fatalf("apiURL left the dialled origin: %q", c.apiURL)
	}
}
