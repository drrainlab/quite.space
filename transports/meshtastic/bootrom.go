// Identifying a board that is NOT running Meshtastic yet (RB-2).
//
// ScanSerial reports such a port as ProbeSilent, which is the honest answer to
// "is there a Meshtastic node here" and a useless one for "what is this
// thing". A board fresh out of its box, or one carrying its manufacturer's
// demo firmware, says nothing at all on the wire: probing it at every baud
// rate from 9600 to 921600 returns zero bytes.
//
// It does say one thing, though, and it says it to everybody: every ESP32
// prints its boot ROM banner on reset, before any firmware runs.
//
//	ESP-ROM:esp32s3-20210327
//	rst:0x1 (POWERON),boot:0x9 (SPI_FAST_FLASH_BOOT)
//
// That line is the only piece of hardware evidence available before a device
// is flashed, and it is worth exactly what it says: the MCU FAMILY. It does
// not name the board. A dozen different LoRa boards carry an ESP32-S3, with
// different radio chips, different pin maps and different antenna switching,
// and flashing the wrong one produces a device that is silent or damaged.
//
// So this file exists to narrow a list, never to choose from it:
//
//   - the chip family is EVIDENCE, quoted from the banner the chip printed;
//   - anything not in the banner is UNKNOWN, never inferred;
//   - the board model is a question for a person, always.
//
// Getting the banner requires asserting the reset line ourselves. On the
// usual USB-serial auto-reset circuit RTS drives EN, so a pulse there reboots
// the chip. Without it the board simply keeps running whatever it was already
// running, silently — which is why a plain read finds nothing.
package meshtastic

import (
	"regexp"
	"strings"
	"time"

	"go.bug.st/serial"
)

// BootROM is what a chip said about itself on reset. Empty fields mean the
// device did not say, which is a different fact from a default.
type BootROM struct {
	// Chip is the MCU family exactly as the banner spells it: "esp32s3",
	// "esp32", "esp32c3". Empty when nothing recognisable arrived.
	Chip string
	// Reset is the reason line ("0x1 (POWERON)"), useful when a board comes
	// up in a way that explains a later failure.
	Reset string
	// Banner is the raw text, so a person can see what we read rather than
	// only what we made of it.
	Banner string
}

// Spoke reports whether anything at all came back.
func (b BootROM) Spoke() bool { return b.Banner != "" }

var (
	reBootChip  = regexp.MustCompile(`ESP-ROM:([a-z0-9]+)-`)
	reBootReset = regexp.MustCompile(`rst:(0x[0-9a-f]+ \([A-Z_]+\))`)
)

// ProbeBootROM pulses reset on a serial port and reads the boot banner.
//
// It is deliberately separate from probeOne: this REBOOTS the device. Doing
// that to a running Meshtastic node in the middle of carrying traffic would
// be rude, so the scanner never calls it and the caller asks for it only
// after a port has already reported itself silent.
func ProbeBootROM(device string, wait time.Duration) (BootROM, error) {
	if wait <= 0 {
		wait = 3 * time.Second
	}
	port, err := serial.Open(device, &serial.Mode{BaudRate: 115200})
	if err != nil {
		return BootROM{}, err
	}
	defer port.Close()

	// Hold both lines released first. Some adapters come up with DTR and RTS
	// asserted together, which on the auto-reset circuit holds the chip in
	// reset — the board then looks dead when it is merely being held down.
	_ = port.SetDTR(false)
	_ = port.SetRTS(false)
	time.Sleep(150 * time.Millisecond)
	_ = port.SetReadTimeout(200 * time.Millisecond)
	drain(port, 200*time.Millisecond) // discard whatever was mid-flight

	// The pulse: EN low, then released. IO0 stays high so the chip runs its
	// firmware instead of entering the download bootloader.
	_ = port.SetRTS(true)
	time.Sleep(150 * time.Millisecond)
	_ = port.SetRTS(false)

	text := drain(port, wait)
	out := BootROM{Banner: strings.TrimSpace(text)}
	if m := reBootChip.FindStringSubmatch(text); m != nil {
		out.Chip = m[1]
	}
	if m := reBootReset.FindStringSubmatch(text); m != nil {
		out.Reset = m[1]
	}
	return out, nil
}

func drain(port serial.Port, d time.Duration) string {
	var b strings.Builder
	buf := make([]byte, 512)
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		n, err := port.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return b.String()
}

// ParseBootBanner reads a banner that was captured elsewhere. Split out so
// the parsing is testable without hardware, which is the only part of this
// file that can be.
func ParseBootBanner(text string) BootROM {
	out := BootROM{Banner: strings.TrimSpace(text)}
	if m := reBootChip.FindStringSubmatch(text); m != nil {
		out.Chip = m[1]
	}
	if m := reBootReset.FindStringSubmatch(text); m != nil {
		out.Reset = m[1]
	}
	return out
}
