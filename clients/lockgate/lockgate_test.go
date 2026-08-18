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
		"name": "  alice  ", "passphrase": "correct horse battery",
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
	if c.DisplayName != "alice" {
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

// TestACodeCreatesAKeyNobodyHadToInvent is the ordinary first run.
//
// Four digits are what somebody types; they are NOT the key. The gate draws a
// real passphrase, the caller proves it opens a node, and only then does
// Confirm make the code start working — so the whole claim is end to end:
// nothing weaker than the wordlist ever guards the keystore, and the digits
// open exactly the identity that was made.
func TestACodeCreatesAKeyNobodyHadToInvent(t *testing.T) {
	dir := t.TempDir()
	srv, g := gate(t, dir)

	res := postJSON(t, srv.URL+"/create", map[string]any{"name": " alice ", "code": "4271"})
	if res["ok"] != true {
		t.Fatalf("create refused: %v", res)
	}
	c := <-g.Opened()
	if !c.Created || c.DisplayName != "alice" {
		t.Fatalf("credentials = %+v", c)
	}
	if c.BindCode != "4271" {
		t.Fatalf("BindCode = %q — the caller cannot bind what it was not told", c.BindCode)
	}
	// The generated passphrase is not the code, and is not something a person
	// under the pressure of a setup screen would have produced.
	if string(c.Passphrase) == "4271" || len(c.Passphrase) < 20 {
		t.Fatalf("generated passphrase is too weak to be the real key: %q", c.Passphrase)
	}

	rt, err := node.Open(dir, c.Passphrase, c.DisplayName)
	if err != nil {
		t.Fatalf("the generated key does not open a node: %v", err)
	}
	rt.Close()

	if err := g.Confirm(c); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	// A second gate over the same directory — the returning device.
	srv2, _ := gate(t, dir)
	if st := getJSON(t, srv2.URL+"/state"); st["mode"] != "passcode" {
		t.Fatalf("mode = %v, want passcode — the code did not become the way in", st["mode"])
	}
	if bad := postJSON(t, srv2.URL+"/unlock", map[string]any{"code": "9999"}); bad["ok"] != false {
		t.Fatalf("a wrong code opened the device: %v", bad)
	}
	if ok := postJSON(t, srv2.URL+"/unlock", map[string]any{"code": "4271"}); ok["ok"] != true {
		t.Fatalf("the chosen code does not open the device: %v", ok)
	}
}

// TestConfirmWithoutACodeIsNothing — the shell calls it on every open, and a
// returning person, or somebody who carries their own passphrase, must not
// have anything bound behind their back.
func TestConfirmWithoutACodeIsNothing(t *testing.T) {
	dir := t.TempDir()
	_, g := gate(t, dir)
	makeIdentity(t, dir, []byte("correct horse battery"))

	if err := g.Confirm(Credentials{Passphrase: []byte("correct horse battery")}); err != nil {
		t.Fatalf("Confirm with no code: %v", err)
	}
	srv2, _ := gate(t, dir)
	if st := getJSON(t, srv2.URL+"/state"); st["mode"] != "passphrase" {
		t.Fatalf("mode = %v, want passphrase — something was bound uninvited", st["mode"])
	}
}

// TestAMalformedCodeMakesNoIdentity. The KEYPAD only ever sends four digits,
// but /create is an HTTP endpoint and the rule that matters is the one the
// storage layer holds: 4 to 12 ASCII digits, nothing else. Too short, too
// long, non-digits, digits that merely look like digits — each must be
// refused BEFORE a key is drawn, or a device ends up holding an identity
// whose way in was never valid.
func TestAMalformedCodeMakesNoIdentity(t *testing.T) {
	for _, code := range []string{"", "123", "12a4", "1234567890123", "１２３４", "4271 "} {
		dir := t.TempDir()
		srv, g := gate(t, dir)
		res := postJSON(t, srv.URL+"/create", map[string]any{"name": "alice", "code": code})
		if res["ok"] == true {
			t.Fatalf("code %q was accepted", code)
		}
		select {
		case c := <-g.Opened():
			t.Fatalf("code %q still produced credentials: %+v", code, c)
		default:
		}
		if st := node.Inspect(dir); st.HasIdentity {
			t.Fatalf("code %q left an identity behind", code)
		}
	}
}
