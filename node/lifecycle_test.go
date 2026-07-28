package node

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
)

const testPass = "passphrase-for-tests"

// The headline guarantee: one data directory, one process.
func TestSecondOpenOnSameDataDirIsRefused(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir, []byte(testPass), "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	second, err := Open(dir, []byte(testPass), "alice")
	if err == nil {
		second.Close()
		t.Fatal("two runtimes opened the same data directory — last writer would " +
			"silently win, and what it wins is the epoch keys")
	}
	if !errors.Is(err, storage.ErrAlreadyRunning) {
		t.Fatalf("expected ErrAlreadyRunning, got: %v", err)
	}
	// And it must be reported before the passphrase is even used, so a wrong
	// passphrase cannot mask the real cause.
	if _, err := Open(dir, []byte("a-completely-different-passphrase"), "alice"); !errors.Is(err, storage.ErrAlreadyRunning) {
		t.Fatalf("the lock should be reported ahead of any passphrase work, got: %v", err)
	}
}

// The single highest-value test in this gate. Before the abort path existed,
// Open leaked the store, the ledger and every opened log on thirteen error
// paths. That was invisible — until the lock made it "mistype your passphrase
// once, and you are locked out of your own data until you restart".
func TestFailedOpenReleasesTheLock(t *testing.T) {
	dir := t.TempDir()

	// Establish a keystore, then close cleanly.
	first, err := Open(dir, []byte(testPass), "alice")
	if err != nil {
		t.Fatal(err)
	}
	first.Close()

	// Now fail an Open with the wrong passphrase.
	if _, err := Open(dir, []byte("wrong-passphrase-entirely"), "alice"); err == nil {
		t.Fatal("a wrong passphrase was accepted")
	}

	// The directory must be usable again immediately.
	again, err := Open(dir, []byte(testPass), "alice")
	if err != nil {
		t.Fatalf("a failed Open kept the lock — the user is now locked out until "+
			"they restart the process: %v", err)
	}
	again.Close()
}

func TestCloseIsIdempotent(t *testing.T) {
	rt, err := Open(t.TempDir(), []byte(testPass), "alice")
	if err != nil {
		t.Fatal(err)
	}
	rt.Close()
	// A window-close callback and an application-quit callback both firing is
	// ordinary, not exotic. The second call must not panic.
	rt.Close()
	rt.Close()
}

// Shutdown must be bounded even when a loop is mid-wait, because a process
// that takes too long to exit gets killed — possibly mid keystore write.
func TestCloseIsBounded(t *testing.T) {
	dir := t.TempDir()
	rt, err := Open(dir, []byte(testPass), "alice")
	if err != nil {
		t.Fatal(err)
	}
	// A goroutine that sleeps the way the fetch loop used to: unbounded, and
	// deaf to the stop signal.
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		<-rt.stop
	}()

	start := time.Now()
	rt.Close()
	if took := time.Since(start); took > closeGrace+2*time.Second {
		t.Fatalf("Close took %v, which is past the %v grace period", took, closeGrace)
	}
	// A clean shutdown released the directory.
	next, err := Open(dir, []byte(testPass), "alice")
	if err != nil {
		t.Fatalf("the directory stayed locked after a clean shutdown: %v", err)
	}
	next.Close()
}

// sleepOrStop is what makes the bound above achievable.
func TestSleepOrStopReturnsOnShutdown(t *testing.T) {
	rt, err := Open(t.TempDir(), []byte(testPass), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !rt.sleepOrStop(time.Millisecond) {
		t.Fatal("a short wait should complete normally")
	}
	go func() { time.Sleep(50 * time.Millisecond); rt.Close() }()
	start := time.Now()
	if rt.sleepOrStop(30 * time.Second) {
		t.Fatal("the wait claimed to finish, but shutdown cut it short")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("shutdown took %v to interrupt the wait", took)
	}
}

// The lock file is left behind on purpose; its presence must never be mistaken
// for a live holder on the next start.
func TestALeftoverLockFileDoesNotBlockTheNextStart(t *testing.T) {
	dir := t.TempDir()
	rt, err := Open(dir, []byte(testPass), "alice")
	if err != nil {
		t.Fatal(err)
	}
	rt.Close()

	if _, err := os.Stat(filepath.Join(dir, storage.LockFileName)); err != nil {
		t.Fatalf("expected the lock file to remain: %v", err)
	}
	again, err := Open(dir, []byte(testPass), "alice")
	if err != nil {
		t.Fatalf("a leftover lock file blocked a fresh start: %v", err)
	}
	again.Close()
}

// The refusal a person actually reads.
func TestAlreadyRunningExplainsItself(t *testing.T) {
	dir := t.TempDir()
	rt, err := Open(dir, []byte(testPass), "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	_, err = Open(dir, []byte(testPass), "alice")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"already open", "another"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal should mention %q: %v", want, err)
		}
	}
}

// A probe must not be indistinguishable from a start. storage.Open creates a
// root on any path it is handed, so "is there a node here?" answered with Open
// would answer by making one — and somebody who mistyped a directory would
// find a plausible empty root at the typo while their real data sat elsewhere.
func TestInspectCreatesNothing(t *testing.T) {
	parent := t.TempDir()
	missing := filepath.Join(parent, "not-created-by-looking")

	st := Inspect(missing)
	if st.Exists || st.HasRoot || st.HasIdentity || st.InUse {
		t.Fatalf("a missing directory should report empty: %+v", st)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("Inspect created the directory it was asked about")
	}

	// An existing but empty directory: present, but no root, and still no
	// salt conjured into it.
	empty := filepath.Join(parent, "empty")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if st := Inspect(empty); !st.Exists || st.HasRoot || st.HasIdentity {
		t.Fatalf("an empty directory misreported: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(empty, "keys", "salt")); !os.IsNotExist(err) {
		t.Fatal("Inspect created a salt in a directory it was only looking at")
	}
}

func TestInspectSeesARealRootAndItsUse(t *testing.T) {
	dir := t.TempDir()
	if st := Inspect(dir); st.HasRoot || st.HasIdentity {
		t.Fatalf("a fresh temp dir already looks like a root: %+v", st)
	}
	rt, err := Open(dir, []byte(testPass), "alice")
	if err != nil {
		t.Fatal(err)
	}
	st := Inspect(dir)
	if !st.Exists || !st.HasRoot || !st.HasIdentity {
		t.Fatalf("a live root misreported: %+v", st)
	}
	if !st.InUse {
		t.Fatal("a running node was not reported as holding the directory")
	}
	rt.Close()
	if st := Inspect(dir); st.InUse {
		t.Fatalf("the directory still reads as in use after a clean shutdown: %+v", st)
	}
}

// The point of VerifyPassphrase: it works on a directory that is currently
// open, which anything built on Open never could — Open would refuse itself.
func TestVerifyPassphraseWorksWhileTheNodeIsRunning(t *testing.T) {
	dir := t.TempDir()
	rt, err := Open(dir, []byte(testPass), "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	if err := VerifyPassphrase(dir, []byte(testPass)); err != nil {
		t.Fatalf("the right passphrase was rejected on a running node: %v", err)
	}
	if err := VerifyPassphrase(dir, []byte("not-the-passphrase")); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("expected ErrWrongPassphrase, got: %v", err)
	}
	if err := VerifyPassphrase(dir, []byte("short")); !errors.Is(err, ErrPassphraseTooShort) {
		t.Fatalf("expected ErrPassphraseTooShort, got: %v", err)
	}
}

// Verifying against nothing must say "nothing here", not create a root and
// then cheerfully accept whatever was typed.
func TestVerifyAgainstAnEmptyDirectoryCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := VerifyPassphrase(dir, []byte(testPass)); !errors.Is(err, ErrNoRootHere) {
		t.Fatalf("expected ErrNoRootHere, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keys", "salt")); !os.IsNotExist(err) {
		t.Fatal("verifying a passphrase created a data root")
	}
}

// The two entry points used to default to different directories, so pinning
// trust with one command and running the node with the other silently edited
// two places.
func TestThereIsOneDefaultDataDir(t *testing.T) {
	if DefaultDataDir() == "" {
		t.Fatal("no default data directory")
	}
	if DefaultDataDir() != DefaultDataDir() {
		t.Fatal("the default is not stable")
	}
}
