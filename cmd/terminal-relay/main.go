// Command terminal-relay runs a standalone blind relay node (plan §22,
// M1.5). Anyone can run one; no relay is mandatory; this one cannot read
// what it carries — it stores rotating hints and ciphertext with TTLs, and
// deletes unconditionally at expiry (ADR-010).
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

func main() {
	addr := ":7411"
	dataDir := ""
	limits := relay.DefaultLimits()
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--listen":
			if i+1 < len(args) {
				addr = args[i+1]
				i++
			}
		case "--data":
			if i+1 < len(args) {
				dataDir = args[i+1]
				i++
			}
		case "--help", "-h":
			fmt.Println("usage: terminal-relay [--listen ADDR] [--data DIR]")
			fmt.Println("  --listen  bind address (default :7411)")
			fmt.Println("  --data    directory for the persistent relay identity key;")
			fmt.Println("            omitted = ephemeral identity (local/dev profile)")
			return
		}
	}
	// A persistent identity (RR-1) only when asked for: a public relay
	// pins its SPKI, a dev relay on loopback needs no name at all.
	var srv *relay.Server
	var port int
	var err error
	pin := ""
	if dataDir != "" {
		cert, p, ierr := loadOrCreateIdentity(dataDir)
		if ierr != nil {
			fmt.Fprintln(os.Stderr, "identity error:", ierr)
			os.Exit(1)
		}
		pin = p
		srv, port, err = relay.StartServerWithIdentity(addr, limits, &cert)
	} else {
		srv, port, err = relay.StartServer(addr, limits)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer srv.Close()
	fmt.Printf("blind relay listening on port %d\n", port)
	if pin != "" {
		fmt.Printf("identity SPKI pin: %s\n", pin)
		fmt.Println("(clients pin this key; the certificate may change, the pin must not)")
	} else {
		fmt.Println("identity: ephemeral (local/dev profile — clients do not pin)")
	}
	fmt.Printf("limits: %d items/hint, %d KiB/item, TTL ≤ %s\n",
		limits.PerHint, limits.MaxItemBytes/1024, limits.MaxTTL)
	fmt.Println("this relay stores rotating hints and ciphertext; it has no way to read either")

	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			fmt.Printf("pending items: %d\n", srv.Pending())
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("\nshutting down")
}
