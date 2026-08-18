package node

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMembersAPIShowsOnePersonOncePerDevices(t *testing.T) {
	now := uint64(time.Now().Unix())
	laptop := openRuntime(t, t.TempDir(), "alice")
	defer laptop.Close()
	tid, err := laptop.CreateSpace("room")
	if err != nil {
		t.Fatal(err)
	}
	phone := pairChild(t, laptop, now)
	// The phone writes into the room so the laptop's log carries the phone's
	// own participant manifest — the second card for the same person.
	srv, addr := startRelay(t)
	defer srv.Close()
	setPersonalRelay(t, laptop, addr)
	setPersonalRelay(t, phone, addr)
	if _, err := phone.Say(tid, "from the phone", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := phone.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	waitUntilMsg(t, laptop, addr, tid, "from the phone")

	api, _ := NewAPIServer(laptop, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/spaces/"+tid.Hex()+"/members", nil)
	req.SetPathValue("id", tid.Hex())
	api.handleMembers(w, req)
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v\n%s", err, w.Body.String())
	}
	humans := 0
	for _, m := range out {
		if m["kind"] == "human" {
			humans++
		}
	}
	if humans != 1 {
		t.Fatalf("one person on two devices shows as %d cards:\n%s", humans, w.Body.String())
	}
}
