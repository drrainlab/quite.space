// A single-instance lock on the data directory.
//
// Two processes on one data root both hold a Keystore in memory and both
// write it back through tmp+rename. Last writer wins, silently, and what it
// wins is somebody's epoch keys — the storage tests already name the fear:
// "epoch keys lost — restart would lock us out of our own spaces". There is no
// server to restore from. This is the failure this file exists to make
// impossible.
//
// Three design choices, each load-bearing:
//
//   - NO PID HEURISTICS. The lock lives on the open file DESCRIPTION, held by
//     the kernel, so a crashed or SIGKILLed holder releases it the instant its
//     file descriptors close — with no reclaim race and nothing to clean up.
//     A lock file containing a pid, checked for staleness, would reintroduce
//     exactly the race it appears to solve. TestStaleLockFileIsNotALock pins
//     this against a future "helpful" staleness check.
//
//   - THE FILE IS NOT THE LOCK. Its existence means nothing at all; only the
//     kernel-held lock on the open description does. It is left behind on
//     purpose and never removed, because removing it opens a window in which
//     one process holds a lock on an unlinked inode while another creates a
//     fresh file and locks that.
//
//   - FAIL CLOSED. If the filesystem cannot lock — NFS, SMB, some container
//     overlays — this refuses to open rather than proceeding unprotected, and
//     there is no environment variable to override it. An advisory lock that
//     silently does nothing is worse than none, because it is believed.
package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// LockFileName is the lock's presence in the data directory. Auditable by
// eye, meaningless as a file — see the package note above.
const LockFileName = "node.lock"

// ErrAlreadyRunning means another process holds this data directory. It is a
// distinct error because it is the one lock failure with a remedy a person can
// act on, and the UI should say so rather than reporting a generic I/O fault.
var ErrAlreadyRunning = errors.New(
	"storage: this data directory is already open in another Quiet Spaces process")

// DirLock is a held single-instance lock. Release exactly once, at shutdown.
type DirLock struct {
	f *os.File
}

// Lock takes the data directory for this process, creating the directory if
// needed. It returns ErrAlreadyRunning if another live process holds it.
//
// Call this BEFORE unlocking the store: a person who launched the app twice
// should hear "already running" immediately, not after a hundred milliseconds
// of scrypt and a passphrase prompt they did not need to answer.
func Lock(dir string) (*DirLock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, LockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("storage: cannot open the lock file: %w", err)
	}
	if err := lockFile(f); err != nil {
		f.Close()
		if errors.Is(err, ErrAlreadyRunning) {
			return nil, ErrAlreadyRunning
		}
		// Anything else means the filesystem would not give us a lock we can
		// trust. Refuse rather than run unprotected.
		return nil, fmt.Errorf(
			"storage: this filesystem cannot lock %s, so a second copy of the app "+
				"could not be prevented from overwriting your keys — refusing to open "+
				"(network filesystems commonly cannot; keep the data directory on a "+
				"local disk): %w", dir, err)
	}
	return &DirLock{f: f}, nil
}

// Release drops the lock. Safe to call twice; the second is a no-op.
//
// Deliberately does NOT delete the lock file — see the package note. Closing
// the descriptor is what releases the kernel's hold.
func (l *DirLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	// unlockFile first so the release is explicit and ordered; Close alone
	// would also do it, but only as a side effect of descriptor teardown.
	_ = unlockFile(f)
	return f.Close()
}

// Path is where the lock lives, for diagnostics.
func (l *DirLock) Path() string {
	if l == nil || l.f == nil {
		return ""
	}
	return l.f.Name()
}
