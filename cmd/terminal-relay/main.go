// SPDX-License-Identifier: AGPL-3.0-only
//
// Copyright 2026 Gleb Bondarenko and the quite.space contributors
//
// This is a NETWORK SERVICE, and it is licensed differently from the rest
// of the repository: the protocol and everything a client needs is
// Apache-2.0, while a component somebody stands up so that OTHER PEOPLE
// can use it is AGPL-3.0-only. Run it, modify it, charge to host it — but
// if you offer a modified version to users over a network, those users are
// entitled to that version's source. See LICENSING.md.
//
// Command terminal-relay runs a standalone blind relay node (plan §22,
// M1.5). Anyone can run one; no relay is mandatory; this one cannot read
// what it carries — it stores rotating hints and ciphertext with TTLs, and
// deletes unconditionally at expiry (ADR-010).
package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

func main() {
	addr := ":7411"
	dataDir := ""
	limits := relay.DefaultLimits()
	var perHintBytes, totalBytes int64
	intArg := func(args []string, i *int) int64 {
		if *i+1 < len(args) {
			*i++
			n, err := strconv.ParseInt(args[*i], 10, 64)
			if err == nil {
				return n
			}
		}
		return 0
	}
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
		// Operator limits (RR-7): abuse rails, not throughput promises.
		case "--max-conns":
			limits.MaxConns = int(intArg(args, &i))
		case "--max-item-kib":
			limits.MaxItemBytes = int(intArg(args, &i)) << 10
		case "--per-hint-mib":
			perHintBytes = intArg(args, &i) << 20
		case "--total-gib":
			totalBytes = intArg(args, &i) << 30
		case "--collect-rate":
			limits.CollectRatePerMin = int(intArg(args, &i))
		case "--write-rate":
			limits.WriteRatePerMin = int(intArg(args, &i))
		case "--fetch-rate":
			limits.FetchRatePerMin = int(intArg(args, &i))
		case "--help", "-h":
			fmt.Println("usage: terminal-relay [--listen ADDR] [--data DIR] [limits]")
			fmt.Println("  --listen        bind address (default :7411)")
			fmt.Println("  --data          directory for the persistent relay identity key;")
			fmt.Println("                  omitted = ephemeral identity (local/dev profile)")
			fmt.Println("  --max-conns N   concurrent connection cap (default 4096)")
			fmt.Println("  --max-item-kib N | --per-hint-mib N | --total-gib N")
			fmt.Println("  --collect-rate N | --write-rate N | --fetch-rate N   (per conn per minute)")
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
	if perHintBytes > 0 || totalBytes > 0 {
		srv.SetByteBudgets(int(perHintBytes), totalBytes)
	}
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
			// The structured metrics line (RR-7) — one greppable record a
			// minute-scale scrape or a human tail can both read.
			fmt.Printf("metrics items=%d bytes=%d conns=%d\n",
				srv.Pending(), srv.PendingBytes(), srv.Conns())
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("\nshutting down")
}
