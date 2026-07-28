//go:build unix

package storage

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive advisory lock without blocking.
//
// flock is tied to the open file description, which is what gives us
// crash-safety for free: when the process dies for any reason, the kernel
// closes its descriptors and the lock is gone. No timeout, no heartbeat, no
// stale-entry sweep.
func lockFile(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EWOULDBLOCK), errors.Is(err, unix.EAGAIN):
		// Someone live is holding it. This is the expected refusal.
		return ErrAlreadyRunning
	default:
		// ENOLCK, EOPNOTSUPP, EINVAL from a filesystem that does not really
		// implement locking. Report it so Lock can fail closed.
		return err
	}
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
