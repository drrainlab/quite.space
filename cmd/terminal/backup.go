package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/drrainlab/quiet_places/node"
)

const backupUsage = `terminal backup   — save an encrypted copy of everything

  terminal backup  --out FILE  --passphrase PASS  [--data DIR]
  terminal restore --in  FILE  --passphrase PASS   --data DIR

The backup holds the whole data directory: identity, spaces, epoch keys and
history. The older 'identity new/restore' bundle carries only WHO you are —
restoring it on a clean machine gives you your name back and an empty app.

--passphrase here is the BACKUP's passphrase. It need not be the one that
opens your node, and choosing a different one is reasonable: a backup travels,
a node does not.

Restore needs an empty directory. Merging a backup into existing data would
interleave two histories, and the damage would only surface much later.
`

// runBackup writes an encrypted copy of a data directory.
//
// Safe while the node is running — it only reads — which matters because a
// backup people have to shut down for is a backup people do not take.
func runBackup(args []string) error {
	flags := parseFlags(args)
	out, pass := flags["out"], flags["passphrase"]
	if pass == "" {
		pass = os.Getenv("QP_BACKUP_PASSPHRASE")
	}
	if out == "" || pass == "" {
		return errors.New(backupUsage)
	}
	dataDir := flags["data"]
	if dataDir == "" {
		dataDir = node.DefaultDataDir()
	}

	// Write to a temporary neighbour and rename, so an interrupted backup
	// never leaves a plausible-looking short file where a good one was.
	tmp := out + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := node.WriteBackup(dataDir, []byte(pass), f); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, out); err != nil {
		os.Remove(tmp)
		return err
	}
	fi, _ := os.Stat(out)
	fmt.Printf("backup written: %s", out)
	if fi != nil {
		fmt.Printf(" (%.1f MiB)", float64(fi.Size())/(1<<20))
	}
	fmt.Println()
	fmt.Println("keep it somewhere the machine it came from cannot take with it")
	fmt.Println("honesty note: nobody can open this without the backup passphrase, " +
		"and nobody can reset it for you")
	return nil
}

// runRestore unpacks a backup into a fresh directory.
//
// Deliberately a separate command rather than something the running node can
// do to itself: a node cannot restore into the directory it currently holds
// open, and pretending otherwise would mean merging two histories.
func runRestore(args []string) error {
	flags := parseFlags(args)
	in, pass, dataDir := flags["in"], flags["passphrase"], flags["data"]
	if pass == "" {
		pass = os.Getenv("QP_BACKUP_PASSPHRASE")
	}
	if in == "" || pass == "" || dataDir == "" {
		return errors.New(backupUsage)
	}
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := node.ReadBackup(dataDir, []byte(pass), f); err != nil {
		return err
	}
	st := node.Inspect(dataDir)
	fmt.Println("restored into", dataDir)
	fmt.Printf("identity present: %v\n", st.HasIdentity)
	fmt.Println("start it with: terminal ui --data", dataDir)
	return nil
}
