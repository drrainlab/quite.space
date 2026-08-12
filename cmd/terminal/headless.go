// TR-0a: the headless verbs. A terminal on a server is configured the way
// the mirror is configured — a short-lived command that opens the runtime,
// changes one thing, and exits; the long-lived work belongs to `terminal
// node`. Nothing here speaks HTTP and nothing here needs the UI: every verb
// is a thin wrapper over the same Runtime calls the API handlers make.
package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/node"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// openForCLI resolves dataDir and passphrase exactly the way mirror does —
// never the passphrase on argv on a server; QP_PASSPHRASE is the documented
// deployment shape (docs/guide/SELF_HOSTING.md).
func openForCLI(flags map[string]string, why string) (*node.Runtime, error) {
	dataDir := flags["data"]
	if dataDir == "" {
		dataDir = node.DefaultDataDir()
	}
	pass := flags["passphrase"]
	if pass == "" {
		pass = os.Getenv("QP_PASSPHRASE")
	}
	if pass == "" {
		return nil, errors.New(why + ": pass --passphrase or set QP_PASSPHRASE")
	}
	return node.Open(dataDir, []byte(pass), flags["name"])
}

// runJoin walks a pass/quick link to membership and WAITS for the outcome,
// because a headless operator has no screen where "pending" resolves later:
// the command's exit code is the answer. The join saga is durable either
// way — a timeout here means "still waiting", never "lost", and running
// `terminal node` lets the saga finish on its own.
func runJoin(args []string) error {
	flags := parseKV(args)
	link := strings.TrimSpace(flags["link"])
	if link == "" {
		return errors.New("join: --link is required (a pass or quick link)")
	}
	waitSec := 90
	if w := flags["wait"]; w != "" {
		n, err := strconv.Atoi(w)
		if err != nil || n < 1 {
			return errors.New("join: --wait wants seconds")
		}
		waitSec = n
	}
	rt, err := openForCLI(flags, "join")
	if err != nil {
		return err
	}
	defer rt.Close()

	req, err := rt.JoinByPass(link)
	if err != nil {
		return err
	}
	fmt.Printf("request %s sent; waiting for the host…\n", req)
	deadline := time.Now().Add(time.Duration(waitSec) * time.Second)
	for time.Now().Before(deadline) {
		state, space := rt.JoinStatus(req)
		switch state {
		case node.JoinReady:
			fmt.Printf("joined %s\n", space)
			return nil
		case node.JoinDeclined, node.JoinExpired, node.JoinExpiredWaiting,
			node.JoinSegmentEnded:
			return fmt.Errorf("join: %s", state)
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Not a failure: the request is journaled (ADR-012) and `terminal node`
	// keeps polling it. The operator is told the truth, not an error.
	fmt.Println("still waiting when --wait ran out; the request survives —")
	fmt.Println("run `terminal node` (or `terminal join` again) to keep waiting")
	return nil
}

// runSay is the dev/proof verb behind gate TR-0a: publish one message
// headlessly and stay alive just long enough for the sync loop to hand it
// to the relay. It reports the hand-off honestly instead of pretending exit
// means delivery.
func runSay(args []string) error {
	flags := parseKV(args)
	spaceHex := strings.TrimSpace(flags["space"])
	text := flags["text"]
	if spaceHex == "" || text == "" {
		return errors.New("say: --space HEX and --text are required")
	}
	tid, err := id.ParseTerminalID(spaceHex)
	if err != nil {
		return fmt.Errorf("say: bad space id: %w", err)
	}
	rt, err := openForCLI(flags, "say")
	if err != nil {
		return err
	}
	defer rt.Close()

	eid, err := rt.Say(tid, text, node.SayOptions{})
	if err != nil {
		return err
	}
	fmt.Printf("emitted %s\n", eid.Hex()[:16])
	// Give the resumed sync loop a few cycles to push, then say where the
	// hand-off stands. The log is durable regardless.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st := rt.RelaySync()
		if st.Pushed > 0 && len(st.Held) == 0 {
			fmt.Println("handed to the relay")
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	st := rt.RelaySync()
	if len(st.Held) > 0 {
		fmt.Printf("held: %s — the event is safe in the log and will be re-offered\n",
			st.Held[0].Reason)
	} else {
		fmt.Println("hand-off not confirmed within 15s — the event is safe in the log")
	}
	return nil
}

// runStatus is one honest page for an operator with a shell: identity,
// spaces, the relay picture, and holds — the same facts the diagnostics API
// serves, without HTTP. It opens the runtime briefly, so it refuses cleanly
// while `terminal node` holds the data-dir lock.
func runStatus(args []string) error {
	flags := parseKV(args)
	rt, err := openForCLI(flags, "status")
	if err != nil {
		return err
	}
	defer rt.Close()

	fmt.Printf("Identity   %s\n", rt.Principal.Fingerprint())
	fmt.Printf("Device     %s\n", rt.Device.ID.Hex()[:16])

	spaces := rt.Spaces()
	sort.Slice(spaces, func(i, j int) bool { return spaces[i].Title < spaces[j].Title })
	fmt.Printf("\nSpaces (%d)\n", len(spaces))
	for _, s := range spaces {
		line := fmt.Sprintf("  %s  %s  msgs %d", s.ID.Hex()[:16], s.Title, s.Messages)
		if s.Undecryptable > 0 {
			line += fmt.Sprintf("  undecryptable %d", s.Undecryptable)
		}
		fmt.Println(line)
	}

	d := rt.RelayDiagnosticsSnapshot()
	fmt.Printf("\nRelay      mode %s", d.Mode)
	if d.Primary != "" {
		fmt.Printf("  primary %s (%s)", d.Primary, d.PrimaryHealth)
	}
	fmt.Println()
	if len(d.Ingress) > 0 {
		fmt.Printf("Ingress    %s\n", strings.Join(d.Ingress, ", "))
	}
	if len(d.LocalPeers) > 0 {
		fmt.Printf("Local      %s\n", strings.Join(d.LocalPeers, ", "))
	}
	if d.NoRoute > 0 {
		fmt.Printf("NoRoute    %d peer device(s) currently unreachable\n", d.NoRoute)
	}
	sync := rt.RelaySync()
	for _, h := range sync.Held {
		fmt.Printf("Held       %s — %s (%d frames)\n", h.Title, h.Reason, h.Frames)
	}

	if conns := rt.ConnectorIDs(); len(conns) > 0 {
		fmt.Printf("\nConnectors\n")
		for _, cid := range conns {
			st, err := rt.ConnectorStatus(cid)
			if err != nil {
				fmt.Printf("  %s  (unreadable: %v)\n", cid, err)
				continue
			}
			target := st.Target
			if target == "" {
				target = "— no route"
			} else {
				target = target[:16]
			}
			fmt.Printf("  %s → %s  gen %d  pending %d  published %d  refused %d  orphaned %d\n",
				cid, target, st.Binding, st.Pending, st.Published, st.Refused, st.Orphaned)
		}
	}
	return nil
}

// runRoute rebinds a connector: `terminal route set --connector C --space
// HEX`. A temporal boundary, said plainly — the old space keeps what it
// received and loses the connector; pending never crosses.
func runRoute(args []string) error {
	if len(args) == 0 || args[0] != "set" {
		return errors.New("route: usage: terminal route set --connector C --space HEX")
	}
	flags := parseKV(args[1:])
	conn := strings.TrimSpace(flags["connector"])
	spaceHex := strings.TrimSpace(flags["space"])
	if conn == "" || spaceHex == "" {
		return errors.New("route set: --connector and --space are required")
	}
	tid, err := id.ParseTerminalID(spaceHex)
	if err != nil {
		return fmt.Errorf("route set: bad space id: %w", err)
	}
	rt, err := openForCLI(flags, "route")
	if err != nil {
		return err
	}
	defer rt.Close()
	gen, err := rt.ConnectorRoute(conn, tid)
	if err != nil {
		return err
	}
	fmt.Printf("binding %d: %s → %s\n", gen, conn, spaceHex[:16])
	fmt.Println("from this moment; earlier and pending ingress stays with its own binding")
	return nil
}
