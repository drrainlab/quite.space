package storage_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
)

func TestSecondLockOnSameDirIsRefused(t *testing.T) {
	dir := t.TempDir()
	first, err := storage.Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	if _, err := storage.Lock(dir); !errors.Is(err, storage.ErrAlreadyRunning) {
		t.Fatalf("a second lock on the same directory should be refused, got: %v", err)
	}
	// A different directory is unaffected.
	other, err := storage.Lock(t.TempDir())
	if err != nil {
		t.Fatalf("locking an unrelated directory failed: %v", err)
	}
	other.Release()
}

func TestReleaseLetsTheNextProcessIn(t *testing.T) {
	dir := t.TempDir()
	first, err := storage.Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := storage.Lock(dir)
	if err != nil {
		t.Fatalf("the directory stayed locked after Release: %v", err)
	}
	second.Release()
	// Release is idempotent — shutdown paths run twice more often than
	// anyone expects.
	if err := first.Release(); err != nil {
		t.Fatalf("a second Release should be a no-op, got: %v", err)
	}
}

// The file is not the lock. A leftover file — from a crash, a backup, a
// half-finished copy — must never look like a live holder, because a person
// staring at "already running" with no other process running has no recourse.
func TestStaleLockFileIsNotALock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, storage.LockFileName)
	if err := os.WriteFile(path, []byte("12345\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := storage.Lock(dir)
	if err != nil {
		t.Fatalf("a leftover lock FILE was mistaken for a held lock: %v", err)
	}
	defer l.Release()

	// And the file survives: deleting it would open a window where one holder
	// owns an unlinked inode while a newcomer locks a fresh file.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the lock file should be left in place: %v", err)
	}
}

// The whole reason for choosing a descriptor-lifetime lock: a process that
// dies without cleaning up must not wedge the data directory forever. SIGKILL
// gives it no chance to run any cleanup at all.
func TestCrashedHolderReleasesTheLock(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a subprocess")
	}
	dir := t.TempDir()

	// A tiny helper process: take the lock, announce it, then sleep forever.
	helper := filepath.Join(t.TempDir(), "holder.go")
	src := `package main

import (
	"fmt"
	"os"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
)

func main() {
	if _, err := storage.Lock(os.Args[1]); err != nil {
		fmt.Println("ERR", err)
		os.Exit(1)
	}
	fmt.Println("HELD")
	os.Stdout.Sync()
	time.Sleep(10 * time.Minute)
}
`
	if err := os.WriteFile(helper, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	// BUILD, then run the binary directly. `go run` compiles and then execs a
	// child, so killing the `go run` process would leave the real holder alive
	// and this test would fail for a reason that has nothing to do with locks.
	bin := filepath.Join(t.TempDir(), "holder")
	build := exec.Command("go", "build", "-o", bin, helper)
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("could not build the holder: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	buf := make([]byte, 4)
	if _, err := out.Read(buf); err != nil {
		t.Skipf("could not start the holder subprocess: %v", err)
	}
	if string(buf) != "HELD" {
		t.Skipf("holder did not take the lock: %q", buf)
	}
	if _, err := storage.Lock(dir); !errors.Is(err, storage.ErrAlreadyRunning) {
		t.Fatalf("the subprocess holds the lock but we got in: %v", err)
	}

	// SIGKILL: no defer runs, no Release, nothing tidied.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	deadline := time.Now().Add(10 * time.Second)
	for {
		l, err := storage.Lock(dir)
		if err == nil {
			l.Release()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the lock survived the death of its holder: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// repoRoot finds the module root so `go run` resolves the import.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}

// The refusal has to name the remedy. "permission denied" sends a person to
// chmod; "already open in another process" sends them to the other window.
func TestTheRefusalExplainsItself(t *testing.T) {
	dir := t.TempDir()
	l, err := storage.Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	_, err = storage.Lock(dir)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"already open", "another"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal should mention %q: %v", want, err)
		}
	}
}
