package meshtastic

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

// A node hands its ENTIRE database to a connecting client: LoRa config, every
// channel, module config, and one node_info per node it has ever heard. On a
// busy mesh that is hundreds of frames.
//
// The handshake used to budget a fixed 12 seconds for all of it, which is a
// deadline that does not scale with the work. A radio sitting on a mesh of a
// hundred nodes then failed to connect — while the official Meshtastic CLI,
// talking to the very same device seconds earlier, was fine. That is the
// signature of a bug on our side, and it cost an evening of suspecting the
// hardware.
//
// The deadline is now IDLE-based: as long as the device keeps sending, it is
// making progress and we keep reading. Time out only when it goes quiet.
func TestHandshakeSurvivesALargeNodeDatabase(t *testing.T) {
	old := handshakeIdle
	handshakeIdle = 300 * time.Millisecond
	defer func() { handshakeIdle = old }()

	cli, dev := net.Pipe()
	go func() {
		defer dev.Close()
		br := bufio.NewReader(dev)
		frame, err := readFrame(br)
		if err != nil {
			return
		}
		r := &reader{b: frame}
		tag, err := r.varint()
		if err != nil {
			return
		}
		want, err := r.varint()
		if err != nil || int(tag>>3) != 3 {
			return
		}
		writeFrame(dev, hubMyInfo(0x4a307b54))
		// A hundred node_info frames, each arriving well within the idle
		// window but together taking far longer than any fixed budget.
		for range 100 {
			nodeInfo := appendBytesField(nil, 4, appendVarintField(nil, 1, 42))
			if writeFrame(dev, nodeInfo) != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		writeFrame(dev, hubConfigComplete(uint32(want)))
	}()

	start := time.Now()
	radio, err := Connect(cli, Options{})
	if err != nil {
		t.Fatalf("handshake failed after %v with a large node database: %v",
			time.Since(start), err)
	}
	defer radio.Close()
	if radio.NodeNum() != 0x4a307b54 {
		t.Errorf("node number = %08x", radio.NodeNum())
	}
	if elapsed := time.Since(start); elapsed < 2*time.Second {
		t.Errorf("the device streamed for ~2s but the handshake returned in %v "+
			"— it cannot have read the whole dump", elapsed)
	}
}

// The other half: a device that goes quiet mid-dump must NOT hang the caller
// forever. Idle-based means idle-bounded.
func TestHandshakeGivesUpOnASilentDevice(t *testing.T) {
	old := handshakeIdle
	handshakeIdle = 250 * time.Millisecond
	defer func() { handshakeIdle = old }()

	cli, dev := net.Pipe()
	go func() {
		defer dev.Close()
		br := bufio.NewReader(dev)
		if _, err := readFrame(br); err != nil {
			return
		}
		writeFrame(dev, hubMyInfo(1))
		// …and then say nothing at all, ever.
		time.Sleep(10 * time.Second)
	}()

	start := time.Now()
	_, err := Connect(cli, Options{})
	if err == nil {
		t.Fatal("a device that went silent mid-handshake reported success")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("took %v to give up on a silent device", elapsed)
	}
}

// THE THIRD DEVICE, and the one that got away.
//
// The two tests above cover a node that streams Meshtastic and a node that
// goes silent. The case neither covers is a device that TALKS STEADILY AND IS
// NOT MESHTASTIC — and that is not an exotic hypothetical, it is an RNode
// modem chattering KISS on the same serial line, which is what both boards in
// this project now run.
//
// readFrame scans a byte stream for a two-byte start marker. In somebody
// else's framing it finds one eventually, hands back a frame that will not
// decode, and — because the idle window is extended by every frame that
// arrives — the handshake resets its own patience and continues. Forever.
//
// Measured before the fix, on two real boards through the UI's own scan
// endpoint: no answer in 300 seconds, both serial ports still open and being
// read, held until the process was killed. The comment in serial.go is proud
// of having fixed the SILENT version of exactly this.
//
// The rule the fix encodes: progress may extend the window of patience; it
// may never extend the total.
func TestHandshakeGivesUpOnADeviceThatTalksButIsNotMeshtastic(t *testing.T) {
	oldIdle, oldTotal := handshakeIdle, handshakeTotal
	handshakeIdle, handshakeTotal = 200*time.Millisecond, 1500*time.Millisecond
	defer func() { handshakeIdle, handshakeTotal = oldIdle, oldTotal }()

	cli, dev := net.Pipe()
	stop := make(chan struct{})
	go func() {
		defer dev.Close()
		br := bufio.NewReader(dev)
		if _, err := readFrame(br); err != nil {
			return
		}
		// Well-formed FRAMES carrying bytes that are not FromRadio messages —
		// the shape a foreign protocol presents once readFrame has locked onto
		// a coincidental start marker. Steady, so the idle window never
		// expires: only a total can end this.
		for {
			select {
			case <-stop:
				return
			default:
			}
			if writeFrame(dev, []byte{0xC0, 0xFF, 0xEE, 0xC0}) != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	defer close(stop)

	start := time.Now()
	radio, err := Connect(cli, Options{})
	if err == nil {
		radio.Close()
		t.Fatal("a device that is not Meshtastic reported a successful handshake")
	}
	elapsed := time.Since(start)
	if elapsed > 4*time.Second {
		t.Fatalf("took %v to give up on a chattering non-Meshtastic device — "+
			"the idle window is still being extended by its noise", elapsed)
	}
	// And it must SAY which of the two failures this was, because the person
	// holding the board acts differently on each.
	if !strings.Contains(err.Error(), "none of its") {
		t.Errorf("gave up in %v but blamed the wrong thing: %v", elapsed, err)
	}
}
