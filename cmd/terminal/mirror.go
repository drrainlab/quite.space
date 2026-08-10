// `terminal mirror` volunteers this node to keep a public space reachable
// (PH-3).
//
// A public space lives at the relay in 48-hour windows, refreshed by its
// owner's heartbeat. When the owner is offline for longer, the space stops
// existing for anyone who has not already read it. A mirror republishes the
// owner's signed envelope verbatim and holds the media it references, so
// the space survives its owner's absence.
//
// Mirroring grants nothing. This node has no space key: it cannot write,
// re-sign, or alter a byte, and it never drains anyone's mailbox. It adds
// availability, and that is all it adds.
//
// Unlike `terminal custodian`, these commands DO need the passphrase: they
// change what this node does on the network and are recorded in the
// keystore, not in a file of public keys.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/drrainlab/quiet_places/node"
	"github.com/drrainlab/quiet_places/protocol/id"
)

const mirrorUsage = `usage:
  terminal mirror add    --link SHARE_LINK [--data DIR] --passphrase P [--seed]
  terminal mirror list                     [--data DIR] --passphrase P
  terminal mirror remove --space HEX       [--data DIR] --passphrase P

  add     open a public space and keep it reachable from this node
  list    show which spaces this node mirrors, and how much media it holds
  remove  stop mirroring (nothing already downloaded is deleted)

  --seed  also answer other readers' media requests using blobs already
          held. Off by default: seeding tells others you have the file, and
          therefore that you read this space.

After 'add', run the node normally (terminal node --data DIR --passphrase P)
— the sync loop does the rest.`

func runMirror(args []string) error {
	if len(args) == 0 {
		return errors.New(mirrorUsage)
	}
	sub, rest := args[0], args[1:]
	flags := parseKV(rest)
	dataDir := flags["data"]
	if dataDir == "" {
		dataDir = node.DefaultDataDir()
	}
	pass := flags["passphrase"]
	if pass == "" {
		// THE SAME WAY THE NODE TAKES IT, and for a sharper reason here: this
		// is the command an operator runs on a server, and an argv is world
		// readable. Anybody with a shell on the box can read /proc and see a
		// passphrase for as long as the process lives — and systemd prints
		// the command line of a service to anyone who asks its status.
		pass = os.Getenv("QP_PASSPHRASE")
	}
	if pass == "" {
		return errors.New("mirror: pass --passphrase or set QP_PASSPHRASE " +
			"(this changes node state)")
	}

	rt, err := node.Open(dataDir, []byte(pass), "")
	if err != nil {
		return err
	}
	defer rt.Close()

	switch sub {
	case "add":
		link := strings.TrimSpace(flags["link"])
		if link == "" {
			return errors.New("mirror add: --link is required (the space's share link)")
		}
		tid, err := rt.OpenPublicLink(link)
		if err != nil {
			return err
		}
		if err := rt.SetMirror(tid, true); err != nil {
			return err
		}
		if _, ok := flags["seed"]; ok {
			if err := rt.SetSeed(tid, true); err != nil {
				return err
			}
		}
		fmt.Printf("mirroring %s\n", tid.Hex())
		fmt.Println("run the node to start keeping it reachable")
		return nil

	case "list":
		rows := rt.MirroredSpaces()
		if len(rows) == 0 {
			fmt.Println("this node mirrors nothing")
			return nil
		}
		for _, m := range rows {
			seed := ""
			if m.Seed {
				seed = "  seeding"
			}
			fmt.Printf("%s  %s  media %d/%d%s\n",
				m.SpaceID.Hex()[:16], m.Title, m.AssetsHeld, m.AssetsTotal, seed)
		}
		return nil

	case "remove":
		tid, err := id.ParseTerminalID(strings.TrimSpace(flags["space"]))
		if err != nil {
			return errors.New("mirror remove: --space must be a terminal id")
		}
		if err := rt.SetMirror(tid, false); err != nil {
			return err
		}
		if err := rt.SetSeed(tid, false); err != nil {
			return err
		}
		fmt.Printf("no longer mirroring %s\n", tid.Hex())
		fmt.Println("already downloaded content stays on this node")
		return nil
	}
	return errors.New(mirrorUsage)
}
