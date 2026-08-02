// Command quiet-radio checks that a Meshtastic node is configured for the
// segment it is supposed to join (RB-2).
//
// Two radios that disagree about region, modem preset or channel key are not
// broken and not out of range: they are on different air. Both transmit
// happily, neither hears anything, and nothing inside Quiet Spaces can tell
// that apart from "nobody is nearby". This command reads what the node is
// actually set to, compares it with the segment's profile, and says exactly
// which field is wrong and what to type.
//
//	quiet-radio --radio tcp:192.168.1.50                     # what is this node set to?
//	quiet-radio --radio serial:/dev/ttyUSB0 --profile beta.profile
//	quiet-radio --radio tcp:192.168.1.50 --save-profile beta.profile
//	quiet-radio --radio tcp:192.168.1.50 --raw               # what does the node send?
//
// Exit codes: 0 everything checked matched · 1 something disagreed ·
// 3 nothing disagreed but something could not be verified · 2 usage or
// connection failure. The 1/3 split is deliberate — "this radio is wrong"
// and "this radio did not tell me" call for different actions.
//
// No key is ever printed. Profiles carry a fingerprint, which identifies a
// key without revealing it, so a profile is a plain file that can travel
// with the beta package.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

const usage = `usage: quiet-radio --radio tcp:HOST[:PORT]|serial:/dev/PATH
                   [--profile FILE] [--save-profile FILE] [--raw]
                   [--snapshot | --diff | --restore] [--store FILE]

  --profile FILE       check this node against a segment profile
  --save-profile FILE  capture a profile from this (correctly configured) node
  --channel N          which channel index to capture (default: the PRIMARY,
                       which on most radios is the PUBLIC default-key channel —
                       pass your own channel's index instead)
  --raw                also list the message types the node sent

  --snapshot           record what this radio holds now, as the known-good state
  --diff               name every setting that has moved since the snapshot
  --restore            put the snapshot back on the radio (asks first)
  --store FILE         where snapshots live (default: the data directory,
                       not the current one — a recovery record must be
                       findable from wherever you happen to be standing)

A profile says what a SEGMENT requires and travels between people. A
snapshot says what ONE DEVICE held at one moment and belongs to that
device — it is what answers "what was this set to before?", which is the
first question after a write goes wrong and the one nothing could answer
when a bad write muted two boards. Neither file contains a channel key.`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := parseFlags(args)
	target := flags["radio"]
	if target == "" || flags["help"] != "" {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}

	radio, err := dial(target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "radio:", err)
		return 2
	}
	defer radio.Close()

	cfg := radio.Config()
	fmt.Print(cfg.Report())

	if flags["raw"] != "" {
		printRaw(cfg)
	}

	if flags["snapshot"] != "" || flags["diff"] != "" || flags["restore"] != "" {
		return runSnapshot(flags, radio, cfg)
	}

	if path := flags["save-profile"]; path != "" {
		// A segment of one's own is usually a SECONDARY channel; the primary
		// is normally the public default-key one. Capturing the wrong channel
		// produces a profile that verifies clean while two radios talk past
		// each other.
		index := -1
		if c := flags["channel"]; c != "" {
			if _, err := fmt.Sscanf(c, "%d", &index); err != nil || index < 0 || index > 7 {
				fmt.Fprintf(os.Stderr, "--channel must be 0..7, got %q\n", c)
				return 2
			}
		}
		p, err := meshtastic.ProfileFromChannel(profileName(flags, path), cfg, index)
		if err != nil {
			fmt.Fprintln(os.Stderr, "\n"+err.Error())
			return 2
		}
		if err := os.WriteFile(path, []byte(p.Format()), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "write profile:", err)
			return 2
		}
		fmt.Printf("\nprofile captured to %s\n", path)
		return 0
	}

	path := flags["profile"]
	if path == "" {
		// Nothing to compare against. Reporting the configuration is still
		// the useful half: it is what someone reads out to whoever set the
		// segment up.
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "profile:", err)
		return 2
	}
	p, err := meshtastic.ParseProfile(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "profile:", err)
		return 2
	}

	verdict := p.Check(cfg)
	name := p.Name
	if name == "" {
		name = path
	}
	fmt.Printf("\nchecked against %s:\n%s", name, verdict.Report())

	switch {
	case verdict.Failed():
		fmt.Println("\nThis node is not on the same air as the segment. Apply " +
			"the fixes above,\nthen run this command again — the node reports " +
			"its new configuration immediately.")
		return 1
	case verdict.Incomplete():
		fmt.Println("\nEverything this node reported agrees with the segment, " +
			"but it did not report\neverything the profile asks about. Compare " +
			"the rest by hand with `meshtastic --info`.")
		return 3
	}
	fmt.Println("\nThis node is configured for the segment.")
	return 0
}

func dial(target string) (*meshtastic.Radio, error) {
	return meshtastic.Open(target, meshtastic.Options{})
}

// printRaw lists the FromRadio members this build skipped. The protobuf
// subset here is hand-transcribed, and a wrong field number would otherwise
// be invisible — it would look like a node that simply reported nothing.
// This is how real hardware corrects us.
func printRaw(cfg meshtastic.NodeConfig) {
	if len(cfg.Unrecognised) == 0 {
		fmt.Println("\nthe node sent nothing this build does not read")
		return
	}
	fields := make([]int, 0, len(cfg.Unrecognised))
	for f := range cfg.Unrecognised {
		fields = append(fields, f)
	}
	sort.Ints(fields)
	fmt.Println("\nFromRadio members this build skipped (field number ×count):")
	for _, f := range fields {
		fmt.Printf("  %d ×%d%s\n", f, cfg.Unrecognised[f], knownAs(f))
	}
}

// knownAs labels the FromRadio members we deliberately do not read, so the
// raw dump distinguishes "expected to skip this" from "something new".
func knownAs(field int) string {
	switch field {
	case 1:
		return "  (id)"
	case 4:
		return "  (node_info — other nodes on the mesh)"
	case 6:
		return "  (log_record)"
	case 8:
		return "  (rebooted)"
	case 9:
		return "  (moduleConfig)"
	case 11:
		return "  (queueStatus)"
	case 12:
		return "  (xmodemPacket)"
	case 14:
		return "  (mqttClientProxyMessage)"
	case 15:
		return "  (fileInfo)"
	case 16:
		return "  (clientNotification)"
	case 17:
		return "  (deviceuiConfig)"
	}
	return "  (unknown to this build — please report)"
}

func profileName(flags map[string]string, path string) string {
	if n := flags["name"]; n != "" {
		return n
	}
	base := path
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	return strings.TrimSuffix(base, ".profile")
}

func parseFlags(args []string) map[string]string {
	out := map[string]string{}
	for i := 0; i < len(args); i++ {
		if len(args[i]) <= 2 || args[i][:2] != "--" {
			continue
		}
		name := args[i][2:]
		if i+1 >= len(args) || (len(args[i+1]) > 2 && args[i+1][:2] == "--") {
			out[name] = "1"
			continue
		}
		out[name] = args[i+1]
		i++
	}
	return out
}
