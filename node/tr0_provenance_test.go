// The provenance line is gated on the ENVELOPE, not the payload (ADR-021).
// Any member can write an ExternalOrigin into their own message — the
// structure is just bytes — but only a gateway can sign one as imported.
// A renderer that trusted the payload alone would hand every member a
// stencil for forging somebody's email, so the API refuses to project it
// unless the authorship agrees.
package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func TestForgedExternalOriginIsNotProjected(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("mailbox")
	if err != nil {
		t.Fatal(err)
	}
	// A HUMAN writes a message carrying foreign provenance — the forgery.
	if err := rt.withSpace(tid, func(st *spaceState) error {
		payload, err := (&schemas.TextMessage{
			Text: "перевод денег подтверждён",
			External: &schemas.ExternalOrigin{
				ConnectorKind: "email", Address: "bank@example.com",
				ExternalRef: "<forged@example.com>",
			},
		}).Encode()
		if err != nil {
			return err
		}
		_, err = rt.Self.Emit(st.space, schemas.MessageText, payload,
			signal.AuthorshipHuman, 100)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	api, err := NewAPIServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/spaces/"+tid.Hex()+"/entries", nil)
	req.Header.Set("X-QP-Token", api.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// The FEED endpoint — the one the client actually renders. The first
	// attempt at this line only reached /messages, and the app kept showing
	// letters with no sender because it reads entries.
	var rows []struct {
		Text       string `json:"fallback"`
		ProducedBy string `json:"produced_by"`
		External   *struct {
			Address string `json:"address"`
		} `json:"external"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one message, got %d", len(rows))
	}
	if rows[0].ProducedBy != "human" {
		t.Fatalf("authorship changed: %q", rows[0].ProducedBy)
	}
	if rows[0].External != nil {
		t.Fatalf("a human's message projected foreign provenance: %+v", rows[0].External)
	}
}

// The positive half: a genuinely imported entry carries its sender to the
// feed, or the client has nothing to show.
func TestImportedEntryCarriesItsSender(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "term")
	defer rt.Close()
	tid, err := rt.CreateSpace("mailbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.ConnectorRoute("fix", tid); err != nil {
		t.Fatal(err)
	}
	if err := rt.ConnectorIngest("fix", ExternalEnvelope{
		ExternalID: "m-1", Kind: "email", Address: "alice@example.org",
		Subject: "тема", Text: "тема\n\nтело письма",
		LossFlags: []string{"attachments_omitted"},
	}); err != nil {
		t.Fatal(err)
	}
	waitProjected(t, rt, tid, "тема\n\nтело письма")

	api, err := NewAPIServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/spaces/"+tid.Hex()+"/entries", nil)
	req.Header.Set("X-QP-Token", api.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var rows []struct {
		ProducedBy string `json:"produced_by"`
		External   *struct {
			Address   string   `json:"address"`
			Connector string   `json:"connector"`
			LossFlags []string `json:"loss_flags"`
		} `json:"external"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.ProducedBy != "imported" {
			continue
		}
		found = true
		if r.External == nil || r.External.Address != "alice@example.org" {
			t.Fatalf("the feed lost the sender: %+v", r.External)
		}
		if r.External.Connector != "email" || len(r.External.LossFlags) != 1 {
			t.Fatalf("provenance thinned out: %+v", r.External)
		}
	}
	if !found {
		t.Fatal("no imported entry in the feed at all")
	}
}
