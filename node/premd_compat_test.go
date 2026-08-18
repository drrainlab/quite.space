package node

import (
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// The two compatibility proofs for the identity migration (MD-0…0c), run
// against a GENUINE artifact: node/testdata/premd holds a data dir written
// by the last pre-certification build (commit 73b238e — see its README).
// Nothing here emulates the old build; these are its actual bytes.

func premdDataDir(t *testing.T) (string, id.TerminalID) {
	t.Helper()
	dst := t.TempDir()
	src := filepath.Join("testdata", "premd", "data")
	if err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(out, 0o700)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(f, in)
		return err
	}); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "premd", "tid"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(b) != len(id.TerminalID{}) {
		t.Fatalf("bad fixture tid: %v", err)
	}
	var tid id.TerminalID
	copy(tid[:], b)
	return dst, tid
}

// An EXISTING replica with pre-certification history upgrades in place: the
// keystore grows its two new keys (arity 19 → 21), the legacy allowlist
// freezes from the history already here, the gate goes on — and the catalog
// is exactly what it was. An upgrade is never a reason to lose your own
// spaces or stop hearing the people already in them.
func TestLegacyPublicMirrorSurvivesIdentityMigrationInPlace(t *testing.T) {
	dir, tid := premdDataDir(t)

	rt := openRuntime(t, dir, "curator")
	// THE MIGRATION HAPPENED: pairs that already had history here are frozen.
	if len(rt.ks.LegacyBindings) == 0 {
		t.Fatal("the migration froze nothing from a data dir full of history")
	}
	// THE CATALOG IS UNTOUCHED: same two publications, visible exactly once.
	for _, text := range []string{"release notes: 0.1.1", "welcome to the quiet directory"} {
		if got := countMsg(t, rt, tid, text); got != 1 {
			t.Fatalf("%q visible %d times after the migration, want exactly 1", text, got)
		}
	}
	// THE GATE IS ON AND THE OWNER STILL SPEAKS: the device now also holds a
	// certificate it never had, minted at this open.
	if _, err := rt.Say(tid, "post-migration release note", SayOptions{}); err != nil {
		t.Fatalf("the upgraded owner cannot write into its own catalog: %v", err)
	}
	if _, known := rt.ident.certificateFor(rt.Device.ID); !known {
		t.Fatal("the upgraded device did not certify itself")
	}
	rt.Close()

	// AND IT SURVIVES A RESTART — the allowlist and the new certificate are
	// durable, not a property of the migrating process.
	rt2 := openRuntime(t, dir, "curator")
	defer rt2.Close()
	if len(rt2.ks.LegacyBindings) == 0 {
		t.Fatal("the frozen allowlist did not survive the restart")
	}
	for _, text := range []string{"release notes: 0.1.1", "welcome to the quiet directory",
		"post-migration release note"} {
		if got := countMsg(t, rt2, tid, text); got != 1 {
			t.Fatalf("%q visible %d times after restart, want exactly 1", text, got)
		}
	}
}

// A FRESH node opens the old public directory TODAY. Its allowlist is empty
// — it never saw this history before the migration — so the old frames are
// admissible only through decision C: the upgraded publisher's certificate
// travels with the history, is learned before judgement, and the chain
// position of the proof does not matter. This is the test that says the
// official catalog does not need to be recreated.
func TestPreCertificatePublicSpaceCanBeOpenedByAFreshNode(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()

	dir, tid := premdDataDir(t)
	alice := openRuntime(t, dir, "curator") // the upgraded publisher
	defer alice.Close()
	if err := alice.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := alice.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}

	bobDir := t.TempDir()
	bob := openRuntime(t, bobDir, "reader")
	if err := bob.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		_ = bob.fetchPublicProjection(addr, tid)
		if countMsg(t, bob, tid, "release notes: 0.1.1") >= 1 &&
			countMsg(t, bob, tid, "welcome to the quiet directory") >= 1 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Exactly once each — learning the certificate and re-judging must never
	// duplicate what it releases.
	for _, text := range []string{"release notes: 0.1.1", "welcome to the quiet directory"} {
		if got := countMsg(t, bob, tid, text); got != 1 {
			t.Fatalf("%q visible %d times on a fresh mirror, want exactly 1", text, got)
		}
	}
	// Nothing is stuck waiting for a certificate: the proof travelled with
	// the history it authorises.
	for _, ref := range bob.IngressRefusals() {
		if ref.Reason == "certificate_not_known" {
			t.Fatalf("a fresh mirror left history waiting on a certificate: %+v", ref)
		}
	}
	if h, err := bob.ingressHold(); err == nil {
		if items, _ := h.List(); len(items) != 0 {
			t.Fatalf("%d frame(s) stuck in the ingress hold on a fresh mirror", len(items))
		}
	}
	bob.Close()

	// RESTART THE MIRROR: what it holds, it holds — with at most a re-fetch
	// from the relay and NO new action from the publisher.
	bob2 := openRuntime(t, bobDir, "reader")
	defer bob2.Close()
	if countMsg(t, bob2, tid, "release notes: 0.1.1") == 0 {
		_ = bob2.fetchPublicProjection(addr, tid)
	}
	for _, text := range []string{"release notes: 0.1.1", "welcome to the quiet directory"} {
		if got := countMsg(t, bob2, tid, text); got != 1 {
			t.Fatalf("%q visible %d times after the mirror restarted, want 1", text, got)
		}
	}
}

// THE SECOND DEVICE INHERITS THE ALLOWLIST. A laptop that migrated froze
// its legacy bindings — the pre-certification devices it already knew — and
// admits their history by that list. A phone paired afterwards was born
// after the migration: empty allowlist, and no certificate for those
// devices exists anywhere, so every old frame the laptop relays sits in the
// phone's hold forever. Measured on the first live pairing as "no history,
// no other people, no names, no media". The allowlist is the PERSON's
// knowledge and rides the freight with the rest of what the person knows.
func TestAPairedPhoneReadsHistoryFromBeforeCertificationExisted(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	now := uint64(time.Now().Unix())

	dir, tid := premdDataDir(t)
	laptop := openRuntime(t, dir, "curator") // migrated: allowlist frozen
	defer laptop.Close()
	setPersonalRelay(t, laptop, addr)
	if len(laptop.ks.LegacyBindings) == 0 {
		t.Fatal("the fixture did not migrate an allowlist")
	}

	// A FRIEND WHO NEVER UPGRADED. Her frames were admitted here by the
	// laptop's frozen allowlist; no certificate for her device exists
	// anywhere. Simulated by an author the laptop grandfathers in by hand.
	friend := newTestAuthor(t, tid, 0x77)
	lsp, _ := laptop.spaceForTest(tid) // takes r.mu itself — never call it under the lock
	laptop.mu.Lock()
	laptop.ident.legacy[storage.LegacyBinding{Principal: friend.prin, Device: friend.dev}] = true
	laptop.ks.LegacyBindings = append(laptop.ks.LegacyBindings,
		storage.LegacyBinding{Principal: friend.prin, Device: friend.dev})
	_, err := lsp.Log.Ingest(friend.frameAt(t, "a friend, from before certificates", 1))
	laptop.mu.Unlock()
	if err != nil {
		t.Fatalf("the laptop refused a grandfathered friend: %v", err)
	}

	phone := pairChild(t, laptop, now)
	setPersonalRelay(t, phone, addr)
	if _, _, err := laptop.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	waitUntilMsg(t, phone, addr, tid, "release notes: 0.1.1")
	waitUntilMsg(t, phone, addr, tid, "a friend, from before certificates")
	if got := countMsg(t, phone, tid, "welcome to the quiet directory"); got != 1 {
		t.Fatalf("pre-certification history on the phone: %d, want 1", got)
	}
	if h, err := phone.ingressHold(); err == nil {
		if items, _ := h.List(); len(items) != 0 {
			t.Fatalf("%d frame(s) stuck in the phone's hold — history it can never release", len(items))
		}
	}
}
