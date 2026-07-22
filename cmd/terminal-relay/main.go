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
	limits := relay.DefaultLimits()
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--listen":
			if i+1 < len(args) {
				addr = args[i+1]
				i++
			}
		case "--help", "-h":
			fmt.Println("usage: terminal-relay [--listen ADDR]   (default :7411)")
			return
		}
	}
	srv, port, err := relay.StartServer(addr, limits)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer srv.Close()
	fmt.Printf("blind relay listening on port %d\n", port)
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
