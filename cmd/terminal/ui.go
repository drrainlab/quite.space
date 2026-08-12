package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/drrainlab/quiet_places/clients/lockgate"
	webui "github.com/drrainlab/quiet_places/clients/web-ui"
	"github.com/drrainlab/quiet_places/connector/email"
	"github.com/drrainlab/quiet_places/kernel/passcode"
	"github.com/drrainlab/quiet_places/node"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/lan"
	"github.com/drrainlab/quiet_places/transports/meshtastic"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
)

// runUI starts the node runtime, the local API, and (unless --no-browser)
// opens the embedded web UI. `terminal node` is the same runtime without
// UI assets — one binary, one behavior (ADR-011).
func runUI(args []string, withUI bool) error {
	flags := parseFlags(args)
	dataDir := flags["data"]
	if dataDir == "" {
		dataDir = node.DefaultDataDir()
	}
	pass := flags["passphrase"]
	if pass == "" {
		pass = os.Getenv("QP_PASSPHRASE")
	}

	// THE LOCK GATE. With no passphrase to hand and a passcode bound on this
	// device, the port is taken by a screen that asks for four digits, and
	// the node opens with whatever that unwraps. Nothing below this block
	// knows it happened: node.Open still receives a passphrase, exactly as
	// it always has.
	//
	// Backwards compatible in both directions — a passphrase on the command
	// line skips the gate entirely, and a device with no passcode bound
	// reaches the same error it always did.
	gateToken, gatePort := flags["token"], -1
	if pass == "" {
		if st, err := passcode.Info(dataDir); err == nil && st.Bound {
			l, gateAddr, p, err := lockgate.BindPort(parsePort(flags["port"]))
			if err != nil {
				return err
			}
			gatePort = p
			if gateToken == "" {
				if gateToken, err = lockgate.NewToken(); err != nil {
					return err
				}
			}
			url := "http://" + gateAddr
			fmt.Printf("locked · %d-digit code · open %s\n", st.Digits, url)
			if withUI && flags["no-browser"] == "" {
				openBrowser(url)
			}
			// The CLI enters the gate only for a bound code, so what comes
			// back is always an existing identity — the gate's first-run and
			// passphrase modes belong to the desktop shell, which has no
			// command line to pass a passphrase on.
			creds, err := lockgate.Unlock(l, dataDir, gateToken)
			if err != nil {
				return err
			}
			pass = string(creds.Passphrase)
		}
	}

	if pass == "" {
		return fmt.Errorf("pass --passphrase or set QP_PASSPHRASE (encrypts the local " +
			"keystore) — or bind a code to it with `terminal passcode set`")
	}
	name := flags["name"]
	if name == "" {
		name = "me"
	}

	rt, err := node.Open(dataDir, []byte(pass), name)
	if err != nil {
		return err
	}
	defer rt.Close()
	fmt.Println("node open — principal", rt.Principal.Fingerprint())
	fmt.Println("data root:", dataDir)

	if flags["no-lan"] == "" {
		if err := rt.StartLAN(":0", lan.MulticastAddr); err != nil {
			fmt.Println("LAN disabled:", err)
		} else {
			fmt.Printf("LAN: listening on :%d, announcing to %s\n", rt.LAN().Port, lan.MulticastAddr)
		}
	}
	// The segment's expected radio settings and its network id arrive with the
	// beta package, beside the gateway pin. Installed BEFORE the radio is
	// attached so the Gateway screen can judge the very first configuration
	// the node reports, rather than only the second.
	if path := flags["radio-profile"]; path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("radio profile: %w", err)
		}
		p, err := meshtastic.ParseProfile(data)
		if err != nil {
			return fmt.Errorf("radio profile %s: %w", path, err)
		}
		rt.SetRadioProfile(&p)
		fmt.Printf("radio profile %q loaded — the Gateway screen will name any "+
			"field that does not match\n", p.Name)
	}
	// Channel 0 is the radio's PRIMARY channel, which on a real device is
	// usually the public default-key one shared with everyone in range.
	// Selecting a dedicated channel keeps a segment off other people's air.
	if ch := flags["mesh-channel"]; ch != "" {
		var n uint32
		if _, err := fmt.Sscanf(ch, "%d", &n); err != nil || n > 7 {
			return fmt.Errorf("--mesh-channel must be 0..7, got %q", ch)
		}
		rt.SetMeshChannel(n)
	}
	if net := flags["mesh-network"]; net != "" {
		rt.SetMeshNetwork(net)
		fmt.Println("listening for gateway beacons on network", net)
	}

	if dev := flags["rnode"]; dev != "" {
		// RNode: the modem carrier, always under the Radio Transfer layer —
		// there is no raw wire to fall back to, because a bare modem has no
		// framing of its own worth speaking.
		seed := flags["mesh-seed"]
		if seed == "" {
			return errors.New("--rnode needs --mesh-seed: the segment seed is " +
				"what makes two radios a segment, and without it no frame " +
				"verifies")
		}
		if flags["mesh"] != "" {
			return errors.New("--rnode and --mesh are two radios: a node " +
				"attaches one")
		}
		// One derivation, shared with the attach path in the interface. A
		// phrase typed here and the same phrase typed there MUST land on the
		// same segment, and the only way to be sure of that is to have one
		// function.
		sum, err := radiotransfer.SeedFromPhrase(seed)
		if err != nil {
			return fmt.Errorf("--mesh-seed: %w", err)
		}
		if err := rt.StartRNodeTransfer(dev, sum); err != nil {
			fmt.Println("rnode: connection failed:", err)
		} else {
			fmt.Printf("rnode: attached via %s (radio-transfer wire, summaries "+
				"every 60s — LoRa airtime)\n", dev)
		}
	}

	if target := flags["mesh"]; target != "" {
		start := rt.StartMeshtastic
		wire := "raw"
		// --mesh-seed opts this radio into the Quiet Radio Transfer layer:
		// whole messages, fragmented to the carrier's real MTU, and repaired
		// when a fragment goes missing.
		//
		// The seed is what makes a segment a segment — every radio derives
		// the same frame-authentication key from it, so a peer with a
		// different seed is not a peer. It is a flag rather than something
		// carried in the invite because MR-2 is what puts it in the invite,
		// and shipping an interim shape into the keystore is how an interim
		// shape becomes permanent.
		if seed := flags["mesh-seed"]; seed != "" {
			sum, err := radiotransfer.SeedFromPhrase(seed)
			if err != nil {
				return fmt.Errorf("--mesh-seed: %w", err)
			}
			start = func(t string) error { return rt.StartMeshtasticTransfer(t, sum) }
			wire = "radio-transfer"
		}
		// compact is decided AFTER the transfer layer, and only where the
		// transfer layer was not asked for: two fragmentation layers on one
		// carrier would each repair what the other broke up.
		if wire != "radio-transfer" {
			switch flags["compact"] {
			case "", "0":
				// raw default for real radios
			case "table", "2b":
				start = rt.StartMeshtasticTable
				wire = "compact+idtable"
			default:
				start = rt.StartMeshtasticCompact
				wire = "compact"
			}
		} else if c := flags["compact"]; c != "" && c != "0" {
			return errors.New("--compact and --mesh-seed are two fragmentation " +
				"layers on one radio: the transfer layer already cuts messages to " +
				"the carrier's MTU and repairs what goes missing. Pick one")
		}
		if err := start(target); err != nil {
			fmt.Println("mesh: connection failed:", err)
		} else {
			m := rt.Mesh()
			fmt.Printf("mesh: channel %d · connected as node %d via %s (%s wire, summaries every 60s — LoRa airtime)\n",
				m.Channel, m.NodeNum, target, wire)
			if m.Channel == 0 {
				fmt.Println("  note: channel 0 is this radio's PRIMARY channel, usually the" +
					" public default-key one.\n  Everyone in range sees these packets (contents stay" +
					" encrypted) and relays them.\n  Use --mesh-channel N for a channel of your own.")
			}
		}
	}

	var uiFS = webui.FS()
	if !withUI {
		uiFS = nil
	}
	api, err := node.NewAPIServer(rt, uiFS)
	if err != nil {
		return err
	}
	// The gate already holds a port — take that same one, or a browser sent
	// to the lock screen on an auto-assigned port would find nothing there
	// after unlocking.
	port := parsePort(flags["port"])
	if gatePort >= 0 {
		port = gatePort
	}
	api.SetToken(gateToken)
	addr, l, err := api.Serve(port)
	if err != nil {
		return err
	}
	defer l.Close()
	url := "http://" + addr + "/?token=" + api.Token()
	fmt.Println("local API:", url)

	if withUI && flags["no-browser"] == "" {
		openBrowser(url)
	}

	// External connectors (TR-0d): every configured email adapter polls in
	// its own goroutine for the life of the process. The journal makes the
	// poller crash-safe, so process exit is an acceptable stop.
	connCtx, connCancel := context.WithCancel(context.Background())
	defer connCancel()
	for cid, ac := range rt.GetSettings().Adapters {
		if ac.Type != "email" {
			continue
		}
		cid, ac := cid, ac
		go email.Run(connCtx, email.Config{
			URL: ac.JMAPURL, Account: ac.Account, Token: ac.Token,
			PollSeconds: ac.PollSeconds,
		}, func(env node.ExternalEnvelope) error {
			return rt.ConnectorIngest(cid, env)
		}, func() []node.OutboundEnvelope {
			// Unsettled sends from before a restart first, then the fresh
			// authority-checked candidates.
			resume, _ := rt.ConnectorOutboundResume(cid)
			fresh, _ := rt.ConnectorOutbox(cid)
			return append(resume, fresh...)
		}, func(eid id.EventID, sent bool, outcome string) {
			_ = rt.ConnectorOutboundResult(cid, eid, sent, outcome)
		})
		fmt.Printf("connector %s: email poller running for %s\n", cid, ac.Account)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("\nshutting down")
	return nil
}

// openBrowser launches the default browser at url on the host OS. Best-effort:
// a headless box or a missing opener is a no-op — the tokened URL is printed
// above either way.
func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default: // linux, *bsd
		cmd = "xdg-open"
	}
	_ = exec.Command(cmd, append(args, url)...).Start()
}
