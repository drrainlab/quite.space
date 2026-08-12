// RR-1: relay identity — SPKI pin sets, the untrusted state, confirmable
// TOFU and the local-lan exemption. Real TLS handshakes against a real
// relay with a persistent-style identity.
package node

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/drrainlab/quiet_places/transports/relay"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// testIdentity builds a persistent-style relay identity and its pin.
func testIdentity(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv},
		base64.StdEncoding.EncodeToString(sum[:])
}

func startPinnedRelay(t *testing.T) (addr, pin string, srv *relayserver.Server) {
	t.Helper()
	cert, pin := testIdentity(t)
	srv, port, err := relayserver.StartServerWithIdentity("127.0.0.1:0", relayserver.DefaultLimits(), &cert)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("127.0.0.1:%d", port), pin, srv
}

func TestPinnedDialAcceptsTheRightKeyAndItsRotationSet(t *testing.T) {
	addr, pin, srv := startPinnedRelay(t)
	defer srv.Close()

	// Exact pin.
	c, err := relay.DialClientPinned(addr, pinVerifier(addr, []string{pin}))
	if err != nil {
		t.Fatalf("the right pin was refused: %v", err)
	}
	c.Close()
	// Rotation set [previous, current] — the current key matches the SET.
	c, err = relay.DialClientPinned(addr, pinVerifier(addr, []string{"OLDKEY", pin}))
	if err != nil {
		t.Fatalf("a rotation set containing the current key was refused: %v", err)
	}
	c.Close()
}

func TestPinMismatchIsUntrustedNotANetworkError(t *testing.T) {
	addr, _, srv := startPinnedRelay(t)
	defer srv.Close()

	_, err := relay.DialClientPinned(addr, pinVerifier(addr, []string{"WRONG"}))
	if err == nil {
		t.Fatal("a wrong pin was accepted")
	}
	err = unwrapPinError(err)
	var untrusted ErrRelayUntrusted
	if !errors.As(err, &untrusted) {
		t.Fatalf("mismatch surfaced as %T (%v), not ErrRelayUntrusted", err, err)
	}
}

func TestRelayIdentityObservesWithoutTrusting(t *testing.T) {
	addr, pin, srv := startPinnedRelay(t)
	defer srv.Close()

	got, err := RelayIdentity(addr)
	if err != nil {
		t.Fatal(err)
	}
	if got != pin {
		t.Fatalf("observed %q, relay presents %q", got, pin)
	}
}

func TestTOFUConfirmThenEnforce(t *testing.T) {
	addr, pin, srv := startPinnedRelay(t)
	defer srv.Close()
	dir := t.TempDir()

	// Confirming a WRONG fingerprint is refused — no blind trust.
	if err := TrustRelayAt(dir, addr, "SOMETHING-ELSE"); err == nil {
		t.Fatal("a wrong fingerprint was confirmable")
	}
	if err := TrustRelayAt(dir, addr, pin); err != nil {
		t.Fatal(err)
	}
	if got, ok := LoadRelayStateAt(dir).TrustedPin(addr); !ok || got != pin {
		t.Fatalf("pin not stored: %q %v", got, ok)
	}
	// The relay's key CHANGES (new server, same address impossible in-test;
	// simulate by checking the verifier the stored pin builds).
	if err := pinVerifier(addr, []string{pin})("DIFFERENT"); err == nil {
		t.Fatal("a changed key passed the confirmed pin")
	}
	// Forget drops it.
	if err := ForgetRelayAt(dir, addr); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadRelayStateAt(dir).TrustedPin(addr); ok {
		t.Fatal("forget kept the pin")
	}
}

// dialRelay profile decisions, without real dials where refusal is the point.
func TestDialRelayProfiles(t *testing.T) {
	r := openRuntime(t, t.TempDir(), "profiles")
	defer r.Close()

	// Loopback → plain dial path (the dial itself fails on a dead port,
	// but it must NOT fail with a trust error).
	_, err := r.dialRelay("127.0.0.1:1")
	if err == nil {
		t.Fatal("dead port dialed successfully?")
	}
	var needs ErrRelayNeedsTrust
	var untrusted ErrRelayUntrusted
	if errors.As(err, &needs) || errors.As(err, &untrusted) {
		t.Fatalf("loopback hit the trust machinery: %v", err)
	}

	// Custom non-local without a confirmed pin → refused BEFORE dialing.
	_, err = r.dialRelay("relay.example.org:7411")
	if !errors.As(err, &needs) {
		t.Fatalf("an unconfirmed custom relay was not refused for trust: %v", err)
	}
}

// A confirmed custom pin makes dialRelay verify — and a live relay with
// the matching key connects end to end.
func TestDialRelayUsesTheConfirmedPin(t *testing.T) {
	addr, pin, srv := startPinnedRelay(t)
	defer srv.Close()
	r := openRuntime(t, t.TempDir(), "tofu-dial")
	defer r.Close()

	// 127.0.0.1 is loopback (plain) — force the custom path via a hosts-
	// style alias is impossible in-test, so verify the TOFU branch by
	// pinning and calling the pinned dialer directly with the stored pin.
	if err := r.TrustRelay(addr, pin); err != nil {
		t.Fatal(err)
	}
	stored, ok := r.loadRelayState().TrustedPin(addr)
	if !ok {
		t.Fatal("pin missing after TrustRelay")
	}
	c, err := relay.DialClientPinned(addr, pinVerifier(addr, []string{stored}))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}
