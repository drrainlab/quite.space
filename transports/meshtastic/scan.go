// Finding the radio (RB-2). Asking a person to type a device path is asking
// them to know something the computer already knows — and to keep knowing it
// when it changes underneath them.
//
// It does change. A T3-S3 that enumerates as
// /dev/cu.usbmodem24EC4A307B541 one minute comes back as
// /dev/cu.usbmodem1101 after a reset, because one name is derived from the
// firmware's MAC and the other from the USB port location. A configuration
// file with a device path in it is therefore wrong on a schedule.
//
// So: probe every plausible port and say what is actually on each one. The
// honesty rule is the same as everywhere else in this package — report what a
// port answered, never what it probably is. A port that did not respond is
// reported as not responding, not as "no radio".
package meshtastic

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// ProbeKind is what a port turned out to be.
type ProbeKind string

const (
	// ProbeRadio: a Meshtastic node answered and told us about itself.
	ProbeRadio ProbeKind = "radio"
	// ProbeBusy: the port exists but something else holds it. Very common —
	// the Meshtastic app, a serial monitor, or another copy of this program.
	ProbeBusy ProbeKind = "busy"
	// ProbeSilent: opened, but nothing that looked like a Meshtastic node
	// came back before the deadline.
	ProbeSilent ProbeKind = "silent"
	// ProbeSkipped: not a plausible candidate (Bluetooth audio and friends).
	// Listed rather than hidden, so "why didn't it try X" has an answer.
	ProbeSkipped ProbeKind = "skipped"
)

// PortProbe is one port and what answered on it.
type PortProbe struct {
	Port   string
	Kind   ProbeKind
	Detail string

	// Filled in only for ProbeRadio, and only with what the node itself said.
	NodeNum  uint32
	Firmware string
	Region   string
	Preset   string
	Channels int
	// PrimaryKey describes the channel key CLASS on the primary channel —
	// never the key. Useful at a glance: a node on the public default key is
	// the single most common surprise.
	PrimaryKey string
}

// ScanSerial probes every serial port and reports what is on each.
//
// Ports are probed CONCURRENTLY but each with its own short deadline, because
// a laptop can have a dozen of them and a person watching a spinner will not
// wait a dozen timeouts. A busy port fails immediately, which is the common
// case and costs nothing.
func ScanSerial(idle time.Duration) []PortProbe {
	ports, err := ListSerialPorts()
	if err != nil {
		return nil
	}
	var (
		mu   sync.Mutex
		out  []PortProbe
		wg   sync.WaitGroup
		sema = make(chan struct{}, 4) // a few at a time; opening ports is not free
	)
	for _, p := range ports {
		if reason, skip := skipPort(p); skip {
			mu.Lock()
			out = append(out, PortProbe{Port: p, Kind: ProbeSkipped, Detail: reason})
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(port string) {
			defer wg.Done()
			sema <- struct{}{}
			defer func() { <-sema }()
			pr := probePort(port, idle)
			mu.Lock()
			out = append(out, pr)
			mu.Unlock()
		}(p)
	}
	wg.Wait()

	// Radios first, then the ports worth a second look, then the noise.
	rank := map[ProbeKind]int{ProbeRadio: 0, ProbeBusy: 1, ProbeSilent: 2, ProbeSkipped: 3}
	sort.Slice(out, func(i, j int) bool {
		if rank[out[i].Kind] != rank[out[j].Kind] {
			return rank[out[i].Kind] < rank[out[j].Kind]
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// skipPort filters ports that cannot be a radio, with a reason. Reporting the
// reason matters: a person looking for their device wants to know we saw the
// port and chose not to try it.
func skipPort(port string) (string, bool) {
	low := strings.ToLower(port)
	switch {
	case strings.Contains(low, "bluetooth"):
		return "Bluetooth port, not a USB radio", true
	case strings.Contains(low, "debug-console"):
		return "system debug console", true
	case strings.HasPrefix(port, "/dev/tty."):
		// On macOS the cu.* twin is the one to open; tty.* blocks waiting for
		// carrier detect and would hang the probe.
		return "duplicate of the cu.* port", true
	}
	// Known Bluetooth audio naming is not exhaustive, so anything that is not
	// a recognisable USB serial device is skipped rather than opened: opening
	// a random port can wedge it.
	for _, want := range []string{"usbmodem", "usbserial", "ttyusb", "ttyacm", "wchusb", "slab_usb"} {
		if strings.Contains(low, want) {
			return "", false
		}
	}
	return "not a USB serial device", true
}

// probePort asks a port what it is, and asks TWICE before saying "nothing".
//
// Measured on a native-USB ESP32-S3 running Meshtastic: consecutive attempts
// alternate cleanly between answering and timing out "after 0 frames", and
// the length of the patience makes no difference — an eight-second wait fails
// where a one-and-a-half-second one succeeds. Whatever the device is doing
// between opens, ONE ATTEMPT IS NOT EVIDENCE, and this function's whole job
// is to report what is on a port.
//
// The cost is paid only where it is owed: a port that answers costs one
// attempt, and a genuinely empty one costs a second short timeout.
func probePort(port string, idle time.Duration) PortProbe {
	pr := probeOne(port, idle)
	if pr.Kind != ProbeSilent {
		return pr
	}
	time.Sleep(300 * time.Millisecond)
	again := probeOne(port, idle)
	if again.Kind == ProbeRadio {
		return again
	}
	return pr
}

// probeOne opens a port, asks the node to identify itself, and closes.
func probeOne(port string, idle time.Duration) PortProbe {
	pr := PortProbe{Port: port}

	// A probe uses a shorter patience than a real attach: we are identifying
	// a device, not waiting out a hundred-node database. The deadline rides
	// in Options because probes run CONCURRENTLY — when this was a package
	// variable each probe's patience depended on what the others happened to
	// be doing at that instant.
	radio, err := openSerial(port, Options{Idle: idle})

	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "busy"), strings.Contains(msg, "in use"),
			strings.Contains(msg, "locked"), strings.Contains(msg, "denied"):
			pr.Kind, pr.Detail = ProbeBusy, "something else is using this port "+
				"(the Meshtastic app, a serial monitor, or another copy of this program)"
		default:
			pr.Kind, pr.Detail = ProbeSilent, msg
		}
		return pr
	}
	defer radio.Close()

	cfg := radio.Config()
	pr.Kind = ProbeRadio
	pr.NodeNum = radio.NodeNum()
	pr.Firmware = cfg.Firmware
	pr.Channels = len(cfg.Channels)
	if cfg.LoRa != nil {
		pr.Region = cfg.LoRa.RegionName()
		pr.Preset = cfg.LoRa.PresetName()
	}
	if ch, ok := cfg.PrimaryChannel(); ok {
		pr.PrimaryKey = ch.KeyClass.String()
	}
	switch {
	case pr.Firmware != "" && pr.Region != "":
		pr.Detail = "Meshtastic node, firmware " + pr.Firmware
	case pr.NodeNum != 0:
		pr.Detail = "Meshtastic node (it did not report its settings)"
	default:
		// It spoke the stream protocol but told us nothing identifying.
		pr.Kind = ProbeSilent
		pr.Detail = "opened, but nothing identified itself"
	}
	return pr
}

// FirstRadio returns the port of the first probe that is a radio.
func FirstRadio(probes []PortProbe) (string, bool) {
	for _, p := range probes {
		if p.Kind == ProbeRadio {
			return p.Port, true
		}
	}
	return "", false
}
