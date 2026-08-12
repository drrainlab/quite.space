package lockgate

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/node"
)

// gate stands one up over a temp directory. httptest is the whole harness:
// the gate is an http.Handler and nothing about it needs a window.
func gate(t *testing.T, dir string) (*httptest.Server, *Gate) {
	t.Helper()
	g, err := New(dir, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)
	return srv, g
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return out
}

func postJSON(t *testing.T, url string, body any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

// makeIdentity opens and closes a node so the directory holds a real keystore.
func makeIdentity(t *testing.T, dir string, pass []byte) {
	t.Helper()
	rt, err := node.Open(dir, pass, "me")
	if err != nil {
		t.Fatalf("node.Open: %v", err)
	}
	rt.Close()
}

// TestTheFloorStillMatchesStorage is why MinPassphrase may be a literal here.
//
// The gate refuses a short passphrase at the field, before any derivation, so
// somebody typing one is told immediately rather than after a scrypt. That is
// only honest while the number agrees with the one node.Open actually enforces
// — so this asserts BOTH directions: one byte under is refused there, and
// exactly MinPassphrase is accepted there.
func TestTheFloorStillMatchesStorage(t *testing.T) {
	short := bytes.Repeat([]byte("a"), MinPassphrase-1)
	if _, err := node.Open(t.TempDir(), short, "me"); !errors.Is(err, storage.ErrPassphraseTooShort) {
		t.Fatalf("storage accepted %d bytes (or failed otherwise): %v", len(short), err)
	}
	exact := bytes.Repeat([]byte("a"), MinPassphrase)
	dir := t.TempDir()
	rt, err := node.Open(dir, exact, "me")
	if err != nil {
		t.Fatalf("storage refused exactly MinPassphrase=%d: %v", MinPassphrase, err)
	}
	rt.Close()
}

// TestAnEmptyDirectoryAsksToCreateAndCreatesNothing is the property node.Inspect
// exists for, seen from the screen: asking "is there anything here?" must not
// answer by making something.
func TestAnEmptyDirectoryAsksToCreateAndCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-yet")
	srv, _ := gate(t, dir)

	if got := getJSON(t, srv.URL+"/state")["mode"]; got != "create" {
		t.Fatalf("mode = %v, want create", got)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the probe created %s", dir)
	}
}

// TestCreateHandsBackWhatOpenWillNeed. The gate never opens anything itself —
// its whole output is the credentials somebody else calls node.Open with.
func TestCreateHandsBackWhatOpenWillNeed(t *testing.T) {
	dir := t.TempDir()
	srv, g := gate(t, dir)

	res := postJSON(t, srv.URL+"/create", map[string]any{
		"name": "  gleb  ", "passphrase": "correct horse battery",
	})
	if res["ok"] != true {
		t.Fatalf("create refused: %v", res)
	}
	if res["token"] != "test-token" {
		t.Fatalf("token = %v", res["token"])
	}

	c := <-g.Opened()
	if !c.Created {
		t.Fatal("Created is false — the caller cannot tell this was a first run")
	}
	if c.DisplayName != "gleb" {
		t.Fatalf("DisplayName = %q, want the trimmed name", c.DisplayName)
	}
	if string(c.Passphrase) != "correct horse battery" {
		t.Fatalf("Passphrase = %q", c.Passphrase)
	}
	// And the credentials really do open a node — which is the only claim
	// worth making about them.
	rt, err := node.Open(dir, c.Passphrase, c.DisplayName)
	if err != nil {
		t.Fatalf("the credentials the gate handed back do not open: %v", err)
	}
	rt.Close()
}

// TestAShortPassphraseIsRefusedBeforeAnythingIsMade — and the refusal says the
// floor, so the screen can repeat the rule rather than invent one.
func TestAShortPassphraseIsRefusedBeforeAnythingIsMade(t *testing.T) {
	dir := t.TempDir()
	srv, _ := gate(t, dir)

	res := postJSON(t, srv.URL+"/create", map[string]any{"passphrase": "short"})
	if res["ok"] == true {
		t.Fatal("a five-character passphrase was accepted")
	}
	if res["reason"] != "too_short" {
		t.Fatalf("reason = %v, want too_short", res["reason"])
	}
	if storage.HasKeystore(dir) {
		t.Fatal("a refused first run left a keystore behind")
	}
}

// TestCreateCannotOverwriteAnIdentity. A first-run screen that raced an
// existing keystore would be a way to lose everything on the device, so the
// mode is not merely hidden in the UI — the endpoint refuses.
func TestCreateCannotOverwriteAnIdentity(t *testing.T) {
	dir := t.TempDir()
	makeIdentity(t, dir, []byte("the original passphrase"))

	srv, _ := gate(t, dir)
	if got := getJSON(t, srv.URL+"/state")["mode"]; got != "passphrase" {
		t.Fatalf("mode = %v, want passphrase", got)
	}
	res := postJSON(t, srv.URL+"/create", map[string]any{"passphrase": "a different one"})
	if res["ok"] == true {
		t.Fatal("create overwrote an existing identity")
	}
	if res["reason"] != "exists" {
		t.Fatalf("reason = %v, want exists", res["reason"])
	}
	// The original still opens.
	rt, err := node.Open(dir, []byte("the original passphrase"), "me")
	if err != nil {
		t.Fatalf("the identity did not survive: %v", err)
	}
	rt.Close()
}

// TestARunningNodeIsReportedAsAlreadyOpen. Not a wrong answer and not a
// failure — a different situation, with a different instruction.
func TestARunningNodeIsReportedAsAlreadyOpen(t *testing.T) {
	dir := t.TempDir()
	rt, err := node.Open(dir, []byte("a passphrase that is long enough"), "me")
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	srv, _ := gate(t, dir)
	if got := getJSON(t, srv.URL+"/state")["mode"]; got != "in_use" {
		t.Fatalf("mode = %v, want in_use", got)
	}
}

// TestTheRightPassphraseOpensAndTheWrongOneDoesNotSpendAnything.
func TestTheRightPassphraseOpensAndTheWrongOneDoesNotSpendAnything(t *testing.T) {
	dir := t.TempDir()
	makeIdentity(t, dir, []byte("the real passphrase"))
	srv, g := gate(t, dir)

	if res := postJSON(t, srv.URL+"/unlock", map[string]any{"code": "not it at all"}); res["ok"] == true {
		t.Fatal("a wrong passphrase opened the gate")
	}
	res := postJSON(t, srv.URL+"/unlock", map[string]any{"code": "the real passphrase"})
	if res["ok"] != true {
		t.Fatalf("the right passphrase was refused: %v", res)
	}
	c := <-g.Opened()
	if c.Created {
		t.Fatal("an existing identity was reported as a first run")
	}
	if string(c.Passphrase) != "the real passphrase" {
		t.Fatalf("Passphrase = %q", c.Passphrase)
	}
}

// TestSuggestOffersAndBindsNothing. It is a convenience, not a decision.
func TestSuggestOffersAndBindsNothing(t *testing.T) {
	dir := t.TempDir()
	srv, _ := gate(t, dir)

	first := getJSON(t, srv.URL+"/suggest")["passphrase"]
	second := getJSON(t, srv.URL+"/suggest")["passphrase"]
	s, ok := first.(string)
	if !ok || len(s) < MinPassphrase {
		t.Fatalf("suggested %q", first)
	}
	if first == second {
		t.Fatal("two suggestions were identical")
	}
	if storage.HasRoot(dir) || storage.HasKeystore(dir) {
		t.Fatal("suggesting a passphrase created something")
	}
}

// TestTheTokenNeverTravelsBeforeSomebodyIsIn. The page is reachable without
// authenticating — nobody has authenticated yet — so the only place the
// session token may appear is the reply that let somebody in.
func TestTheTokenNeverTravelsBeforeSomebodyIsIn(t *testing.T) {
	dir := t.TempDir()
	makeIdentity(t, dir, []byte("the real passphrase"))
	srv, _ := gate(t, dir)

	for _, path := range []string{"/", "/state", "/suggest"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body := make([]byte, 1<<20)
		n, _ := resp.Body.Read(body)
		resp.Body.Close()
		if bytes.Contains(body[:n], []byte("test-token")) {
			t.Fatalf("%s handed out the session token", path)
		}
	}
	res := postJSON(t, srv.URL+"/unlock", map[string]any{"code": "wrong"})
	if res["token"] != nil {
		t.Fatal("a refusal handed out the session token")
	}
}
