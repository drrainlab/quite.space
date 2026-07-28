package node

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

// The promise BP-1 actually needs: a person who loses the machine gets back
// not only who they were, but what they were IN. The existing recovery bundle
// carries 2 of the keystore's 11 fields, so it could never do this.
func TestBackupRestoresTheSpacesAndTheHistory(t *testing.T) {
	dir := t.TempDir()
	rt, err := Open(dir, []byte(testPass), "alice")
	if err != nil {
		t.Fatal(err)
	}
	tid, err := rt.CreateSpace("Night Moss")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"first", "second", "third"} {
		if _, err := rt.Say(tid, m, SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint := rt.Principal.Fingerprint()

	var buf bytes.Buffer
	if err := WriteBackup(dir, []byte("backup-passphrase"), &buf); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	// A clean machine: nothing but the file.
	restored := filepath.Join(t.TempDir(), "restored")
	if err := ReadBackup(restored, []byte("backup-passphrase"), bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	rt2, err := Open(restored, []byte(testPass), "alice")
	if err != nil {
		t.Fatalf("the restored directory would not open: %v", err)
	}
	defer rt2.Close()

	if rt2.Principal.Fingerprint() != fingerprint {
		t.Fatal("identity did not survive the restore")
	}
	sp, ok := rt2.spaceForTest(tid)
	if !ok {
		t.Fatal("the space did not survive — this is exactly what the identity-only " +
			"recovery bundle could not carry")
	}
	msgs := sp.State.Messages()
	if len(msgs) != 3 {
		t.Fatalf("history did not survive: %d of 3 messages", len(msgs))
	}
	// Decryptable, not merely present: the epoch keys came too.
	if msgs[0].Text != "first" || msgs[2].Text != "third" {
		t.Fatalf("messages restored but unreadable: %+v", msgs)
	}
	// And the restored node can still write on the same chain.
	if _, err := rt2.Say(tid, "after the restore", SayOptions{}); err != nil {
		t.Fatalf("the restored node cannot write: %v", err)
	}
}

// A wrong passphrase and a damaged file must fail identically — telling them
// apart tells someone grinding a stolen backup when they are close.
func TestBackupFailuresAreIndistinguishable(t *testing.T) {
	dir := t.TempDir()
	rt, err := Open(dir, []byte(testPass), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.CreateSpace("x"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteBackup(dir, []byte("backup-passphrase"), &buf); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	wrongErr := ReadBackup(filepath.Join(t.TempDir(), "a"), []byte("wrong-passphrase"),
		bytes.NewReader(buf.Bytes()))
	if !errors.Is(wrongErr, ErrBackupUnreadable) {
		t.Fatalf("a wrong passphrase should be ErrBackupUnreadable, got: %v", wrongErr)
	}

	damaged := append([]byte(nil), buf.Bytes()...)
	damaged[len(damaged)-1] ^= 0x01
	damagedErr := ReadBackup(filepath.Join(t.TempDir(), "b"), []byte("backup-passphrase"),
		bytes.NewReader(damaged))
	if !errors.Is(damagedErr, ErrBackupUnreadable) {
		t.Fatalf("a damaged file should be ErrBackupUnreadable, got: %v", damagedErr)
	}
	if wrongErr.Error() != damagedErr.Error() {
		t.Fatalf("the two failures report differently:\n %v\n %v", wrongErr, damagedErr)
	}
}

// Restoring on top of a live root would interleave two histories, and the
// damage would only show up much later as a forked chain.
func TestRestoreRefusesANonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	rt, err := Open(dir, []byte(testPass), "alice")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteBackup(dir, []byte("backup-passphrase"), &buf); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	err = ReadBackup(dir, []byte("backup-passphrase"), bytes.NewReader(buf.Bytes()))
	if err == nil {
		t.Fatal("restored on top of an existing root")
	}
	if !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("the refusal should say what is wrong: %v", err)
	}
}

// The lock is a kernel fact about a live process, never a file worth carrying.
func TestBackupCarriesNoLock(t *testing.T) {
	dir := t.TempDir()
	rt, err := Open(dir, []byte(testPass), "alice")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteBackup(dir, []byte("backup-passphrase"), &buf); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	restored := filepath.Join(t.TempDir(), "r")
	if err := ReadBackup(restored, []byte("backup-passphrase"), bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(restored, "node.lock")); !os.IsNotExist(err) {
		t.Fatal("the backup carried a lock file")
	}
	// Which is the point: the restored directory opens immediately.
	rt2, err := Open(restored, []byte(testPass), "alice")
	if err != nil {
		t.Fatalf("the restored directory would not open: %v", err)
	}
	rt2.Close()
}

// A backup taken while the node runs must work — that is the only kind most
// people will ever take.
func TestBackupCanBeTakenWhileRunning(t *testing.T) {
	dir := t.TempDir()
	rt, err := Open(dir, []byte(testPass), "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if _, err := rt.CreateSpace("live"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteBackup(dir, []byte("backup-passphrase"), &buf); err != nil {
		t.Fatalf("could not back up a running node: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("empty backup")
	}
}

func TestBackupRefusesWhereThereIsNothing(t *testing.T) {
	var buf bytes.Buffer
	err := WriteBackup(t.TempDir(), []byte("backup-passphrase"), &buf)
	if !errors.Is(err, ErrNoRootHere) {
		t.Fatalf("expected ErrNoRootHere, got: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatal("wrote a backup of nothing")
	}
}

// A hostile archive naming ../../.ssh/authorized_keys must land nowhere. A
// legitimate backup never contains such a name, so this can only ever be an
// attack or corruption — refusing is the only correct response.
func TestRestoreRefusesPathsThatEscape(t *testing.T) {
	// Build a backup by hand, with one honest file and one that climbs out.
	var plain bytes.Buffer
	zw := gzip.NewWriter(&plain)
	tw := tar.NewWriter(zw)
	for _, name := range []string{"keys/salt", "../../escaped"} {
		body := []byte("x")
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: int64(len(body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	zw.Close()

	salt := make([]byte, backupSaltLen)
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	aead, err := backupAEAD([]byte("backup-passphrase"), salt)
	if err != nil {
		t.Fatal(err)
	}
	var file bytes.Buffer
	file.WriteString(backupMagic)
	file.Write(salt)
	file.Write(nonce)
	file.Write(aead.Seal(nil, nonce, plain.Bytes(), backupAAD))

	parent := t.TempDir()
	target := filepath.Join(parent, "restore-here")
	err = ReadBackup(target, []byte("backup-passphrase"), bytes.NewReader(file.Bytes()))
	if err == nil {
		t.Fatal("an archive that escapes the restore directory was accepted")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("the refusal should name the reason: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, "escaped")); !os.IsNotExist(err) {
		t.Fatal("a file was written outside the restore directory")
	}
}
