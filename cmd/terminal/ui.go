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

	var uiFS = webui.FS()
	if !withUI {
		uiFS = nil
	}
	api, err := node.NewAPIServer(rt, uiFS)
	if err != nil {
		return err
	}
	addr, l, err := api.Serve(0)
	if err != nil {
		return err
	}
	defer l.Close()
	url := "http://" + addr + "/?token=" + api.Token()
	fmt.Println("local API:", url)

	if withUI && flags["no-browser"] == "" && runtime.GOOS == "darwin" {
		_ = exec.Command("open", url).Start()
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("\nshutting down")
	return nil
}
