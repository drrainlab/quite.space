package node

// UI-1: /api/spaces carries the inbox line — the newest moment, who said
// it, when — so a list can read like a messenger's without a second
// request per room.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSpacesCarryTheInboxLine(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("inbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Say(tid, "the last word here", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	api, err := NewAPIServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/spaces", nil)
	req.Header.Set("X-QP-Token", api.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var rooms []struct {
		ID   string `json:"id"`
		Last *struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
			Mine bool   `json:"mine"`
			At   uint64 `json:"at"`
		} `json:"last"`
	}
	if err := json.Unmarshal(body, &rooms); err != nil {
		t.Fatal(err)
	}
	for _, r := range rooms {
		if r.ID != tid.Hex() {
			continue
		}
		if r.Last == nil {
			t.Fatal("the room has a moment and no inbox line")
		}
		if r.Last.Kind != "text" || r.Last.Text != "the last word here" || !r.Last.Mine || r.Last.At == 0 {
			t.Fatalf("inbox line wrong: %+v", *r.Last)
		}
		return
	}
	t.Fatal("the room is missing from /api/spaces")
}
