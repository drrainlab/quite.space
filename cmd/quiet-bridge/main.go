// Command quiet-bridge is the blind boundary element (TN-B, ADR-015): a
// store-and-forward daemon between a radio carrier and the blind relay.
// It is not a member of anything: no identity, no keys of any space, no
// payload access — a Raspberry Pi with a radio and an uplink becomes a
// segment gateway that can prove custody and nothing more.
package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/drrainlab/quiet_places/kernel/routing"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports"
	"github.com/drrainlab/quiet_places/transports/bridge"
	"github.com/drrainlab/quiet_places/transports/compact"
	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

func main() {
	flags := parseFlags(os.Args[1:])
	dataDir := flags["data"]
	if dataDir == "" {
		dataDir = "./quiet-bridge-data"
	}
	radioTarget := flags["radio"]
	relayAddr := flags["relay"]
	if radioTarget == "" || relayAddr == "" {
		fmt.Fprintln(os.Stderr, `usage: quiet-bridge --radio tcp:HOST[:PORT]|serial:/dev/PATH --relay HOST:PORT
       [--data DIR] [--subscriptions FILE] [--learn] [--compact]
       [--airtime BYTES_PER_MIN] [--ttl HOURS]`)
		os.Exit(2)
	}

	var radio transports.Endpoint
	var rerr error
	switch {
	case strings.HasPrefix(radioTarget, "tcp:"):
		radio, rerr = meshtastic.DialTCP(strings.TrimPrefix(radioTarget, "tcp:"))
	case strings.HasPrefix(radioTarget, "serial:"):
		radio, rerr = meshtastic.OpenSerial(strings.TrimPrefix(radioTarget, "serial:"))
	default:
		fmt.Fprintln(os.Stderr, "error: radio target must be tcp: or serial:")
		os.Exit(2)
	}
	if rerr != nil {
		fmt.Fprintln(os.Stderr, "radio:", rerr)
		os.Exit(1)
	}
	// Real radios default to RAW; --compact is the operator's opt-in
	// (every peer on the carrier must speak compact too — TN-2A).
	if flags["compact"] != "" {
		radio = compact.Wrap(radio)
	}

	subs, err := loadSubscriptions(flags["subscriptions"])
	if err != nil {
		fmt.Fprintln(os.Stderr, "subscriptions:", err)
		os.Exit(1)
	}

	caps := routing.DefaultQueueCaps()
	if h := flags["ttl"]; h != "" {
		var hours int
		if _, err := fmt.Sscanf(h, "%d", &hours); err == nil && hours > 0 {
			caps.OperatorTTL = time.Duration(hours) * time.Hour
		}
	}
	airtime := 0.0
	if a := flags["airtime"]; a != "" {
		fmt.Sscanf(a, "%f", &airtime)
	}

	b, err := bridge.New(bridge.Config{
		DataDir:       dataDir,
		Instance:      hostname(),
		Radio:         radio,
		RadioLink:     routing.LinkID("mesh:" + radioTarget),
		RadioDomain:   routing.LoopDomainID("mesh:" + radioTarget),
		RelayAddr:     relayAddr,
		RelayDomain:   routing.LoopDomainID("relay:" + relayAddr),
		Subscriptions: subs,
		Learn:         flags["learn"] != "",
		AirtimePerMin: airtime,
		QueueCaps:     caps,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "bridge:", err)
		os.Exit(1)
	}
	defer b.Close()
	fmt.Println(b)
	fmt.Printf("custodian public key (pin on nodes): %x\n", b.CustodianPub())
	fmt.Println("blind by construction: no identity, no space keys, headers only")
	if flags["learn"] != "" {
		fmt.Println("note: --learn admits unknown destinations onto probation, " +
			"but a destination with no operator-provisioned internet mailbox " +
			"has nowhere to be delivered and is refused rather than held")
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	radioT := time.NewTicker(2 * time.Second)
	relayT := time.NewTicker(20 * time.Second)
	sweepT := time.NewTicker(5 * time.Minute)
	defer radioT.Stop()
	defer relayT.Stop()
	defer sweepT.Stop()

	for {
		select {
		case <-stop:
			fmt.Println("\nshutting down — custody persists on disk")
			return
		case <-radioT.C:
			now := time.Now()
			b.PumpRadio(now)
			// Announce before draining: a node hands over frames only when
			// something asks, and a backed-up data queue must never be the
			// reason the bridge went silent.
			b.WakeRadio(now)
			b.PushRadio(now)
		case <-relayT.C:
			now := time.Now()
			if _, err := b.PushRelay(now); err != nil {
				fmt.Println("relay push:", err)
			}
			if _, err := b.PullRelay(now); err != nil {
				fmt.Println("relay pull:", err)
			}
			s := b.Stats()
			fmt.Printf("radio in/out %d/%d · relay in/out %d/%d · custody %d · dedup %d · refused %d\n",
				s.RadioIn, s.RadioOut, s.RelayIn, s.RelayOut, b.QueueLen(), s.Deduped, s.Refused)
		case <-sweepT.C:
			b.Sweep(time.Now())
		}
	}
}

// loadSubscriptions reads the operator's routing capabilities. One line per
// destination:
//
//	<network-id> <terminal-hex> radio=<dev-hex>[,<dev-hex>…] internet=<dev-hex>[,…]
//
// Every id is opaque to this daemon. What the operator is declaring is not
// who these people are but WHERE each mailbox can be reached from — which
// side of the boundary — because that is the one thing a blind gateway
// cannot work out for itself.
func loadSubscriptions(path string) ([]bridge.Subscription, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []bridge.Subscription
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			return nil, fmt.Errorf("line %d: want "+
				"`<network-id> <terminal-hex> radio=… internet=…`", n+1)
		}
		term, err := parseTerminal(fields[1])
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n+1, err)
		}
		sub := bridge.Subscription{NetworkID: fields[0], Terminal: term}
		for _, f := range fields[2:] {
			key, list, ok := strings.Cut(f, "=")
			if !ok {
				return nil, fmt.Errorf("line %d: expected key=value, got %q", n+1, f)
			}
			devs, err := parseDevices(list)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", n+1, err)
			}
			switch key {
			case "radio":
				sub.RadioDevices = append(sub.RadioDevices, devs...)
			case "internet":
				sub.InternetDevices = append(sub.InternetDevices, devs...)
			default:
				return nil, fmt.Errorf("line %d: unknown field %q "+
					"(expected radio= or internet=)", n+1, key)
			}
		}
		if len(sub.InternetDevices) == 0 && len(sub.RadioDevices) == 0 {
			return nil, fmt.Errorf("line %d: a subscription with no mailbox "+
				"carries nothing in either direction", n+1)
		}
		out = append(out, sub)
	}
	return out, nil
}

func parseTerminal(s string) (id.TerminalID, error) {
	var t id.TerminalID
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != id.Size {
		return t, fmt.Errorf("bad terminal id %q", s)
	}
	copy(t[:], b)
	return t, nil
}

func parseDevices(list string) ([]id.DeviceID, error) {
	var out []id.DeviceID
	for _, s := range strings.Split(list, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		b, err := hex.DecodeString(s)
		if err != nil || len(b) != id.Size {
			return nil, fmt.Errorf("bad device id %q", s)
		}
		var d id.DeviceID
		copy(d[:], b)
		out = append(out, d)
	}
	return out, nil
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "quiet-bridge"
	}
	return h
}

func parseFlags(args []string) map[string]string {
	out := map[string]string{}
	for i := 0; i < len(args); i++ {
		if len(args[i]) <= 2 || args[i][:2] != "--" {
			continue
		}
		name := args[i][2:]
		if i+1 >= len(args) || (len(args[i+1]) > 2 && args[i+1][:2] == "--") {
			out[name] = "1"
			continue
		}
		out[name] = args[i+1]
		i++
	}
	return out
}
