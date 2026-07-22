package node

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func openRuntime(t *testing.T, dir, name string) *Runtime {
	t.Helper()
	rt, err := Open(dir, []byte("test passphrase"), name)
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// The critical persistence test: everything — identity, spaces, epochs,
// chain positions — must survive a full restart, and writing must continue
// on the same chain without forking.
func TestRestartContinuesSeamlessly(t *testing.T) {
	dir := t.TempDir()

	rt := openRuntime(t, dir, "alice")
	fp := rt.Principal.Fingerprint()
	tid, err := rt.CreateSpace("Forest Session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Say(tid, "before restart"); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	if rt2.Principal.Fingerprint() != fp {
		t.Fatal("identity changed across restart")
	}
	s, ok := rt2.Space(tid)
	if !ok {
		t.Fatal("space lost across restart")
	}
	msgs := s.State.Messages()
	if len(msgs) != 1 || msgs[0].Text != "before restart" {
		t.Fatalf("messages lost: %+v", msgs)
	}
	// Writing must continue the same device chain (no fork, no reset).
	if _, err := rt2.Say(tid, "after restart"); err != nil {
		t.Fatalf("cannot write after restart: %v", err)
	}
	if len(s.State.Messages()) != 2 {
		t.Fatal("second message not applied")
	}
	if s.Log.Forked(rt2.Device.ID) {
		t.Fatal("restart forked our own chain")
	}
	spaces := rt2.Spaces()
	if len(spaces) != 1 || spaces[0].Title != "Forest Session" || !spaces[0].Owned {
		t.Fatalf("space meta lost: %+v", spaces)
	}
}

// Two runtimes: invite over the API surface, direct LAN connection, full
// convergence including encrypted payloads.
func TestTwoNodesInviteAndSync(t *testing.T) {
	rtA := openRuntime(t, t.TempDir(), "alice")
	defer rtA.Close()
	rtB := openRuntime(t, t.TempDir(), "bob")
	defer rtB.Close()

	tid, err := rtA.CreateSpace("Shared Lab")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rtA.Say(tid, "welcome"); err != nil {
		t.Fatal(err)
	}

	invite, err := rtA.MintInvite(tid, rtB.Device.ID, rtB.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := rtB.JoinInvite(invite)
	if err != nil {
		t.Fatal(err)
	}
	if joined != tid {
		t.Fatal("joined wrong space")
	}

	// Direct connection (no discovery dependence in tests).
	if err := rtA.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := rtB.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := rtB.ConnectPeer(fmt.Sprintf("127.0.0.1:%d", rtA.LAN().Port)); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		sB, _ := rtB.Space(tid)
		if len(sB.State.Messages()) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no convergence: B has %d events", sB.Log.Len())
		}
		time.Sleep(50 * time.Millisecond)
	}

	// B answers; A receives.
	if _, err := rtB.Say(tid, "glad to be here"); err != nil {
		t.Fatal(err)
	}
	for {
		sA, _ := rtA.Space(tid)
		if len(sA.State.Messages()) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reply did not arrive")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestAPI(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	api, err := NewAPIServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()

	call := func(method, path string, body any, out any) int {
		t.Helper()
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		req.Header.Set("X-QP-Token", api.Token())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}

	// No token → 401 (the UI URL carries the token; nothing else gets in).
	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request got %d", resp.StatusCode)
	}

	var st statusResp
	if code := call("GET", "/api/status", nil, &st); code != 200 {
		t.Fatalf("status: %d", code)
	}
	if !strings.Contains(st.Fingerprint, " ") || st.DeviceID == "" {
		t.Fatalf("bad status: %+v", st)
	}

	var created map[string]string
	if code := call("POST", "/api/spaces", map[string]string{"title": "API Space"}, &created); code != 200 {
		t.Fatalf("create: %d", code)
	}
	sid := created["id"]

	if code := call("POST", "/api/spaces/"+sid+"/messages",
		map[string]string{"text": "via api"}, nil); code != 200 {
		t.Fatalf("say: %d", code)
	}
	var msgs []messageResp
	if code := call("GET", "/api/spaces/"+sid+"/messages", nil, &msgs); code != 200 {
		t.Fatalf("messages: %d", code)
	}
	if len(msgs) != 1 || msgs[0].Text != "via api" || msgs[0].ProducedBy != "human" || !msgs[0].Mine {
		t.Fatalf("message projection wrong: %+v", msgs)
	}

	// Card lifecycle through the API.
	var card map[string]string
	if code := call("POST", "/api/spaces/"+sid+"/cards",
		map[string]string{"title": "try the api", "origin": msgs[0].ID}, &card); code != 200 {
		t.Fatalf("card: %d", code)
	}
	if code := call("POST", "/api/spaces/"+sid+"/cards/"+card["id"]+"/status",
		map[string]string{"title": "try the api", "status": "done"}, nil); code != 200 {
		t.Fatal("card status update failed")
	}
	var state stateResp
	if code := call("GET", "/api/spaces/"+sid+"/state", nil, &state); code != 200 {
		t.Fatal("state failed")
	}
	if len(state.Cards) != 1 || state.Cards[0].Status != "done" {
		t.Fatalf("card state wrong: %+v", state.Cards)
	}
}
