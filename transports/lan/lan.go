// Package lan is the T2 transport (plan §19, M1.1): peer discovery on the
// local network and direct connections, with no coordinator.
//
// Honest layering (ADR-007, ADR-008): the TLS 1.3 session uses ephemeral
// self-signed certificates and therefore claims NOTHING about who the peer
// is — it only hides traffic from passive LAN observers. Authenticity comes
// from event signatures, confidentiality from epoch encryption (ADR-005).
// The transport's capability declaration says exactly that.
package lan

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports"
)

const (
	maxPacket = 1 << 20
	// multicast group for announces; any node may listen.
	MulticastAddr = "239.255.71.80:47180"
)

// ---- Framed, TLS-wrapped Endpoint ----

// Conn adapts a stream connection to the transports.Endpoint contract:
// Send writes a length-prefixed packet; a reader goroutine buffers inbound
// packets for Poll.
type Conn struct {
	c      net.Conn
	mu     sync.Mutex
	inbox  [][]byte
	closed bool
	err    error
}

func newConn(c net.Conn) *Conn {
	cc := &Conn{c: c}
	go cc.readLoop()
	return cc
}

func (c *Conn) readLoop() {
	header := make([]byte, 4)
	for {
		if _, err := io.ReadFull(c.c, header); err != nil {
			c.fail(err)
			return
		}
		n := binary.BigEndian.Uint32(header)
		if n == 0 || n > maxPacket {
			c.fail(fmt.Errorf("lan: bad packet length %d", n))
			return
		}
		pkt := make([]byte, n)
		if _, err := io.ReadFull(c.c, pkt); err != nil {
			c.fail(err)
			return
		}
		c.mu.Lock()
		c.inbox = append(c.inbox, pkt)
		c.mu.Unlock()
	}
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		c.err = err
	}
	c.c.Close()
}

// Send writes one packet to the peer.
func (c *Conn) Send(pkt []byte) error {
	if len(pkt) == 0 || len(pkt) > maxPacket {
		return fmt.Errorf("lan: packet length %d out of range", len(pkt))
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("lan: connection closed")
	}
	c.mu.Unlock()
	buf := make([]byte, 4+len(pkt))
	binary.BigEndian.PutUint32(buf, uint32(len(pkt)))
	copy(buf[4:], pkt)
	_, err := c.c.Write(buf)
	if err != nil {
		c.fail(err)
	}
	return err
}

// Poll drains buffered inbound packets.
func (c *Conn) Poll() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.inbox
	c.inbox = nil
	return out
}

// Capabilities: stream transport, realtime, transport-level acks only.
func (c *Conn) Capabilities() transports.Capabilities {
	return transports.Capabilities{MaxPayload: 0, Realtime: true, Ack: transports.AckTransport}
}

// Closed reports whether the connection died, and why.
func (c *Conn) Closed() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed, c.err
}

// Close shuts the connection down.
func (c *Conn) Close() error { return c.c.Close() }

// ---- Node: listener + dialer with ephemeral TLS ----

// Node is one LAN presence: a TLS listener and a dialer sharing an
// ephemeral certificate.
type Node struct {
	tlsCert  tls.Certificate
	listener net.Listener
}

// NewNode generates the ephemeral session certificate.
func NewNode() (*Node, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}
	return &Node{tlsCert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}}, nil
}

func (n *Node) serverConfig() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{n.tlsCert}, MinVersion: tls.VersionTLS13}
}

func (n *Node) clientConfig() *tls.Config {
	// InsecureSkipVerify is deliberate and documented: the session makes no
	// identity claim (see package comment). Do not "fix" this by trusting
	// the certificate — identity lives in event signatures.
	return &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}
}

// Listen starts accepting direct connections; each is handed to onConn.
// Returns the bound TCP port.
func (n *Node) Listen(addr string, onConn func(*Conn)) (int, error) {
	l, err := tls.Listen("tcp", addr, n.serverConfig())
	if err != nil {
		return 0, err
	}
	n.listener = l
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			onConn(newConn(c))
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Close stops listening.
func (n *Node) Close() error {
	if n.listener != nil {
		return n.listener.Close()
	}
	return nil
}

// Dial opens a direct connection to a peer.
func (n *Node) Dial(addr string) (*Conn, error) {
	c, err := tls.Dial("tcp", addr, n.clientConfig())
	if err != nil {
		return nil, err
	}
	return newConn(c), nil
}

// ---- Discovery (privacy-preserving hints) ----

// Hint derives the rotating discovery token for a terminal in a time
// bucket. Announcing the hint instead of the terminal id means an observer
// cannot enumerate spaces; a peer that already knows the terminal id
// computes the same hint and matches (vision: a private Terminal cannot be
// discovered without an invite). Honest limit: an observer who already
// knows a terminal id CAN confirm its presence nearby.
func Hint(terminal id.TerminalID, bucket uint64) []byte {
	h := sha256.New()
	h.Write([]byte("qp-lan-hint-v0:"))
	h.Write(terminal[:])
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], bucket)
	h.Write(b[:])
	return h.Sum(nil)[:16]
}

// Bucket returns the current hint rotation bucket (6-hour windows).
func Bucket(nowUnix uint64) uint64 { return nowUnix / (6 * 3600) }

// Announcement is one node's discovery beacon.
type Announcement struct {
	Addr  string // host:port to dial (host filled by the receiver)
	Port  int
	Hints [][]byte
	Nonce uint64 // lets a node ignore its own beacons
}

// announce payload key table v0.
const (
	annKeyPort  = 1
	annKeyHints = 2
	annKeyNonce = 3
)

func encodeAnnounce(port int, hints [][]byte, nonce uint64) []byte {
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, annKeyPort)
	buf = codec.AppendUint(buf, uint64(port))
	buf = codec.AppendUint(buf, annKeyHints)
	buf = codec.AppendArray(buf, len(hints))
	for _, h := range hints {
		buf = codec.AppendBytes(buf, h)
	}
	buf = codec.AppendUint(buf, annKeyNonce)
	buf = codec.AppendUint(buf, nonce)
	return buf
}

func decodeAnnounce(data []byte) (*Announcement, error) {
	d := codec.NewDecoder(data)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	a := &Announcement{}
	for {
		k, ok, er := m.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case annKeyPort:
			var v uint64
			v, er = d.ReadUint()
			a.Port = int(v)
		case annKeyHints:
			var cnt int
			cnt, er = d.ReadArray()
			if er != nil {
				return nil, er
			}
			for range cnt {
				h, e := d.ReadBytes()
				if e != nil {
					return nil, e
				}
				a.Hints = append(a.Hints, append([]byte(nil), h...))
			}
		case annKeyNonce:
			a.Nonce, er = d.ReadUint()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if a.Port == 0 {
		return nil, errors.New("lan: announce missing port")
	}
	return a, nil
}

// AnnounceOnce broadcasts one beacon for the given hints to the multicast
// group (or any UDP address, e.g. 127.0.0.1:<port> in tests).
func AnnounceOnce(udpAddr string, tcpPort int, hints [][]byte, nonce uint64) error {
	raddr, err := net.ResolveUDPAddr("udp4", udpAddr)
	if err != nil {
		return err
	}
	c, err := net.DialUDP("udp4", nil, raddr)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write(encodeAnnounce(tcpPort, hints, nonce))
	return err
}

// ListenAnnounces binds a UDP listener (multicast group or plain address)
// and forwards decoded announcements until the socket closes. Returns the
// bound address (useful with port 0 in tests) and a stop function.
func ListenAnnounces(udpAddr string, onAnnounce func(Announcement)) (string, func(), error) {
	laddr, err := net.ResolveUDPAddr("udp4", udpAddr)
	if err != nil {
		return "", nil, err
	}
	var conn *net.UDPConn
	if laddr.IP != nil && laddr.IP.IsMulticast() {
		conn, err = net.ListenMulticastUDP("udp4", nil, laddr)
	} else {
		conn, err = net.ListenUDP("udp4", laddr)
	}
	if err != nil {
		return "", nil, err
	}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			a, err := decodeAnnounce(buf[:n])
			if err != nil {
				continue // not ours; ignore quietly
			}
			a.Addr = net.JoinHostPort(src.IP.String(), fmt.Sprint(a.Port))
			onAnnounce(*a)
		}
	}()
	return conn.LocalAddr().String(), func() { conn.Close() }, nil
}

// MatchHint reports whether an announcement carries the hint for a terminal
// in either the current or previous bucket (clock-skew tolerance).
func MatchHint(a Announcement, terminal id.TerminalID, nowUnix uint64) bool {
	buckets := []uint64{Bucket(nowUnix)}
	if b := Bucket(nowUnix); b > 0 {
		buckets = append(buckets, b-1)
	}
	for _, b := range buckets {
		want := Hint(terminal, b)
		for _, h := range a.Hints {
			if string(h) == string(want) {
				return true
			}
		}
	}
	return false
}
