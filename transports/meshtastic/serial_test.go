package meshtastic

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"go.bug.st/serial"
)

// silentPort is a device that opens and then says nothing — a board running
// factory firmware, a half-flashed one, a bootloader. go.bug.st reports an
// expired read timeout the way this does: no bytes, no error.
type silentPort struct{ serial.Port }

func (silentPort) Read(p []byte) (int, error)           { return 0, nil }
func (silentPort) SetReadTimeout(d time.Duration) error { return nil }
func (silentPort) Write(p []byte) (int, error)          { return len(p), nil }
func (silentPort) Close() error                         { return nil }

// A silent device must end the read, not the program.
//
// This is the shape of a real hang: probeOne opened a port, Connect called
// readFrame, and the read blocked forever because the handshake deadline is
// implemented with SetReadDeadline — which a TCP connection has and a serial
// port does not. The scanner promises each port "its own short deadline";
// before this wrapper existed, over USB it had none.
func TestASilentDeviceEndsTheReadInsteadOfHanging(t *testing.T) {
	conn := &serialConn{Port: silentPort{}}
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 16)
		_, err := conn.Read(buf)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("a timed-out read returned %v — bufio reads (0, nil) as "+
				"'no progress, try again' and retries a hundred times, which is "+
				"not a deadline", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a silent device blocked the read — this is the hang that stopped " +
			"the port scanner dead on any board not running Meshtastic")
	}
}

// Real data must pass through untouched: the wrapper only speaks up when
// there is nothing at all.
func TestTheWrapperDoesNotDisturbARealRead(t *testing.T) {
	conn := &serialConn{Port: talkingPort{}}
	buf := make([]byte, 4)
	n, err := conn.Read(buf)
	if err != nil || n != 4 || string(buf) != "meta" {
		t.Fatalf("read returned (%d, %v, %q)", n, err, buf[:n])
	}
}

type talkingPort struct{ serial.Port }

func (talkingPort) Read(p []byte) (int, error) { return copy(p, "meta"), nil }

// The handshake deadline must be per-call, not a package variable: the port
// scanner probes several devices at once, and a shared deadline made each
// probe's patience depend on what the others were doing at that instant.
func TestTheHandshakeDeadlineIsPerCall(t *testing.T) {
	got := Options{Idle: 1500 * time.Millisecond}.withDefaults()
	if got.Idle != 1500*time.Millisecond {
		t.Fatalf("Idle = %s, want the caller's own 1.5s", got.Idle)
	}
	if def := (Options{}).withDefaults(); def.Idle != handshakeIdle {
		t.Fatalf("a caller that asked for nothing got %s, want the %s default",
			def.Idle, handshakeIdle)
	}
}

// The three ways an attach can fail are three different questions, and only
// one of them is worth asking again. Retrying a mistyped device path would
// turn a typo into a node quietly waiting for hardware that does not exist —
// which is precisely what the synchronous first dial exists to prevent.
func TestOnlyASilentDeviceIsWorthAskingAgain(t *testing.T) {
	silent := []error{
		errors.New("meshtastic: handshake read after 0 frames: i/o timeout"),
		errors.New("meshtastic: the node went quiet during the config handshake after 3 frames (waited 8s)"),
	}
	for _, err := range silent {
		if !SilentHandshake(err) {
			t.Fatalf("%v should be retried — the device is there and said nothing", err)
		}
	}
	permanent := []error{
		nil,
		errors.New("open /dev/cu.nosuchthing: no such file or directory"),
		errors.New("Serial port busy"),
		errors.New("meshtastic: unknown message type 12"),
	}
	for _, err := range permanent {
		if SilentHandshake(err) {
			t.Fatalf("%v must fail on the spot — retrying it hides the real answer", err)
		}
	}
}

// A CONNECTED radio is allowed to be silent, and must not be torn down for it.
//
// The deadline that ends a stalled handshake is the same mechanism that, left
// in place afterwards, kills a healthy link: a LoRa segment can go minutes
// without a packet, and every idle window looked like a timeout. Measured on
// real hardware — a link that carried no traffic at all dropped and
// reconnected every ~40 seconds, tx=0 rx=0 throughout.
func TestAQuietLinkSurvivesOnceTheHandshakeIsOver(t *testing.T) {
	conn := &serialConn{Port: silentPort{}}

	// During the handshake, silence still ends the read.
	if _, err := conn.Read(make([]byte, 8)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("before the handshake finished, silence returned %v — a stalled "+
			"handshake must not block forever", err)
	}

	conn.HandshakeDone()

	// Afterwards it does not: the caller waits for a packet that may be
	// minutes away.
	n, err := conn.Read(make([]byte, 8))
	if err != nil || n != 0 {
		t.Fatalf("a connected radio with nothing to say returned (%d, %v) — that "+
			"tears down a working link every time the air goes quiet", n, err)
	}
}

// A `serial:` target arrives from the local API. That is behind the session
// token — but the token opens every route, and a route that opens an
// ARBITRARY PATH is a different kind of thing from one that reads a space.
func TestOnlyADeviceLookingPathIsOpened(t *testing.T) {
	for _, ok := range []string{
		"/dev/ttyUSB0", "/dev/ttyACM0",
		"/dev/cu.usbserial-0001", "/dev/tty.usbmodem24EC4A307B541",
		"/dev/serial/by-id/usb-1a86_USB_Serial-if00-port0", // the symlink form
		"COM3", "com12", `\\.\COM12`,
	} {
		if !looksLikeASerialDevice(ok) {
			t.Errorf("%q refused — that is a real radio path", ok)
		}
	}
	for _, bad := range []string{
		"/etc/passwd", "/etc/shadow",
		"/Users/somebody/.ssh/id_rsa",
		"/dev/../etc/passwd", // the reason traversal is refused by name
		"../../etc/passwd",
		"", "relay.example:7411", "tcp:127.0.0.1",
	} {
		if looksLikeASerialDevice(bad) {
			t.Errorf("%q accepted as a serial device", bad)
		}
	}
}

func TestOpeningANonDeviceSaysWhatIsWrong(t *testing.T) {
	_, err := OpenSerial("/etc/passwd")
	if err == nil {
		t.Fatal("a regular file was opened as a radio")
	}
	if !strings.Contains(err.Error(), "not a serial device path") {
		t.Errorf("refused with %q — the reason should name the shape, not the "+
			"termios failure that would otherwise have happened by luck", err)
	}
}
