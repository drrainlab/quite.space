package meshtastic

import (
	"bufio"
	"net"
	"sync"
	"testing"
	"time"
)

// recordingDevice is a fake node that answers the handshake and then keeps
// every MeshPacket the client sends, so a test can assert what actually went
// on the air rather than what the client believed it configured.
type recordingDevice struct {
	mu   sync.Mutex
	sent []*RxPacket
}

func (d *recordingDevice) packets() []*RxPacket {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*RxPacket(nil), d.sent...)
}

func dialRecording(t *testing.T, opts Options) (*Radio, *recordingDevice) {
	t.Helper()
	dev := &recordingDevice{}
	cli, wire := net.Pipe()
	go func() {
		defer wire.Close()
		br := bufio.NewReader(wire)
		for {
			frame, err := readFrame(br)
			if err != nil {
				return
			}
			r := &reader{b: frame}
			for !r.done() {
				tag, err := r.varint()
				if err != nil {
					return
				}
				field, wt := int(tag>>3), int(tag&7)
				switch {
				case field == 3 && wt == wireVarint: // want_config_id
					want, err := r.varint()
					if err != nil {
						return
					}
					writeFrame(wire, hubMyInfo(0x4a307b54))
					writeFrame(wire, hubConfigComplete(uint32(want)))
				case field == 1 && wt == wireBytes: // ToRadio.packet
					raw, err := r.bytes()
					if err != nil {
						return
					}
					pkt, err := decodeMeshPacket(raw)
					if err != nil {
						continue
					}
					dev.mu.Lock()
					dev.sent = append(dev.sent, pkt)
					dev.mu.Unlock()
				default:
					if err := r.skip(wt); err != nil {
						return
					}
				}
			}
		}
	}()
	radio, err := Connect(cli, opts)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { radio.Close() })
	return radio, dev
}

func waitForPackets(t *testing.T, dev *recordingDevice, n int) []*RxPacket {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := dev.packets(); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d packets reached the device, want %d", len(dev.packets()), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The channel index must reach the WIRE, not merely be stored in Options.
// Transmitting on channel 0 means transmitting on the node's PRIMARY channel,
// which on a real device is very often the public default-key one that every
// Meshtastic user in range shares. Getting this wrong is not a cosmetic bug:
// it puts our traffic on a stranger's channel and spends their airtime.
func TestTransmitChannelReachesTheWire(t *testing.T) {
	radio, dev := dialRecording(t, Options{Channel: 3})
	if err := radio.Send([]byte("on the private channel")); err != nil {
		t.Fatal(err)
	}
	pkt := waitForPackets(t, dev, 1)[0]
	if pkt.Channel != 3 {
		t.Fatalf("transmitted on channel %d, want 3 — the configured channel "+
			"never reached the packet", pkt.Channel)
	}
	if string(pkt.Payload) != "on the private channel" {
		t.Errorf("payload mangled: %q", pkt.Payload)
	}
}

// The default is channel 0 and that is deliberate, but it is a decision worth
// pinning: a silent change of default would move everyone's traffic onto a
// different channel with no error anywhere.
func TestDefaultChannelIsPrimary(t *testing.T) {
	radio, dev := dialRecording(t, Options{})
	if err := radio.Send([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if pkt := waitForPackets(t, dev, 1)[0]; pkt.Channel != 0 {
		t.Fatalf("default transmit channel is %d, want 0 (PRIMARY)", pkt.Channel)
	}
	if got := radio.Capabilities().MaxPayload; got != 200 {
		t.Errorf("MaxPayload default changed to %d", got)
	}
}

// Capabilities and the reported channel must survive a reconnect: the channel
// belongs to the carrier configuration, not to whichever device object is
// behind the link at this instant.
func TestSupervisedLinkKeepsItsChannel(t *testing.T) {
	hub, err := StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	s, err := Supervise("tcp:"+hub.Addr(), Options{Channel: 2}, fastBackoff())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if got := s.Channel(); got != 2 {
		t.Fatalf("Channel() = %d, want 2", got)
	}
	hub.DropAll()
	deadline := time.Now().Add(5 * time.Second)
	for s.Status().Reconnects == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the link never came back")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := s.Channel(); got != 2 {
		t.Fatalf("after a reconnect the link reports channel %d, want 2", got)
	}
}
