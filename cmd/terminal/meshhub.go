package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

// runMeshHub runs the fake mesh: every connected client acts as a radio on
// one shared channel. Development only — it has none of LoRa's loss, delay,
// or airtime limits, and says so.
func runMeshHub(args []string) error {
	flags := parseFlags(args)
	addr := flags["listen"]
	if addr == "" {
		addr = "127.0.0.1:4403"
	}
	hub, err := meshtastic.StartHub(addr)
	if err != nil {
		return err
	}
	defer hub.Close()
	fmt.Println("fake mesh hub listening on", hub.Addr())
	fmt.Println("point nodes at it:  terminal ui --mesh tcp:" + hub.Addr())
	fmt.Println("honesty note: this is LoRa-in-a-box — no loss, no airtime limits; real radios are slower")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	return nil
}
