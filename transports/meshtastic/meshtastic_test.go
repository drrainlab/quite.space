package meshtastic

import (
	"bufio"
	"bytes"
	"testing"
	"time"
)

func startHub(t *testing.T) *Hub {
	t.Helper()
	h, err := StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// ---- Tests ----

func TestProtoRoundTrip(t *testing.T) {
	frame := EncodeDataPacket(Broadcast, 0, PortPrivateApp, []byte("hello mesh"), 42, 3, false)
	// The hub-side decoder must read back our own encoding.
	r := &reader{b: frame}
	tag, err := r.varint()
	if err != nil || tag>>3 != 1 {
		t.Fatalf("not a ToRadio packet: tag %d err %v", tag, err)
	}
	raw, err := r.bytes()
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := decodeMeshPacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.To != Broadcast || pkt.Portnum != PortPrivateApp ||
		!bytes.Equal(pkt.Payload, []byte("hello mesh")) || pkt.ID != 42 {
		t.Fatalf("round trip mangled: %+v", pkt)
	}
}

func TestDecodeSkipsUnknownFields(t *testing.T) {
	// A future FromRadio with extra fields around a packet must decode.
	inner := hubPacket(7, PortPrivateApp, []byte("x"))
	frame := appendVarintField(nil, 1, 12345) // FromRadio.id (unknown to us)
	frame = append(frame, inner...)
	frame = appendBytesField(frame, 13, []byte{0xde, 0xad}) // metadata blob
	msg, err := DecodeFromRadio(frame)
	if err != nil || msg.Packet == nil || msg.Packet.From != 7 {
		t.Fatalf("unknown fields broke decode: %+v %v", msg, err)
	}
}

func TestFrameResyncAfterGarbage(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("[INFO] boot complete\r\n") // serial debug noise
	buf.WriteByte(start1)                       // a lone 0x94 in noise
	buf.WriteString("more noise")
	writeFrame(&buf, []byte("real frame"))
	got, err := readFrame(bufio.NewReader(&buf))
	if err != nil || string(got) != "real frame" {
		t.Fatalf("resync failed: %q %v", got, err)
	}
}

func TestRadioHandshakeAndExchange(t *testing.T) {
	hub := startHub(t)
	a, err := DialTCP(hub.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := DialTCP(hub.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if a.NodeNum() == 0 || b.NodeNum() == 0 || a.NodeNum() == b.NodeNum() {
		t.Fatalf("node numbers wrong: %d %d", a.NodeNum(), b.NodeNum())
	}
	if err := a.Send([]byte("over the air")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if pkts := b.Poll(); len(pkts) > 0 {
			if string(pkts[0]) != "over the air" {
				t.Fatalf("payload mangled: %q", pkts[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("packet never arrived")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Oversized packets are refused locally (LoRa honesty, not silent drop).
	if err := a.Send(make([]byte, 300)); err == nil {
		t.Fatal("oversized packet accepted")
	}
}

func TestMeshCapabilitiesHonest(t *testing.T) {
	hub := startHub(t)
	r, err := DialTCP(hub.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	caps := r.Capabilities()
	if caps.Realtime {
		t.Fatal("LoRa claimed realtime")
	}
	if caps.MaxPayload == 0 || caps.MaxPayload > DataPayloadMax {
		t.Fatalf("payload cap dishonest: %d", caps.MaxPayload)
	}
}
