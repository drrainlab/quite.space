package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	webui "github.com/drrainlab/quiet_places/clients/web-ui"
	"github.com/drrainlab/quiet_places/node"
	"github.com/drrainlab/quiet_places/transports/lan"
	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

// runUI starts the node runtime, the local API, and (unless --no-browser)
// opens the embedded web UI. `terminal node` is the same runtime without
// UI assets — one binary, one behavior (ADR-011).
func runUI(args []string, withUI bool) error {
	flags := parseFlags(args)
	dataDir := flags["data"]
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dataDir = filepath.Join(home, ".quiet-places")
	}
	pass := flags["passphrase"]
	if pass == "" {
		pass = os.Getenv("QP_PASSPHRASE")
	}
	if pass == "" {
		return fmt.Errorf("pass --passphrase or set QP_PASSPHRASE (encrypts the local keystore)")
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
	if net := flags["mesh-network"]; net != "" {
		rt.SetMeshNetwork(net)
		fmt.Println("listening for gateway beacons on network", net)
	}

	if target := flags["mesh"]; target != "" {
		start := rt.StartMeshtastic
		wire := "raw"
		switch flags["compact"] {
		case "table", "2b":
			start = rt.StartMeshtasticTable
			wire = "compact+idtable"
		case "", "0":
			// raw default for real radios
			if flags["compact"] != "" {
				start = rt.StartMeshtasticCompact
				wire = "compact"
			}
		default:
			start = rt.StartMeshtasticCompact
			wire = "compact"
		}
		if err := start(target); err != nil {
			fmt.Println("mesh: connection failed:", err)
		} else {
			fmt.Printf("mesh: connected as node %d via %s (%s wire, summaries every 60s — LoRa airtime)\n",
				rt.Mesh().NodeNum, target, wire)
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
	port := 0
	if p := flags["port"]; p != "" {
		if _, err := fmt.Sscanf(p, "%d", &port); err != nil {
			return fmt.Errorf("bad --port %q", p)
		}
	}
	api.SetToken(flags["token"])
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
