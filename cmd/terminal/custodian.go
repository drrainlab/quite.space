// `terminal custodian` manages which gateways this node trusts (ADR-015 §7).
//
// A gateway proves it took custody of a message by signing a receipt. That
// signature counts for nothing unless the key is PINNED here: trust on first
// use is forbidden, because a gateway is exactly the position an attacker
// would want, and "the first key I saw" is not a decision anyone made.
//
// These commands work on the pin file directly — no passphrase, and no need
// to stop a running node. That is possible because the file holds public
// keys only. Nothing in it is a secret; it is plain text on purpose, so a
// person can read their own trust decisions back and check them by eye.
package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/node"
)

func runCustodian(args []string) error {
	if len(args) == 0 {
		return errors.New(custodianUsage)
	}
	sub, rest := args[0], args[1:]
	flags := parseKV(rest)
	dataDir := flags["data"]
	if dataDir == "" {
		dataDir = "./quiet-data"
	}
	path := node.CustodianPath(dataDir)

	pins, warn, err := node.LoadCustodianFile(path)
	if err != nil {
		return fmt.Errorf("read pins: %w", err)
	}
	for _, w := range warn {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}

	switch sub {
	case "list":
		return listCustodians(pins, path)
	case "pin":
		return pinCustodian(path, pins, flags)
	case "unpin":
		return unpinCustodian(path, pins, flags)
	}
	return errors.New(custodianUsage)
}

const custodianUsage = `usage:
  terminal custodian list  [--data DIR]
  terminal custodian pin   [--data DIR] --link DOMAIN --key HEX [--label TEXT]
  terminal custodian unpin [--data DIR] --link DOMAIN

DOMAIN is the link a receipt must arrive on for the key to count:
"radio", "lan", or "relay:HOST". The key is the 64-character hex the
gateway prints at startup ("custodian public key (pin on nodes)").

Pins are public keys. There is no passphrase and no secret here, and a
running node picks up changes from its Gateway screen or on restart.`

func listCustodians(pins []node.CustodianPin, path string) error {
	if len(pins) == 0 {
		fmt.Printf("no gateways are trusted (%s)\n\n", path)
		fmt.Println("Until a gateway is pinned, its custody receipts prove nothing:")
		fmt.Println("messages still travel, but nothing can confirm anyone took them on.")
		return nil
	}
	fmt.Printf("%s\n\n", path)
	for _, p := range pins {
		fmt.Printf("  %-14s %s\n", p.LinkDomain, p.Fingerprint())
		fmt.Printf("  %-14s %x\n", "", p.Key)
		if p.Label != "" {
			fmt.Printf("  %-14s %s\n", "", p.Label)
		}
		when := "date unknown"
		if p.PinnedAt.After(time.Unix(100, 0)) {
			when = "pinned " + p.PinnedAt.Format("2006-01-02 15:04")
		}
		if p.Replaced != "" {
			when += ", replacing " + p.Replaced
		}
		fmt.Printf("  %-14s %s\n\n", "", when)
	}
	return nil
}

func pinCustodian(path string, pins []node.CustodianPin, flags map[string]string) error {
	domain := flags["link"]
	if domain == "" {
		return errors.New("--link is required (radio, lan, relay:HOST)")
	}
	if strings.ContainsAny(domain, " \t") {
		return errors.New("a link domain cannot contain spaces")
	}
	raw, err := hex.DecodeString(strings.TrimSpace(flags["key"]))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return errors.New("--key must be 64 hex characters — the value the " +
			"gateway prints as `custodian public key (pin on nodes)`")
	}

	pin := node.CustodianPin{
		LinkDomain: domain,
		Key:        ed25519.PublicKey(raw),
		Label:      flags["label"],
		PinnedAt:   time.Now(),
	}
	out := make([]node.CustodianPin, 0, len(pins)+1)
	replacedFP := ""
	for _, p := range pins {
		if p.LinkDomain == domain {
			if string(p.Key) == string(pin.Key) {
				fmt.Printf("%s is already pinned to %s — nothing changed\n",
					domain, pin.Fingerprint())
				return nil
			}
			// Manual rotation: the operator changed the gateway's key and
			// told this person the new one. Allowed, and recorded — a trust
			// decision that changes with no trace is indistinguishable from
			// one that changed by itself.
			replacedFP = p.Fingerprint()
			pin.Replaced = replacedFP
			if pin.Label == "" {
				pin.Label = p.Label
			}
			continue
		}
		out = append(out, p)
	}
	out = append(out, pin)
	if err := node.SaveCustodianFile(path, out); err != nil {
		return err
	}
	if replacedFP != "" {
		fmt.Printf("REPLACED the pin for %s: %s is no longer trusted, %s is.\n",
			domain, replacedFP, pin.Fingerprint())
		fmt.Println("If you did not expect to replace a key, check with whoever " +
			"runs the gateway\nbefore sending anything you would not want mishandled.")
		return nil
	}
	fmt.Printf("pinned %s for %s\n", pin.Fingerprint(), domain)
	return nil
}

func unpinCustodian(path string, pins []node.CustodianPin, flags map[string]string) error {
	domain := flags["link"]
	if domain == "" {
		return errors.New("--link is required")
	}
	out := make([]node.CustodianPin, 0, len(pins))
	found := false
	for _, p := range pins {
		if p.LinkDomain == domain {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		return fmt.Errorf("no gateway is pinned for %q", domain)
	}
	if err := node.SaveCustodianFile(path, out); err != nil {
		return err
	}
	fmt.Printf("unpinned %s — receipts arriving on that link now prove nothing\n", domain)
	return nil
}

// parseKV reads --key value pairs, matching the other commands here.
func parseKV(args []string) map[string]string {
	out := map[string]string{}
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "--") {
			continue
		}
		name := strings.TrimPrefix(args[i], "--")
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
			out[name] = "1"
			continue
		}
		out[name] = args[i+1]
		i++
	}
	return out
}
