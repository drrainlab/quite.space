package meshtastic

import (
	"os"
	"strings"
	"sync/atomic"

	"go.bug.st/serial"
)

// OpenSerial connects to a USB-attached node (e.g. /dev/tty.usbserial-XXXX
// on macOS, /dev/ttyUSB0 or /dev/ttyACM0 on Linux). Meshtastic's serial
// console speaks the same stream protocol at 115200 baud.
func OpenSerial(device string) (*Radio, error) {
	return openSerial(device, Options{})
}

func openSerial(device string, opts Options) (*Radio, error) {
	port, err := serial.Open(device, &serial.Mode{BaudRate: 115200})
	if err != nil {
		return nil, err
	}
	opts = opts.withDefaults()
	// THE HANDSHAKE DEADLINE ONLY EXISTS IF IT IS BUILT HERE.
	//
	// Connect bounds the config dump by pushing a read deadline forward on
	// every frame — but it does that through SetReadDeadline, which a TCP
	// connection has and a serial port does not. So over the network the
	// deadline worked, and over USB it silently did not exist: a device that
	// opens and then says nothing blocked the very first read forever.
	//
	// That is not a rare case. It is every board not running Meshtastic —
	// factory firmware, a half-flashed device, a bootloader — and it hung
	// the port scanner, whose own promise is that each port gets "its own
	// short deadline".
	if err := port.SetReadTimeout(opts.Idle); err != nil {
		port.Close()
		return nil, err
	}
	opts.Serial = true
	return Connect(&serialConn{Port: port}, opts)
}

// serialConn turns an expired read timeout into an ERROR — but only while
// the handshake is still running.
//
// go.bug.st reports a timeout as (0, nil), which bufio reads as "no progress,
// try again" and retries a hundred times before giving up. A hundred silent
// windows is not a deadline. Saying so once, in the form the caller already
// handles, is.
//
// AFTER the handshake the rule inverts, and getting this wrong killed working
// radios: a connected node is SUPPOSED to be quiet. It speaks when a packet
// arrives, and a LoRa segment can be silent for minutes. Left in place, the
// same deadline tore the link down every idle window — a healthy radio with
// nothing to say, reported as a timeout, reconnected, and torn down again.
// So this mirrors exactly what the TCP path already does when the config
// dump ends: it drops the deadline and waits as long as it takes.
type serialConn struct {
	serial.Port
	live atomic.Bool
}

func (s *serialConn) Read(p []byte) (int, error) {
	n, err := s.Port.Read(p)
	if n == 0 && err == nil && !s.live.Load() {
		return 0, os.ErrDeadlineExceeded
	}
	return n, err
}

// HandshakeDone lifts the deadline for the life of the link. Named the same
// way for both transports so Connect can end the handshake once, for either.
func (s *serialConn) HandshakeDone() {
	s.live.Store(true)
	_ = s.Port.SetReadTimeout(serial.NoTimeout)
}

// ListSerialPorts enumerates candidate serial devices (diagnostics/UI).
func ListSerialPorts() ([]string, error) {
	return serial.GetPortsList()
}

// SilentHandshake reports whether an open failed because the device said
// NOTHING, as opposed to not being there or being held by somebody else.
//
// The three are different questions with different answers. A wrong device
// path and a busy port are permanent for this attempt and their messages are
// already actionable; retrying them would only hide a typo. A device that
// opened and then stayed quiet is the one case where trying again is the
// right move — measured on a native-USB ESP32-S3, where a single short
// session answers roughly a third of the time and a long-lived connection,
// once established, holds fine.
func SilentHandshake(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "handshake read") ||
		strings.Contains(s, "went quiet during the config handshake")
}
