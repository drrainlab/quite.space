// `terminal relay …` — the headless half of custom-relay trust (RR-1).
// A custom relay's identity is confirmed by a PERSON comparing the
// fingerprint with the operator; there is deliberately no
// --accept-any-certificate. Runs without a passphrase: relays.json holds
// pins and measurements, never secrets.
package main

import (
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/node"
)

func runRelayTrust(args []string) error {
	if len(args) < 1 {
		return errors.New(`usage:
  terminal relay show-identity <endpoint>            print the relay's SPKI fingerprint
  terminal relay trust <endpoint> <fingerprint>      confirm and pin it [--data DIR]
  terminal relay forget <endpoint>                   drop the pin        [--data DIR]`)
	}
	dataDir := node.DefaultDataDir()
	rest := args[1:]
	var pos []string
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--data" && i+1 < len(rest) {
			dataDir = rest[i+1]
			i++
			continue
		}
		pos = append(pos, rest[i])
	}
	switch args[0] {
	case "show-identity":
		if len(pos) != 1 {
			return errors.New("usage: terminal relay show-identity <endpoint>")
		}
		pin, err := node.RelayIdentity(pos[0])
		if err != nil {
			return err
		}
		fmt.Printf("relay %s\nSPKI pin: %s\n", pos[0], pin)
		fmt.Println("verify this fingerprint with the relay operator before trusting it")
		return nil
	case "trust":
		if len(pos) != 2 {
			return errors.New("usage: terminal relay trust <endpoint> <fingerprint> [--data DIR]")
		}
		if err := node.TrustRelayAt(dataDir, pos[0], pos[1]); err != nil {
			return err
		}
		fmt.Printf("pinned %s\nfrom now on a different key at this address is a hard failure\n", pos[0])
		return nil
	case "forget":
		if len(pos) != 1 {
			return errors.New("usage: terminal relay forget <endpoint> [--data DIR]")
		}
		if err := node.ForgetRelayAt(dataDir, pos[0]); err != nil {
			return err
		}
		fmt.Printf("forgot %s\n", pos[0])
		return nil
	}
	return fmt.Errorf("terminal relay: unknown action %q", args[0])
}
