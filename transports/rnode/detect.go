// Asking a board what it is, without telling it anything.
//
// This exists because the port scan could not name an RNode. It reported one
// as a device "talking, but not Meshtastic" — true, and not enough to offer
// somebody an Attach button for the radio sitting on their desk.
package rnode

import (
	"fmt"
	"time"

	"go.bug.st/serial"
)

// detectSettle is the wait after opening the port. Opening RESETS the MCU on
// these boards, so a command sent immediately goes to a device that is still
// booting — the defect that once made a working radio look dead. Shorter than
// Open's settle because a probe may answer late without costing correctness:
// the worst case is one unrecognised board, not a misconfigured one.
const detectSettle = 1200 * time.Millisecond

// Detect reports whether an RNode modem is on this serial port.
//
// IT SETS NOTHING, and that is the whole design. Every other command in this
// protocol is a SETTER with no ask form — "asking" the frequency by sending
// zero sets the frequency to zero, which is how a board got broken in the
// middle of a diagnosis. DETECT is one of the two exceptions: a query with a
// reply of its own (0x73 out, 0x46 back). So a probe can be run against any
// port on the machine, including ports belonging to somebody else's hardware,
// without changing a thing.
//
// A false answer is never an accusation: it means only that nothing replied
// in the window. The port may hold another radio, a bootloader, or a board
// still coming up after a reset.
func Detect(device string, wait time.Duration) (bool, error) {
	if wait <= 0 {
		wait = 900 * time.Millisecond
	}
	p, err := serial.Open(device, &serial.Mode{BaudRate: 115200})
	if err != nil {
		return false, fmt.Errorf("rnode: %w", err)
	}
	defer p.Close()
	// A read timeout well under the budget, so the deadline below is checked
	// often rather than overshot by a whole window. The lesson is one file
	// over in the Meshtastic driver: a per-read timeout is not a bound on
	// anything, so the loop owns the deadline and the timeout only sets how
	// promptly it notices.
	if err := p.SetReadTimeout(150 * time.Millisecond); err != nil {
		return false, err
	}
	time.Sleep(detectSettle)

	if _, err := p.Write([]byte{fend, cmdDetect, detectReq, fend}); err != nil {
		return false, err
	}

	var parser kissParser
	found := false
	buf := make([]byte, 512)
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) && !found {
		n, err := p.Read(buf)
		if err != nil {
			return false, err
		}
		if n == 0 {
			continue // the read timeout, not an answer either way
		}
		parser.feed(buf[:n], func(cmd byte, payload []byte) {
			if cmd == cmdDetect && len(payload) == 1 && payload[0] == detectResp {
				found = true
			}
		})
	}
	return found, nil
}
