// A deliberately independent look at one RNode.
//
// It shares no code with transports/rnode on purpose: when a carrier delivers
// nothing, the driver is a suspect, and a diagnostic built on the suspect can
// only confirm its own assumptions. This talks to the serial port directly and
// prints every KISS frame it sees, decoded where the meaning is known.
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"time"

	"go.bug.st/serial"
)

const (
	fend  = 0xC0
	fesc  = 0xDB
	tfend = 0xDC
	tfesc = 0xDD
)

var names = map[byte]string{
	0x00: "DATA", 0x01: "FREQUENCY", 0x02: "BANDWIDTH", 0x03: "TXPOWER",
	0x04: "SF", 0x05: "CR", 0x06: "RADIO_STATE", 0x07: "RADIO_LOCK",
	0x08: "DETECT", 0x0A: "LEAVE", 0x0B: "ST_ALOCK", 0x0C: "LT_ALOCK",
	0x0F: "READY", 0x21: "STAT_RX", 0x22: "STAT_TX", 0x23: "STAT_RSSI",
	0x24: "STAT_SNR", 0x25: "STAT_CHTM", 0x26: "STAT_PHYPRM",
	0x27: "STAT_BAT", 0x28: "STAT_CSMA", 0x29: "STAT_TEMP",
	0x48: "PLATFORM", 0x49: "MCU", 0x50: "FW_VERSION", 0x90: "ERROR",
}

func main() {
	dev := flag.String("dev", "", "serial device")
	freq := flag.Uint("freq", 868950000, "frequency in Hz")
	bw := flag.Uint("bw", 250000, "bandwidth in Hz")
	sf := flag.Uint("sf", 11, "spreading factor")
	cr := flag.Uint("cr", 5, "coding rate")
	txp := flag.Uint("txp", 20, "tx power dBm")
	send := flag.Bool("send", false, "transmit a probe frame after configuring")
	watch := flag.Duration("watch", 20*time.Second, "how long to listen")
	settle := flag.Duration("settle", 2*time.Second, "wait after opening the port")
	flag.Parse()
	if *dev == "" {
		fmt.Println("-dev is required")
		os.Exit(2)
	}

	p, err := serial.Open(*dev, &serial.Mode{BaudRate: 115200})
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer p.Close()
	p.SetReadTimeout(100 * time.Millisecond)

	go readLoop(p)

	// The reference driver sleeps 2 s here, and the reason matters on a
	// native-USB board: opening the port resets the MCU, so configuring
	// immediately talks to a device that is still booting.
	time.Sleep(*settle)

	write := func(cmd byte, data ...byte) {
		out := []byte{fend, cmd}
		for _, c := range data {
			switch c {
			case fend:
				out = append(out, fesc, tfend)
			case fesc:
				out = append(out, fesc, tfesc)
			default:
				out = append(out, c)
			}
		}
		p.Write(append(out, fend))
		time.Sleep(120 * time.Millisecond)
	}
	be32 := func(v uint32) []byte {
		return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	}

	fmt.Println("── detect ──")
	write(0x08, 0x73)
	write(0x50, 0x00)
	write(0x48, 0x00)
	write(0x49, 0x00)
	time.Sleep(500 * time.Millisecond)

	fmt.Println("── configure ──")
	write(0x01, be32(uint32(*freq))...)
	write(0x02, be32(uint32(*bw))...)
	write(0x03, byte(*txp))
	write(0x04, byte(*sf))
	write(0x05, byte(*cr))
	// The airtime locks the reference driver sets and this project's driver
	// did not. Zero means "no limit"; a stale non-zero value in the board's
	// own configuration would block transmission with no error at all.
	write(0x0B, 0x00, 0x00)
	write(0x0C, 0x00, 0x00)
	write(0x06, 0x01)
	time.Sleep(700 * time.Millisecond)

	// ONLY the radio state has an "ask" form (0xFF). Every other command
	// SETS — asking the frequency by sending a zero sets the frequency to
	// zero. Learned by doing it to a board mid-diagnosis.
	fmt.Println("── ask the radio state ──")
	write(0x06, 0xFF)
	time.Sleep(500 * time.Millisecond)

	if *send {
		fmt.Println("── transmitting 5 probe frames ──")
		for i := range 5 {
			write(0x00, []byte(fmt.Sprintf("PROBE-%d-%s", i, "quiet"))...)
			time.Sleep(3 * time.Second)
		}
	}

	fmt.Printf("── listening %s ──\n", *watch)
	time.Sleep(*watch)
}

func readLoop(p serial.Port) {
	buf := make([]byte, 1024)
	var frame []byte
	inFrame, haveCmd, escaped := false, false, false
	var cmd byte
	for {
		n, err := p.Read(buf)
		if err != nil {
			return
		}
		for _, c := range buf[:n] {
			switch {
			case c == fend:
				if inFrame && haveCmd {
					show(cmd, frame)
				}
				inFrame, haveCmd, escaped, frame, cmd = true, false, false, nil, 0
				continue
			case !inFrame:
				continue
			case escaped:
				switch c {
				case tfend:
					c = fend
				case tfesc:
					c = fesc
				}
				escaped = false
			case c == fesc:
				escaped = true
				continue
			}
			if !haveCmd {
				cmd, haveCmd = c, true
				continue
			}
			frame = append(frame, c)
		}
	}
}

func show(cmd byte, b []byte) {
	name := names[cmd]
	if name == "" {
		name = "?"
	}
	// Channel utilisation is the one telemetry frame worth reading: it says
	// how much of the air OTHER transmitters are occupying, which is the
	// difference between "our tuning is wrong" and "the band is busy".
	if cmd == 0x25 && len(b) >= 11 {
		ats := float64(int(b[0])<<8|int(b[1])) / 100
		atl := float64(int(b[2])<<8|int(b[3])) / 100
		cus := float64(int(b[4])<<8|int(b[5])) / 100
		cul := float64(int(b[6])<<8|int(b[7])) / 100
		fmt.Printf("  CHTM  our airtime %.2f%% / %.2f%%   CHANNEL BUSY %.2f%% / %.2f%%"+
			"   noise floor %d dBm\n", ats, atl, cus, cul, int(int8(b[9])))
		return
	}
	// The rest arrives constantly and would bury the answers.
	switch cmd {
	case 0x25, 0x26, 0x27, 0x28, 0x29, 0x23, 0x24:
		return
	}
	extra := ""
	switch cmd {
	case 0x01, 0x02:
		if len(b) >= 4 {
			extra = fmt.Sprintf("  = %d", uint32(b[0])<<24|uint32(b[1])<<16|
				uint32(b[2])<<8|uint32(b[3]))
		}
	case 0x03, 0x04, 0x05, 0x06:
		if len(b) >= 1 {
			extra = fmt.Sprintf("  = %d", b[0])
		}
	case 0x90:
		if len(b) >= 1 {
			extra = fmt.Sprintf("  = ERROR 0x%02x", b[0])
		}
	case 0x00:
		extra = fmt.Sprintf("  = %q", string(b))
	}
	fmt.Printf("  0x%02x %-12s %s%s\n", cmd, name, hex.EncodeToString(b), extra)
}
