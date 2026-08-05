// Command terminal is the M0 CLI: identity management, bundle inspection,
// and the reproducible Protocol Seed demo (plan Phase 0 proof: two headless
// nodes create signed events and synchronize over a file and a local link).
package main

import (
	"fmt"
	"os"

	"github.com/drrainlab/quiet_places/kernel/identity"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "demo":
		err = runDemo()
	case "ui":
		err = runUI(os.Args[2:], true)
	case "node":
		err = runUI(os.Args[2:], false)
	case "identity":
		err = runIdentity(os.Args[2:])
	case "inspect":
		err = runInspect(os.Args[2:])
	case "meshhub":
		err = runMeshHub(os.Args[2:])
	case "radio":
		err = runRadio(os.Args[2:])
	case "mirror":
		err = runMirror(os.Args[2:])
	case "custodian":
		err = runCustodian(os.Args[2:])
	case "relay":
		err = runRelayTrust(os.Args[2:])
	case "backup":
		err = runBackup(os.Args[2:])
	case "restore":
		err = runRestore(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `terminal — Quiet Places / Terminal Network M0 CLI

usage:
  terminal ui   --passphrase P [--data DIR] [--name N] [--no-browser] [--no-lan]
                [--rnode /dev/PATH --mesh-seed "segment phrase"]
                [--mesh tcp:HOST[:PORT] | --mesh serial:/dev/PATH]
                [--radio-profile FILE] [--mesh-network ID] [--mesh-channel N]
                                         run the node and open the web UI
                                         --rnode is the proven radio: an RNode
                                         modem, and it needs --mesh-seed.
                                         --mesh is Meshtastic, experimental.
                                         See docs/radio/
  terminal node --passphrase P [--data DIR]
                                         run headless (API only, no UI assets)
  terminal demo                          run the reproducible two-node demo
  terminal backup  --out FILE --passphrase P [--data DIR]
                                         encrypted copy of EVERYTHING:
                                         identity, spaces, epoch keys, history
                                         (safe while the node is running)
  terminal restore --in FILE --passphrase P --data DIR
                                         unpack a backup into an EMPTY dir
  terminal identity new --passphrase P --out FILE
                                         create an identity, export encrypted
                                         recovery bundle, print fingerprint
                                         (identity ONLY — for everything else
                                         use 'terminal backup')
  terminal identity restore --passphrase P --in FILE
                                         restore an identity from a bundle
  terminal inspect --bundle FILE         list frames in a *.terminal-bundle
  terminal radio list                    what is on each serial port
  terminal radio flash [--port PATH]     install Meshtastic on a board (ERASES it)
  terminal meshhub [--listen ADDR]       run a fake Meshtastic mesh (dev tool):
                                         point nodes at it with --mesh tcp:ADDR
  terminal mirror add|list|remove        keep a public space reachable from
                                         this node (no authority over it)
  terminal custodian list|pin|unpin      which gateways this node trusts
                                         (public keys; no passphrase needed)
  terminal relay show-identity|trust|forget
                                         confirm a custom relay's SPKI pin
                                         (no passphrase needed)
`)
}

func runIdentity(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("identity: need subcommand new|restore")
	}
	flags := parseFlags(args[1:])
	switch args[0] {
	case "new":
		pass, out := flags["passphrase"], flags["out"]
		if pass == "" || out == "" {
			return fmt.Errorf("identity new: --passphrase and --out required")
		}
		p, err := identity.NewPrincipal(identity.NewRand())
		if err != nil {
			return err
		}
		dev, err := identity.NewDevice(identity.NewRand())
		if err != nil {
			return err
		}
		cert := p.Certify(dev, 1, 0)
		bundle, err := identity.ExportRecoveryBundle(p, []*identity.Certificate{cert}, []byte(pass))
		if err != nil {
			return err
		}
		if err := os.WriteFile(out, bundle, 0o600); err != nil {
			return err
		}
		fmt.Println("identity created — no registration, no server, no username")
		fmt.Println("principal fingerprint:", p.Fingerprint())
		fmt.Println("recovery bundle:", out)
		fmt.Println("honesty note: lose all devices AND this bundle and the identity is unrecoverable; nobody can reset it for you")
		return nil
	case "restore":
		pass, in := flags["passphrase"], flags["in"]
		if pass == "" || in == "" {
			return fmt.Errorf("identity restore: --passphrase and --in required")
		}
		data, err := os.ReadFile(in)
		if err != nil {
			return err
		}
		p, certs, err := identity.ImportRecoveryBundle(data, []byte(pass))
		if err != nil {
			return err
		}
		fmt.Println("identity restored")
		fmt.Println("principal fingerprint:", p.Fingerprint())
		fmt.Printf("device certificates: %d\n", len(certs))
		return nil
	default:
		return fmt.Errorf("identity: unknown subcommand %q", args[0])
	}
}

func parseFlags(args []string) map[string]string {
	out := map[string]string{}
	for i := 0; i < len(args); i++ {
		if len(args[i]) <= 2 || args[i][:2] != "--" {
			continue
		}
		name := args[i][2:]
		// A flag followed by another flag (or nothing) is boolean.
		if i+1 >= len(args) || (len(args[i+1]) > 2 && args[i+1][:2] == "--") {
			out[name] = "1"
			continue
		}
		out[name] = args[i+1]
		i++
	}
	return out
}
