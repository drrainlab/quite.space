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
	heartbeatT = 30 * time.Second
)

// handshakeIdle bounds how long the config dump may go QUIET, not how long it
// may take in total.
//
// A node hands a connecting client its entire database: LoRa config, every
// channel, module config, and one node_info per node it has ever heard. On a
// busy mesh that is hundreds of frames, and a fixed total budget fails on
// exactly the radios that matter most — the ones with real neighbours. A
// device streaming steadily is making progress; only silence means it is not.
//
// A var so tests can shrink it.
var handshakeIdle = 8 * time.Second

// Options configure the adapter.
type Options struct {
	Portnum    uint32 // default PortPrivateApp
	Channel    uint32 // mesh channel index, default 0 (primary)
	HopLimit   uint32 // default 3
	MaxPayload int    // default 200 (≤ DataPayloadMax)
	Serial     bool   // send the serial wake sequence before handshake
	// Reliable asks the firmware to retransmit what is not acknowledged
	// (MeshPacket.want_ack). For a BROADCAST — which is what this transport
	// sends — Meshtastic suppresses the ordinary ack to avoid flooding, and
	// instead treats a REBROADCAST BY ANOTHER NODE as an implicit
	// acknowledgement; hearing none, the sender times out and retransmits.
	//
	// On a shared channel this is the difference between arriving and not:
	// measured loss there ran 70-90%, and losing one fragment loses the
	// whole message. It is not free — a retransmission is more airtime, on
	// the very channel that was already congested — so it is a decision,
	// not a default buried in a constant.
	//
	// It buys RETRIES, not proof. Capabilities.Ack stays AckNone: nothing
	// here observes an acknowledgement or reports one upward, and a
	// transport must never claim a delivery it did not see (ADR-007).
	Reliable bool
	// Idle bounds how long the config dump may go QUIET. Zero means the
	// package default. It lives here rather than in a package variable
	// because the port scanner probes several devices CONCURRENTLY, and a
	// shared variable made each probe's deadline depend on what the others
	// were doing at that moment.
	Idle time.Duration
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
	if o.Idle <= 0 {
		o.Idle = handshakeIdle
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
	// txRefused counts packets the FIRMWARE declined to queue. It is the
	// number that was missing: txCount says what we wrote to the port,
	// which on a full queue is not what went on the air.
	txRefused int
	// queue is the radio's last report on its own outgoing queue, and
	// inFlight is what we have handed over since that report. Credit is the
	// difference — the report ages the moment we send against it.
	queue    *QueueStatus
	inFlight int
	// sentIDs remembers the ids of packets we asked to be delivered
	// reliably, so a Routing report can be matched to OUR packet rather
	// than to somebody else's traffic on the same channel. Bounded: the
	// firmware answers within its retry window or not at all.
	sentIDs  map[uint32]struct{}
	sentRing []uint32
	// txAcked and txGaveUp are the outcome of reliable sends, as the
	// FIRMWARE reported them. Until these existed, retransmission was
	// invisible: tx counted what we wrote, and whether the radio then
	// tried once or five times or gave up was not observable at all.
	txAcked  int
	txGaveUp int
	cfg      NodeConfig
}

// DialTCP connects to a WiFi/ethernet node or meshtasticd (port 4403).
func DialTCP(addr string) (*Radio, error) { return dialTCP(addr, Options{}) }

func dialTCP(addr string, opts Options) (*Radio, error) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "4403")
	}
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	return Connect(c, opts)
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

	// Read the config stream until config_complete_id echoes our id. The
	// deadline is pushed forward on every frame that arrives: the dump is as
	// long as the node's database, and only silence means it has stalled.
	br := bufio.NewReaderSize(conn, 2*maxFrame)
	type deadliner interface{ SetReadDeadline(time.Time) error }
	d, canDeadline := conn.(deadliner)
	extend := func() {
		if canDeadline {
			d.SetReadDeadline(time.Now().Add(r.opts.Idle))
		}
	}
	extend()
	deadline := time.Now().Add(r.opts.Idle)
	frames := 0
	for {
		if time.Now().After(deadline) {
			conn.Close()
			return nil, fmt.Errorf("meshtastic: the node went quiet during the "+
				"config handshake after %d frames (waited %s)", frames, r.opts.Idle)
		}
		frame, err := readFrame(br)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("meshtastic: handshake read after %d frames: %w",
				frames, err)
		}
		// Progress: the device is talking, so give it another window.
		frames++
		deadline = time.Now().Add(r.opts.Idle)
		extend()
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
	// The handshake is over: from here the node is allowed to be silent, and
	// a deadline left in place would kill an idle-but-healthy link. TCP drops
	// its deadline; serial lifts its read timeout the same way.
	if d, ok := conn.(deadliner); ok {
		d.SetReadDeadline(time.Time{})
	}
	if h, ok := conn.(interface{ HandshakeDone() }); ok {
		h.HandshakeDone()
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
	if msg.Queue != nil {
		r.mu.Lock()
		r.queue = msg.Queue
		// The report is a fresh count of free entries, so what we thought
		// was in flight is now accounted for in it.
		r.inFlight = 0
		if !msg.Queue.Accepted() {
			r.txRefused++
		}
		r.mu.Unlock()
	}
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
	// LoRa and Device are POINTERS into live state. Copying the struct copies
	// the pointer, so a caller adjusting one field of what it thinks is its
	// own snapshot would be writing into the radio's picture of itself,
	// outside this mutex and from whichever goroutine happened to hold it.
	if r.cfg.LoRa != nil {
		l := *r.cfg.LoRa
		out.LoRa = &l
	}
	if r.cfg.Device != nil {
		d := *r.cfg.Device
		out.Device = &d
	}
	// Same for the retained bytes — a patch writes a new slice, but nothing
	// stops a caller from editing one in place.
	out.LoRaRaw = cloneRaw(r.cfg.LoRaRaw)
	out.DeviceRaw = cloneRaw(r.cfg.DeviceRaw)
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
		if p.Portnum == PortRoutingApp && p.RequestID != 0 {
			r.noteRouting(p)
			continue // an outcome, not a payload
		}
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
	r.inFlight++
	r.mu.Unlock()
	var idBytes [4]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return err
	}
	packetID := binary.BigEndian.Uint32(idBytes[:])
	if r.opts.Reliable {
		r.mu.Lock()
		r.rememberSent(packetID)
		r.mu.Unlock()
	}
	frame := EncodeDataPacket(Broadcast, r.opts.Channel, r.opts.Portnum,
		pkt, packetID, r.opts.HopLimit, r.opts.Reliable)
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

// noteRouting records the fate the firmware reported for one of our
// packets. Reports about anyone else's traffic are ignored: on a shared
// channel most Routing packets are not about us, and counting them would
// turn a neighbour's failures into ours.
func (r *Radio) noteRouting(p *RxPacket) {
	code, ok := DecodeRoutingError(p.Payload)
	if !ok {
		return // a route request or reply, not an outcome
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, mine := r.sentIDs[p.RequestID]; !mine {
		return
	}
	delete(r.sentIDs, p.RequestID)
	switch code {
	case RoutingNone:
		r.txAcked++
	default:
		// MAX_RETRANSMIT and every other refusal share a meaning here: the
		// firmware stopped trying. The specific code is not mapped onto a
		// name this build might have wrong.
		r.txGaveUp++
	}
}

// rememberSent records a packet id we expect a Routing answer for, keeping
// the set bounded by age of issue.
func (r *Radio) rememberSent(id uint32) {
	if r.sentIDs == nil {
		r.sentIDs = map[uint32]struct{}{}
	}
	r.sentIDs[id] = struct{}{}
	r.sentRing = append(r.sentRing, id)
	if len(r.sentRing) > maxTrackedSends {
		drop := r.sentRing[0]
		r.sentRing = r.sentRing[1:]
		delete(r.sentIDs, drop)
	}
}

// maxTrackedSends bounds the outstanding-outcome set. The firmware answers
// within its retry window; anything older is not coming.
const maxTrackedSends = 64

// Delivery reports what the FIRMWARE said became of the packets we asked it
// to deliver reliably: acknowledged, given up on, and still outstanding.
//
// With want_ack off these stay zero, which is honest — nothing was asked,
// so nothing was reported.
func (r *Radio) Delivery() (acked, gaveUp, outstanding int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.txAcked, r.txGaveUp, len(r.sentIDs)
}

// Credit reports how many more packets this radio will take, from what the
// firmware itself last said (mesh.proto QueueStatus, FromRadio field 11).
//
// The report is a snapshot: every packet handed over since then has not been
// counted by it, so the honest number is free-minus-in-flight. A radio that
// has not reported yet answers Known=false, which lets a sender proceed —
// on most links the first message is what makes the radio speak at all.
//
// RetryAfter is deliberately conservative. LongFast spends around two
// seconds of airtime per packet, so a queue that is full now is not going to
// be empty in a hundred milliseconds, and asking again that soon just burns
// wakeups.
func (r *Radio) Credit() transports.Credit {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.queue == nil {
		return transports.Credit{Known: false}
	}
	free := int(r.queue.Free) - r.inFlight
	if free < 0 {
		free = 0
	}
	c := transports.Credit{Packets: free, Known: true}
	if free == 0 {
		c.RetryAfter = queueRetry
	}
	return c
}

// queueRetry is how long a full radio queue is given before being asked
// again — a little over the airtime of one LongFast packet, so the answer
// has had a chance to change.
const queueRetry = 3 * time.Second

// QueueState reports the radio's own view of its outgoing queue, and how
// many packets it has refused (diagnostics).
func (r *Radio) QueueState() (free, maxlen, refused int, known bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.queue == nil {
		return 0, 0, r.txRefused, false
	}
	return int(r.queue.Free), int(r.queue.Maxlen), r.txRefused, true
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
