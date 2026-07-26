package meshtastic

import (
	"bufio"
	"net"
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
