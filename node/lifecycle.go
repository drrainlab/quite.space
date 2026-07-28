// Looking at a data directory without opening it.
//
// Every question a launcher needs to answer before it can show a sensible
// first screen — is there a node here? is one already running? is this the
// right passphrase? — used to require a full Open: scrypt, the ledger, and a
// replay of every space's log. That is the wrong shape for a probe, and it has
// a worse property than being slow: `storage.Open` CREATES a root on any path
// it is handed, so asking "is there anything here?" answered by making
// something.
//
// Nothing in this file writes, and nothing in it takes the data-directory
// lock, so all of it works while a node is running — including on the very
// directory that node has open.
package node

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/drrainlab/quiet_places/kernel/storage"
)

// Re-exported so callers need not import kernel/storage to tell these apart.
// Each one has a different remedy, and a UI that collapses them into "could
// not open" sends people to fix the wrong thing.
var (
	// ErrAlreadyRunning: close the other window.
	ErrAlreadyRunning = storage.ErrAlreadyRunning
	// ErrWrongPassphrase: try again — nothing is damaged.
	ErrWrongPassphrase = storage.ErrWrongPassphrase
	// ErrPassphraseTooShort: the input, not the data.
	ErrPassphraseTooShort = storage.ErrPassphraseTooShort
	// ErrNoRootHere: nothing is wrong; there is simply nothing here.
	ErrNoRootHere = storage.ErrNoRootHere
)

// DefaultDataDir is where a node lives unless told otherwise.
//
// It is a function, and every entry point calls it, because two of them used
// to disagree: the UI defaulted to ~/.quiet-places while the custodian tool
// defaulted to ./quiet-data. Pinning trust with one command and running the
// node with another therefore edited two different directories, and the
// mismatch was invisible — both commands succeeded.
func DefaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// No home is unusual but not fatal; a relative root still works and
		// is better than refusing to start.
		return "quiet-data"
	}
	return filepath.Join(home, ".quiet-places")
}

// State is what a directory is, seen from outside.
type State struct {
	// Dir is the directory asked about.
	Dir string
	// Exists: the directory is there at all.
	Exists bool
	// HasRoot: it holds Quiet Spaces data (a salt).
	HasRoot bool
	// HasIdentity: first run has happened (a keystore exists).
	HasIdentity bool
	// InUse: another live process holds the lock right now.
	InUse bool
	// Note carries anything that stopped the probe short.
	Note string
}

// Inspect answers what a launcher needs before it can draw anything, and
// creates nothing on any path — not the directory, not a salt, not a lock
// file that outlives the probe.
func Inspect(dataDir string) State {
	st := State{Dir: dataDir}
	if fi, err := os.Stat(dataDir); err == nil {
		st.Exists = fi.IsDir()
		if !fi.IsDir() {
			st.Note = "a file is in the way at this path"
			return st
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		st.Note = err.Error()
		return st
	}
	if !st.Exists {
		return st
	}
	st.HasRoot = storage.HasRoot(dataDir)
	st.HasIdentity = storage.HasKeystore(dataDir)
	st.InUse = dirInUse(dataDir)
	return st
}

// dirInUse probes the lock by taking it and letting go at once.
//
// Holding it for the length of a stat is the only way to ask the kernel this
// question, and it is honest about its own race: a launcher that starts in
// the microseconds between the acquire and the release sees "free" and then
// fails at Open with ErrAlreadyRunning — which is the correct answer arriving
// slightly later, not a wrong one.
func dirInUse(dataDir string) bool {
	l, err := storage.Lock(dataDir)
	if errors.Is(err, storage.ErrAlreadyRunning) {
		return true
	}
	if err != nil {
		// Cannot tell. Say no rather than inventing a blocker; Open will
		// refuse for the real reason a moment later.
		return false
	}
	_ = l.Release()
	return false
}

// VerifyPassphrase reports whether a passphrase opens this data directory.
//
// Deliberately does NOT take the lock and does NOT replay anything: it costs
// one scrypt derivation (~100ms) and one keystore decryption. That makes it
// usable while the node is running — to confirm an identity before an export,
// or to re-authenticate for a dangerous action — which a probe built on Open
// could never be, because Open would refuse itself.
//
// It creates nothing: a directory with no root answers ErrNoRootHere rather
// than quietly becoming a root.
func VerifyPassphrase(dataDir string, passphrase []byte) error {
	root, err := storage.OpenExisting(dataDir, passphrase)
	if err != nil {
		return err
	}
	_, err = root.LoadKeystore()
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrNotExist):
		// A salt but no keystore: first run never finished. The passphrase
		// cannot be wrong, because nothing has been sealed with one yet.
		return nil
	default:
		return err
	}
}
