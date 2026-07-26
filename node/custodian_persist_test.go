package node

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// A pin is the whole of the trust decision for a gateway (ADR-015 §7: TOFU
// is forbidden). Losing it on restart does not fail loudly — receipts simply
// stop being honoured, messages stay "sending" forever, and nothing says
// why. A node that has to be re-pinned by hand after every restart is a node
// nobody will run in the field.
func TestPinsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	pub, _ := newKey(t)

	rt := openRuntime(t, dir, "bob")
	if err := rt.PinCustodian("radio", pub); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	again := openRuntime(t, dir, "bob")
	defer again.Close()

	pins := again.Custodians()
	if len(pins) != 1 {
		t.Fatalf("%d pins after restart, want 1", len(pins))
	}
	if !bytes.Equal(pins[0].Key, pub) {
		t.Fatal("the restored pin is not the key that was pinned")
	}
	if pins[0].LinkDomain != "radio" {
		t.Errorf("link domain = %q", pins[0].LinkDomain)
	}
	if pins[0].PinnedAt.IsZero() {
		t.Error("a pin with no date cannot be audited")
	}
}

// Replacing a pin is the beta's key-rotation story: the operator changed the
// gateway's key and told the person the new one. It must work, and it must
// leave a trace — a trust decision that changes with no record is how a
// silent re-pin would look.
func TestReplacingAPinIsRecorded(t *testing.T) {
	dir := t.TempDir()
	first, _ := newKey(t)
	second, _ := newKey(t)

	rt := openRuntime(t, dir, "bob")
	if err := rt.PinCustodian("radio", first); err != nil {
		t.Fatal(err)
	}
	if err := rt.PinCustodian("radio", second); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	again := openRuntime(t, dir, "bob")
	defer again.Close()
	pins := again.Custodians()
	if len(pins) != 1 {
		t.Fatalf("replacing a pin left %d pins, want 1", len(pins))
	}
	if !bytes.Equal(pins[0].Key, second) {
		t.Fatal("the replacement did not take")
	}
	if pins[0].Replaced == "" {
		t.Fatal("a replaced pin left no trace of what it replaced")
	}
	if !strings.HasPrefix(fingerprintOf(first), pins[0].Replaced) &&
		pins[0].Replaced != fingerprintOf(first) {
		t.Errorf("the trace does not identify the previous key: %q vs %q",
			pins[0].Replaced, fingerprintOf(first))
	}
}

// Unpinning is how someone withdraws trust from a gateway, and it has to
// stick across a restart too.
func TestUnpinSticks(t *testing.T) {
	dir := t.TempDir()
	pub, _ := newKey(t)

	rt := openRuntime(t, dir, "bob")
	if err := rt.PinCustodian("radio", pub); err != nil {
		t.Fatal(err)
	}
	if err := rt.UnpinCustodian("radio"); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	again := openRuntime(t, dir, "bob")
	defer again.Close()
	if pins := again.Custodians(); len(pins) != 0 {
		t.Fatalf("an unpinned custodian came back: %+v", pins)
	}
}

// A pin file this node cannot read must fail CLOSED and say so. Quietly
// continuing with no pins would turn "this gateway is trusted" into "no
// gateway is trusted" without a word, and the symptom — messages that never
// leave "sending" — points nowhere near the cause.
func TestAnUnreadablePinIsRefusedAndReported(t *testing.T) {
	dir := t.TempDir()
	good, _ := newKey(t)

	rt := openRuntime(t, dir, "bob")
	if err := rt.PinCustodian("radio", good); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	path := filepath.Join(dir, custodianFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("relay:x notahexkey\n")...), 0o600); err != nil {
		t.Fatal(err)
	}

	again := openRuntime(t, dir, "bob")
	defer again.Close()

	pins := again.Custodians()
	if len(pins) != 1 || !bytes.Equal(pins[0].Key, good) {
		t.Fatalf("a bad line cost the good pins: %+v", pins)
	}
	warnings := again.CustodianWarnings()
	if len(warnings) == 0 {
		t.Fatal("a pin that could not be read was dropped silently")
	}
	if !strings.Contains(strings.Join(warnings, " "), "relay:x") {
		t.Errorf("the warning does not name the line it lost: %v", warnings)
	}
	// And the unreadable domain is genuinely not trusted.
	if _, ok := again.custodians["relay:x"]; ok {
		t.Fatal("an unreadable pin was trusted anyway")
	}
}

// The pin file holds public keys, which are not secrets — that is what makes
// it a plain auditable file. This guards the boundary: nothing private, and
// nothing from the keystore, may end up in it.
func TestPinFileHoldsNothingPrivate(t *testing.T) {
	dir := t.TempDir()
	pub, priv := newKey(t)

	rt := openRuntime(t, dir, "bob")
	if err := rt.PinCustodian("radio", pub); err != nil {
		t.Fatal(err)
	}
	own := rt.Device.Seed()
	rt.Close()

	data, err := os.ReadFile(filepath.Join(dir, custodianFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, priv.Seed()) {
		t.Fatal("a custodian's private key reached the pin file")
	}
	if len(own) > 0 && bytes.Contains(data, own) {
		t.Fatal("this device's own signing key reached the pin file")
	}
}

// The beta workflow: the operator sends a key, the person runs
// `terminal custodian pin`, and the node picks it up. The CLI writes the
// file with no passphrase and without stopping the node, so the seam that
// has to hold is file-written-outside → trust-inside.
func TestAPinWrittenOutsideTheNodeIsHonoured(t *testing.T) {
	dir := t.TempDir()
	pub, _ := newKey(t)

	rt := openRuntime(t, dir, "bob")
	defer rt.Close()
	if len(rt.Custodians()) != 0 {
		t.Fatal("a fresh node already trusts something")
	}

	// What the CLI does.
	if err := SaveCustodianFile(CustodianPath(dir), []CustodianPin{{
		LinkDomain: "radio", Key: pub, Label: "roof gateway", PinnedAt: time.Now(),
	}}); err != nil {
		t.Fatal(err)
	}

	// Until the node is told, it has not changed its mind — trust is not
	// re-read behind the person's back on some timer.
	if len(rt.Custodians()) != 0 {
		t.Fatal("the node changed who it trusts without being asked")
	}
	rt.ReloadCustodians()

	pins := rt.Custodians()
	if len(pins) != 1 || !bytes.Equal(pins[0].Key, pub) {
		t.Fatalf("the node did not pick up the pin: %+v", pins)
	}
	if pins[0].Label != "roof gateway" {
		t.Errorf("label lost: %q", pins[0].Label)
	}
	rt.mu.Lock()
	_, trusted := rt.custodians["radio"]
	rt.mu.Unlock()
	if !trusted {
		t.Fatal("the pin is listed but receipts on that link would still prove nothing")
	}
}

// The write must be atomic. A power cut mid-save that leaves a half-written
// pin file would silently unpin a gateway on the next boot.
func TestPinWriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	pub, _ := newKey(t)
	rt := openRuntime(t, dir, "bob")
	defer rt.Close()
	if err := rt.PinCustodian("radio", pub); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("a temporary file survived the write: %s", e.Name())
		}
	}
}
