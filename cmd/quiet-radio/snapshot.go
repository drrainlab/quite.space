package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

const defaultStore = "radio-config.json"

// runSnapshot handles --snapshot, --diff and --restore.
//
// The three are one feature seen from three moments: before a change, after
// one, and after a change that went wrong. Nothing here writes to the radio
// except --restore, and that asks first.
func runSnapshot(flags map[string]string, radio *meshtastic.Radio,
	cfg meshtastic.NodeConfig) int {
	path := flags["store"]
	if path == "" || path == "1" {
		path = defaultStore
	}
	file, err := loadStore(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "snapshots:", err)
		return 2
	}
	key := meshtastic.SnapshotKey(cfg.NodeNum)

	if flags["snapshot"] != "" {
		if cfg.LoRaRaw == nil && cfg.DeviceRaw == nil {
			fmt.Fprintln(os.Stderr, "\nthis node reported no configuration, so "+
				"there is nothing to record. A snapshot of nothing would later "+
				"read as a radio that held nothing.")
			return 2
		}
		file.Radios[key] = meshtastic.SnapshotOf(cfg, time.Now())
		if err := saveStore(path, file); err != nil {
			fmt.Fprintln(os.Stderr, "snapshots:", err)
			return 2
		}
		fmt.Printf("\nrecorded %s as the known-good state of radio %s\n", path, key)
		fmt.Println("No channel key is in that file — channel settings are a " +
			"different message and are not captured.")
		return 0
	}

	snap, ok := file.Radios[key]
	if !ok {
		fmt.Fprintf(os.Stderr, "\nno snapshot of radio %s in %s. Take one while "+
			"the radio is set up the way you want it:\n  quiet-radio --radio %s "+
			"--snapshot\n", key, path, flags["radio"])
		return 2
	}
	saved, err := snap.Config()
	if err != nil {
		fmt.Fprintln(os.Stderr, "snapshots:", err)
		return 2
	}
	diff, err := meshtastic.DiffConfig(saved, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "snapshots:", err)
		return 2
	}

	if flags["diff"] != "" {
		if len(diff) == 0 {
			fmt.Printf("\nradio %s holds exactly what was saved at %s\n",
				key, snap.TakenAt)
			return 0
		}
		fmt.Printf("\nsince the snapshot at %s, %d setting(s) moved:\n",
			snap.TakenAt, len(diff))
		for _, m := range diff {
			fmt.Println("  · " + m.String())
		}
		fmt.Println("\nPut them back with --restore, or take a new snapshot with " +
			"--snapshot if this is how the radio should be.")
		return 1
	}

	// --restore
	if len(diff) == 0 {
		fmt.Printf("\nradio %s already holds what was saved at %s — nothing to do\n",
			key, snap.TakenAt)
		return 0
	}
	plan, cannot, err := meshtastic.PlanRestore(cfg, snap)
	if err != nil {
		fmt.Fprintln(os.Stderr, "restore:", err)
		return 2
	}
	fmt.Printf("\nrestoring radio %s to its state at %s:\n", key, snap.TakenAt)
	for _, m := range diff {
		fmt.Println("  · " + m.String())
	}
	if len(cannot) > 0 {
		// Said before the confirmation, not after: a person agreeing to a
		// restore is entitled to know which part of it will not happen.
		fmt.Println("\n⚠  These will NOT be restored:")
		for _, c := range cannot {
			fmt.Println("   · " + c)
		}
	}
	if plan == nil {
		fmt.Println("\nNothing here can be written by this build.")
		return 1
	}
	fmt.Println("\n⚠  Transmitting on the wrong frequencies for where you are can " +
		"be illegal.\n   Read the list above before agreeing to it.")
	if !confirm("Write this?") {
		fmt.Println("stopped — nothing was written")
		return 2
	}
	if err := radio.Apply(plan); err != nil {
		fmt.Fprintln(os.Stderr, "restore:", err)
		return 2
	}
	fmt.Println("\nwritten. The radio is rebooting; run this command again to " +
		"see what it now holds.")
	if len(cannot) > 0 {
		return 3 // something was left unrestored, and the caller should know
	}
	return 0
}

func loadStore(path string) (meshtastic.SnapshotFile, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return meshtastic.SnapshotFile{}, err
	}
	return meshtastic.DecodeSnapshotFile(data)
}

// saveStore writes through a temporary file in the same directory, so an
// interrupted write leaves the previous snapshot intact rather than a
// half-written one. Losing the record of what a radio held is exactly the
// situation this file exists to prevent.
func saveStore(path string, f meshtastic.SnapshotFile) error {
	data, err := f.Encode()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".radio-config-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func confirm(prompt string) bool {
	fmt.Print(prompt + " type yes to continue: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(line), "yes")
}
