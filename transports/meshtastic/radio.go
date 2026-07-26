// Package meshtastic is the T6 transport (plan §19, M1.7): a Terminal
// endpoint projected onto a Meshtastic mesh through the official Client
// API stream protocol (serial or TCP), on a private app port.
//
// Honesty notes, stated up front:
//   - LoRa airtime is scarce: this endpoint declares MaxPayload≈200B and
//     no realtime; the runtime syncs over it at a slow cadence. Full-log
//     sync over LoRa works but is slow by nature — that is the physics,
//     not a bug to hide (invariant §2.6).
//   - Delivery: the mesh gives no end-to-end proof; nothing here ever
//     reports more than handed_to_transport (ADR-007/008). Store & Forward
//     nodes may or may not replay history — their presence is never treated
//     as guaranteed delivery.
//   - Privacy: our payloads stay epoch-encrypted end-to-end (ADR-005);
//     envelope headers are visible to mesh channel members, exactly like
//     on any transport.
package meshtastic

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/transports"
)

// Stream framing constants (Meshtastic serial/TCP stream API).
const (
	start1     = 0x94
	start2     = 0xC3
	maxFrame   = 512
	wakeLen    = 32 // serial wake: 32× START2
	handshakeT = 12 * time.Second
	heartbeatT = 30 * time.Second
)

// Options configure the adapter.
type Options struct {
	Portnum    uint32 // default PortPrivateApp
	Channel    uint32 // mesh channel index, default 0 (primary)
	HopLimit   uint32 // default 3
	MaxPayload int    // default 200 (≤ DataPayloadMax)
	Serial     bool   // send the serial wake sequence before handshake
}

func (o Options) withDefaults() Options {
	if o.Portnum == 0 {
		o.Portnum = PortPrivateApp
	}
	if o.HopLimit == 0 {
		o.HopLimit = 3
	}
	if o.MaxPayload == 0 || o.MaxPayload > DataPayloadMax {
		o.MaxPayload = 200
	}
	return o
}

// Radio is a connected Meshtastic device exposed as a transports.Endpoint.
type Radio struct {
	opts Options
	conn io.ReadWriteCloser

	mu      sync.Mutex
	inbox   [][]byte
	closed  bool
	err     error
	nodeNum uint32
	rxCount int
	txCount int
	cfg     NodeConfig
}

// DialTCP connects to a WiFi/ethernet node or meshtasticd (port 4403).
func DialTCP(addr string) (*Radio, error) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "4403")
	}
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	return Connect(c, Options{})
}

// Connect performs the config handshake over an open stream and starts the
// reader. Use DialTCP/OpenSerial unless you carry your own connection.
func Connect(conn io.ReadWriteCloser, opts Options) (*Radio, error) {
	r := &Radio{opts: opts.withDefaults(), conn: conn}
	if r.opts.Serial {
		wake := make([]byte, wakeLen)
		for i := range wake {
			wake[i] = start2
		}
		if _, err := conn.Write(wake); err != nil {
			conn.Close()
			return nil, err
		}
	}
	var idBytes [4]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		conn.Close()
		return nil, err
	}
	wantID := binary.BigEndian.Uint32(idBytes[:])
	if err := writeFrame(conn, EncodeWantConfig(wantID)); err != nil {
		conn.Close()
		return nil, err
	}

	// Read config stream until config_complete_id echoes our id.
	br := bufio.NewReaderSize(conn, 2*maxFrame)
	deadline := time.Now().Add(handshakeT)
	type deadliner interface{ SetReadDeadline(time.Time) error }
	if d, ok := conn.(deadliner); ok {
		d.SetReadDeadline(deadline)
	}
	for {
		if time.Now().After(deadline) {
			conn.Close()
			return nil, errors.New("meshtastic: config handshake timed out")
		}
		frame, err := readFrame(br)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("meshtastic: handshake read: %w", err)
		}
		msg, err := DecodeFromRadio(frame)
		if err != nil {
			continue // tolerate frames we cannot parse during config dump
		}
		if msg.MyNodeNum != nil {
			r.nodeNum = *msg.MyNodeNum
		}
		r.absorb(msg)
		if msg.ConfigCompleteID != nil && *msg.ConfigCompleteID == wantID {
			break
		}
	}
	if d, ok := conn.(deadliner); ok {
		d.SetReadDeadline(time.Time{})
	}

	go r.readLoop(br)
	go r.heartbeatLoop()
	return r, nil
}

// absorb folds a frame's configuration content into the node's picture of
// itself. Called during the handshake and for the life of the link: a node
// re-sends the affected message when its configuration changes, so the
// diagnostic keeps up with a radio someone is reconfiguring at the bench.
func (r *Radio) absorb(msg *FromRadioMsg) {
	if len(msg.Config) == 0 && len(msg.Skipped) == 0 && msg.MyNodeNum == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if msg.MyNodeNum != nil {
		r.cfg.NodeNum = *msg.MyNodeNum
	}
	for _, f := range msg.Config {
		r.cfg.absorbConfig(f.Field, f.Raw)
	}
	for _, field := range msg.Skipped {
		if r.cfg.Unrecognised == nil {
			r.cfg.Unrecognised = map[int]int{}
		}
		r.cfg.Unrecognised[field]++
	}
}

// Config reports what the node told us about itself. Fields the node never
// reported are absent, not zero — see config.go.
func (r *Radio) Config() NodeConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.cfg
	out.Channels = append([]ChannelInfo(nil), r.cfg.Channels...)
	out.Unrecognised = make(map[int]int, len(r.cfg.Unrecognised))
	for k, v := range r.cfg.Unrecognised {
		out.Unrecognised[k] = v
	}
	return out
}

func (r *Radio) readLoop(br *bufio.Reader) {
	for {
		frame, err := readFrame(br)
		if err != nil {
			r.fail(err)
			return
		}
		msg, err := DecodeFromRadio(frame)
		if err != nil {
			continue
		}
		r.absorb(msg)
		if msg.Packet == nil {
			continue
		}
		p := msg.Packet
		if p.Portnum != r.opts.Portnum || p.Encrypted || len(p.Payload) == 0 {
			continue // not ours, or the radio could not decrypt for us
		}
		if r.nodeNum != 0 && p.From == r.nodeNum {
			continue // our own broadcast echoed back
		}
		r.mu.Lock()
		r.inbox = append(r.inbox, p.Payload)
		r.rxCount++
		r.mu.Unlock()
	}
}

func (r *Radio) heartbeatLoop() {
	t := time.NewTicker(heartbeatT)
	defer t.Stop()
	for range t.C {
		r.mu.Lock()
		closed := r.closed
		r.mu.Unlock()
		if closed {
			return
		}
		if err := writeFrame(r.conn, EncodeHeartbeat()); err != nil {
			r.fail(err)
			return
		}
	}
}

func (r *Radio) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		r.err = err
	}
	r.conn.Close()
}

// ---- transports.Endpoint ----

// Send broadcasts one opaque packet on our port.
func (r *Radio) Send(pkt []byte) error {
	if len(pkt) == 0 || len(pkt) > r.opts.MaxPayload {
		return fmt.Errorf("meshtastic: packet length %d out of range (max %d)", len(pkt), r.opts.MaxPayload)
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("meshtastic: radio disconnected")
	}
	r.txCount++
	r.mu.Unlock()
	var idBytes [4]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return err
	}
	frame := EncodeDataPacket(Broadcast, r.opts.Channel, r.opts.Portnum,
		pkt, binary.BigEndian.Uint32(idBytes[:]), r.opts.HopLimit, false)
	if err := writeFrame(r.conn, frame); err != nil {
		r.fail(err)
		return err
	}
	return nil
}

// Poll drains received packets.
func (r *Radio) Poll() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.inbox
	r.inbox = nil
	return out
}

// Capabilities: tiny payloads, not realtime, transport can prove nothing
// beyond having handed the packet to the radio.
func (r *Radio) Capabilities() transports.Capabilities {
	return transports.Capabilities{MaxPayload: r.opts.MaxPayload, Realtime: false, Ack: transports.AckNone}
}

// Closed reports whether the link died, and why.
func (r *Radio) Closed() (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed, r.err
}

// NodeNum is the radio's mesh address (diagnostics).
func (r *Radio) NodeNum() uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nodeNum
}

// Stats reports packet counters (diagnostics).
func (r *Radio) Stats() (tx, rx int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.txCount, r.rxCount
}

// Close disconnects.
func (r *Radio) Close() error {
	r.fail(errors.New("meshtastic: closed"))
	return nil
}

// ---- Stream framing ----

func writeFrame(w io.Writer, payload []byte) error {
	if len(payload) > maxFrame {
		return fmt.Errorf("meshtastic: frame %d exceeds %d", len(payload), maxFrame)
	}
	buf := make([]byte, 4+len(payload))
	buf[0], buf[1] = start1, start2
	binary.BigEndian.PutUint16(buf[2:4], uint16(len(payload)))
	copy(buf[4:], payload)
	_, err := w.Write(buf)
	return err
}

// readFrame scans to the next START1 START2 marker (the serial line may
// interleave ASCII debug logs) and reads one length-prefixed frame.
func readFrame(br *bufio.Reader) ([]byte, error) {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != start1 {
			continue
		}
		b2, err := br.ReadByte()
		if err != nil {
			return nil, err
		}
		if b2 != start2 {
			continue
		}
		var lenBuf [2]byte
		if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
			return nil, err
		}
		n := binary.BigEndian.Uint16(lenBuf[:])
		if n == 0 || int(n) > maxFrame {
			continue // garbage that happened to look like a header; resync
		}
		frame := make([]byte, n)
		if _, err := io.ReadFull(br, frame); err != nil {
			return nil, err
		}
		return frame, nil
	}
}
