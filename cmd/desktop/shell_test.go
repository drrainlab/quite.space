package main

// The shell, tested the way it was built: as an http.Handler.
//
// Nothing here starts Wails, opens a window or needs a display — which is the
// point of keeping the framework behind internal/wailsx. What these assert is
// the part that can actually be wrong: what is served while locked, that
// unlocking really does swap in the API, that a failed open says why rather
// than pretending, and that the node is closed exactly once however the
// process is asked to end.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	webui "github.com/drrainlab/quiet_places/clients/web-ui"
	"github.com/drrainlab/quiet_places/node"
)

func shellOn(t *testing.T, dir string) (*Shell, *httptest.Server) {
	t.Helper()
	s, err := NewShell(dir, webui.FS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Shutdown)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(b)
}

func createIdentity(t *testing.T, srv *httptest.Server, pass string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": "gleb", "passphrase": pass})
	resp, err := http.Post(srv.URL+"/create", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["ok"] != true {
		t.Fatalf("create refused: %v", out)
	}
}

// waitFor polls until cond holds. The swap to the API happens on another
// goroutine — node.Open is deliberately not on the caller's thread — so the
// test waits the same way the opening page does.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestALockedShellServesTheGateAndRefusesTheAPI. Both halves matter: a stale
// poll from a page that outlived a swap must get JSON it can read, not the
// lock screen's HTML.
func TestALockedShellServesTheGateAndRefusesTheAPI(t *testing.T) {
	_, srv := shellOn(t, t.TempDir())

	code, body := get(t, srv.URL+"/api/status")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/status = %d, want 503", code)
	}
	if !strings.Contains(body, `"locked"`) {
		t.Fatalf("body = %q, want a locked refusal", body)
	}

	code, body = get(t, srv.URL+"/")
	if code != http.StatusOK || !strings.Contains(body, "quite.space") {
		t.Fatalf("the gate did not answer: %d %.80q", code, body)
	}
}

// TestUnlockingSwapsInTheRealInterface is the wave's headline: after the gate
// there is no shell-shaped API in the way — it is the same handler `terminal
// ui` serves, and the token the gate handed out is the one that opens it.
func TestUnlockingSwapsInTheRealInterface(t *testing.T) {
	s, srv := shellOn(t, t.TempDir())

	done := make(chan error, 1)
	go func() { done <- s.Await(context.Background()) }()

	createIdentity(t, srv, "a passphrase that is long enough")

	if err := <-done; err != nil {
		t.Fatalf("Await: %v", err)
	}
	waitFor(t, "the API to answer", func() bool {
		code, _ := get(t, srv.URL+"/api/status?token="+s.token)
		return code == http.StatusOK
	})

	// And the token is genuinely required — the swap did not hand the API to
	// anybody who asks.
	if code, _ := get(t, srv.URL+"/api/status"); code == http.StatusOK {
		t.Fatal("the API answered without a token")
	}
}

// TestTheOpeningPageWaitsRatherThanAccusing.
//
// The gate's page navigates to /?token=… about seven hundred milliseconds
// after a correct answer, and node.Open can easily take longer. Without a
// third state that arrival would land back on the gate — which by then
// reports the directory as in use, because WE are the ones holding the lock —
// and the app would tell somebody it was already open somewhere else, in the
// middle of opening.
func TestTheOpeningPageWaitsRatherThanAccusing(t *testing.T) {
	rec := httptest.NewRecorder()
	openingHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/?token=x", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if strings.Contains(strings.ToLower(body), "already open") {
		t.Fatal("the opening page accuses the app of being open elsewhere")
	}
	if !strings.Contains(body, "location.reload()") {
		t.Fatal("the opening page does not come back — it would wait forever")
	}

	rec = httptest.NewRecorder()
	openingHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/status", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/api during opening = %d, want 503", rec.Code)
	}
}

// TestASecondShellSaysSoAndServesNoAPI. The gate's own in_use check races
// anything that opens between the probe and node.Open, so the failure has to
// be survivable at this end too.
func TestASecondShellSaysSoAndServesNoAPI(t *testing.T) {
	dir := t.TempDir()
	first, err := node.Open(dir, []byte("a passphrase that is long enough"), "gleb")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	s, srv := shellOn(t, dir)
	done := make(chan error, 1)
	go func() { done <- s.Await(context.Background()) }()

	// The gate reports in_use, but a person can still reach /unlock — so drive
	// exactly the path a race would take.
	body, _ := json.Marshal(map[string]any{"code": "a passphrase that is long enough"})
	resp, err := http.Post(srv.URL+"/unlock", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if err := <-done; err == nil {
		t.Fatal("a second shell opened a directory another node holds")
	}
	code, page := get(t, srv.URL+"/")
	if code != http.StatusOK || !strings.Contains(page, "already has this device open") {
		t.Fatalf("the failure page does not say why: %d %.200q", code, page)
	}
	if code, _ := get(t, srv.URL+"/api/status?token="+s.token); code == http.StatusOK {
		t.Fatal("a shell that never opened is serving the API")
	}
}

// TestShutdownIsIdempotentAndReleasesTheDirectory. Three things call it — the
// tray's Quit, the run loop returning, and main's belt-and-braces line — so
// "exactly once" is a property rather than a convention.
func TestShutdownIsIdempotentAndReleasesTheDirectory(t *testing.T) {
	dir := t.TempDir()
	s, srv := shellOn(t, dir)

	done := make(chan error, 1)
	go func() { done <- s.Await(context.Background()) }()
	createIdentity(t, srv, "a passphrase that is long enough")
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	s.Shutdown()
	s.Shutdown()
	s.Shutdown()

	// The lock is gone, which is the only observable proof the node really
	// closed rather than merely being forgotten about.
	again, err := node.Open(dir, []byte("a passphrase that is long enough"), "gleb")
	if err != nil {
		t.Fatalf("the directory was not released: %v", err)
	}
	again.Close()
}
