package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

var testPSK = []byte{
	0x9f, 0x2a, 0x01, 0x77, 0xbe, 0xef, 0x12, 0x34,
	0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc,
}

// startNode runs a simulated Meshtastic node reporting cfg, and returns the
// --radio target for it.
func startNode(t *testing.T, cfg *meshtastic.HubConfig) string {
	t.Helper()
	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hub.Close() })
	hub.SetConfig(cfg)
	return "tcp:" + hub.Addr()
}

func goodConfig() *meshtastic.HubConfig {
	return &meshtastic.HubConfig{
		Region: 3 /* EU_868 */, ModemPreset: 0 /* LONG_FAST */, HopLimit: 3,
		UsePreset: true, TxEnabled: true,
		ChannelName: "quiet-beta", PSK: testPSK, Firmware: "2.5.13.abcdef",
	}
}

// capture runs the command and returns its stdout and exit code.
func capture(t *testing.T, args ...string) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	code := run(args)
	w.Close()
	os.Stdout = old
	return <-done, code
}

// The workflow the beta depends on: set one radio up by hand, capture its
// profile, check the others against it.
func TestCaptureThenVerify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beta.profile")
	target := startNode(t, goodConfig())

	if out, code := capture(t, "--radio", target, "--save-profile", path); code != 0 {
		t.Fatalf("capture failed (%d):\n%s", code, out)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), string(testPSK)) {
		t.Fatal("the captured profile carries the channel key")
	}

	// A second node configured the same way passes.
	out, code := capture(t, "--radio", startNode(t, goodConfig()), "--profile", path)
	if code != 0 {
		t.Fatalf("a correctly configured node did not pass (%d):\n%s", code, out)
	}
	if !strings.Contains(out, "configured for the segment") {
		t.Errorf("no clear verdict:\n%s", out)
	}
}

// The failure the whole gate exists for: a node on the wrong region. Both
// radios work perfectly and hear nothing. The output has to name the field,
// both values, and the command that fixes it.
func TestWrongRegionIsNamedAndFixable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beta.profile")
	if _, code := capture(t, "--radio", startNode(t, goodConfig()),
		"--save-profile", path); code != 0 {
		t.Fatal("capture failed")
	}

	wrong := goodConfig()
	wrong.Region = 2 // EU_433
	out, code := capture(t, "--radio", startNode(t, wrong), "--profile", path)
	if code != 1 {
		t.Fatalf("a wrong region exited %d, want 1:\n%s", code, out)
	}
	for _, want := range []string{
		"lora.region", "EU_433", "EU_868", "--set lora.region EU_868",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
}

// A wrong key is the cruellest failure — everything else matches and the
// segment is still silent — and the one where the fix must not print the
// key it is asking for.
func TestWrongKeyIsReportedWithoutPrintingAnyKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beta.profile")
	if _, code := capture(t, "--radio", startNode(t, goodConfig()),
		"--save-profile", path); code != 0 {
		t.Fatal("capture failed")
	}

	other := goodConfig()
	other.PSK = append([]byte(nil), testPSK...)
	other.PSK[0] ^= 0xff
	out, code := capture(t, "--radio", startNode(t, other), "--profile", path)
	if code != 1 {
		t.Fatalf("a different channel key exited %d, want 1:\n%s", code, out)
	}
	if !strings.Contains(out, "channel.key") {
		t.Errorf("the key mismatch was not named:\n%s", out)
	}
	for _, key := range [][]byte{testPSK, other.PSK} {
		if strings.Contains(out, string(key)) {
			t.Fatal("a channel key was printed")
		}
	}
}

// A node that reports nothing is unverified, not broken. Exit 3 keeps that
// distinct from exit 1 so a script — and a person — can tell them apart.
func TestSilentNodeExitsUnverifiedNotFailed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beta.profile")
	if err := os.WriteFile(path, []byte("region = EU_868\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := capture(t, "--radio", startNode(t, nil), "--profile", path)
	if code != 3 {
		t.Fatalf("a silent node exited %d, want 3:\n%s", code, out)
	}
	if strings.Contains(out, "not on the same air") {
		t.Errorf("a node that reported nothing was called misconfigured:\n%s", out)
	}
}

// Capturing a profile from a node that reported nothing would produce a file
// that verifies nothing — and it would look like a working profile.
func TestCaptureRefusesASilentNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.profile")
	if _, code := capture(t, "--radio", startNode(t, nil), "--save-profile", path); code != 2 {
		t.Fatalf("captured a profile from a node that reported nothing (exit %d)", code)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("an empty profile was written")
	}
}
