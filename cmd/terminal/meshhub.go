package main

import (
	"crypto/sha256"
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

	// Simulated devices can report a configuration, including a WRONG one.
	// The Gateway screen exists to catch misconfiguration, and a fake radio
	// that is always set up correctly cannot exercise it — so the segment's
	// settings are flags here, and mismatching them on purpose is the point.
	if cfg := hubConfigFromFlags(flags); cfg != nil {
		hub.SetConfig(cfg)
		fmt.Printf("simulated devices report: region %s · preset %s · "+
			"hop %d · channel %q · key %s\n",
			flags["region"], flags["preset"], cfg.HopLimit, cfg.ChannelName,
			keyWord(cfg.PSK))
	}

	fmt.Println("fake mesh hub listening on", hub.Addr())
	fmt.Println("point nodes at it:  terminal ui --mesh tcp:" + hub.Addr())
	fmt.Println("honesty note: this is LoRa-in-a-box — no loss, no airtime limits; real radios are slower")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	return nil
}

// hubConfigFromFlags builds what simulated devices report about themselves.
// Returns nil when no flags were given, which makes the hub behave like
// older firmware that reports nothing — itself a case worth exercising,
// since the diagnostic must call that "unverified" rather than "wrong".
//
//	--region EU_868 --preset LONG_FAST --hop 3 --channel quiet-beta
//	--key private|default|none
func hubConfigFromFlags(flags map[string]string) *meshtastic.HubConfig {
	if flags["region"] == "" && flags["preset"] == "" &&
		flags["channel"] == "" && flags["key"] == "" {
		return nil
	}
	cfg := &meshtastic.HubConfig{
		UsePreset: true, TxEnabled: flags["tx-off"] == "",
		HopLimit: 3, ChannelName: flags["channel"], Firmware: "hub-sim",
	}
	if flags["channel"] == "" {
		cfg.ChannelName = "LongFast"
	}
	if h := flags["hop"]; h != "" {
		var n uint32
		fmt.Sscanf(h, "%d", &n)
		cfg.HopLimit = n
	}
	cfg.Region = meshtastic.RegionValue(flags["region"])
	cfg.ModemPreset = meshtastic.PresetValue(flags["preset"])
	switch flags["key"] {
	case "", "none":
		cfg.PSK = nil
	case "default":
		cfg.PSK = []byte{0x01}
	default:
		// Any other word becomes a distinct private key, so two hubs given
		// different words simulate two segments that cannot hear each other.
		sum := sha256.Sum256([]byte(flags["key"]))
		cfg.PSK = sum[:16]
	}
	return cfg
}

func keyWord(psk []byte) string {
	switch {
	case len(psk) == 0:
		return "none (unencrypted)"
	case len(psk) == 1:
		return "the built-in default (public)"
	}
	return "private"
}
