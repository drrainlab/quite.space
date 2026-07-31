// Networked blind relay (M1.5): serves the wire protocol over the framed
// TLS connections from transports/lan. Anyone can run one; none is
// mandatory (vision §5.4).
package relay

import (
	"crypto/tls"
	"errors"
	"time"

	"github.com/drrainlab/quiet_places/transports/lan"
)

// ServerLimits bound what one relay accepts.
type ServerLimits struct {
	MaxItemBytes int
	PerHint      int
	MaxTTL       time.Duration
	// PA-0 fetch bounds: hints per fetch request, bytes per fetch reply,
	// fetches per connection per minute. Zero values take the defaults.
	FetchMaxHints   int
	FetchMaxBytes   int
	FetchRatePerMin int
	// PH-0 drain and write bounds. Fetch was bounded from the start; Collect,
	// Replace and Put were not — and every one of them is reachable by anyone
	// who can COMPUTE a hint, which for a public space is anyone holding the
	// link. Reads and writes keep separate budgets on purpose: a busy
	// publisher must still be able to read, and a busy reader must still be
	// able to publish. Zero values take the defaults.
	CollectMaxHints   int
	CollectMaxBytes   int
	CollectRatePerMin int
	WriteRatePerMin   int
}

// DefaultLimits are conservative community-node settings.
func DefaultLimits() ServerLimits {
	return ServerLimits{
		MaxItemBytes: 16 << 20, PerHint: 64, MaxTTL: 7 * 24 * time.Hour,
		FetchMaxHints: 64, FetchMaxBytes: 8 << 20, FetchRatePerMin: 60,
		CollectMaxHints: 64, CollectMaxBytes: 8 << 20, CollectRatePerMin: 60,
		WriteRatePerMin: 240,
	}
}

func (l ServerLimits) fetchMaxHints() int {
	if l.FetchMaxHints <= 0 {
		return 64
	}
	return l.FetchMaxHints
}

func (l ServerLimits) fetchMaxBytes() int {
	if l.FetchMaxBytes <= 0 {
		return 8 << 20
	}
	return l.FetchMaxBytes
}

func (l ServerLimits) fetchRatePerMin() int {
	if l.FetchRatePerMin <= 0 {
		return 60
	}
	return l.FetchRatePerMin
}

func (l ServerLimits) collectMaxHints() int {
	if l.CollectMaxHints <= 0 {
		return 64
	}
	return l.CollectMaxHints
}

func (l ServerLimits) collectMaxBytes() int {
	if l.CollectMaxBytes <= 0 {
		return 8 << 20
	}
	return l.CollectMaxBytes
}

func (l ServerLimits) collectRatePerMin() int {
	if l.CollectRatePerMin <= 0 {
		return 60
	}
	return l.CollectRatePerMin
}

// writeRatePerMin covers Put and Replace together: they are the same
// resource (someone else's mailbox) reached by two verbs.
func (l ServerLimits) writeRatePerMin() int {
	if l.WriteRatePerMin <= 0 {
		return 240
	}
	return l.WriteRatePerMin
}

// Server is a running relay.
type Server struct {
	store  *Store
	limits ServerLimits
	node   *lan.Node
	stop   chan struct{}
}

// StartServer listens on addr (TLS, same session semantics as LAN: the
// channel is private, identity is not claimed — ephemeral certificate,
// the local-lan trust profile).
func StartServer(addr string, limits ServerLimits) (*Server, int, error) {
	return StartServerWithIdentity(addr, limits, nil)
}

// StartServerWithIdentity is StartServer with a PERSISTENT TLS identity
// (RR-1): a public relay pins its SPKI, so its key must outlive restarts.
// cert == nil keeps the ephemeral default.
func StartServerWithIdentity(addr string, limits ServerLimits, cert *tls.Certificate) (*Server, int, error) {
	s := &Server{
		store:  NewStore(limits.PerHint, limits.MaxItemBytes),
		limits: limits,
		stop:   make(chan struct{}),
	}
	// The relay carries whole bundles (frames + manifests) up to its item
	// cap, well past the 1 MiB LAN sync framing — open a large-packet node.
	node, err := lan.NewNodeWithMaxPacket(lan.RelayMaxPacket)
	if err != nil {
		return nil, 0, err
	}
	if cert != nil {
		node.SetCertificate(*cert)
	}
	s.node = node
	port, err := node.Listen(addr, func(c *lan.Conn) { go s.serve(c) })
	if err != nil {
		return nil, 0, err
	}
	// TTL expiry loop: deletion at expiry is unconditional (ADR-010).
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				s.store.Expire(uint64(time.Now().Unix()))
			}
		}
	}()
	return s, port, nil
}

// Close stops the relay.
func (s *Server) Close() {
	close(s.stop)
	s.node.Close()
}

// Pending reports held items (diagnostics).
func (s *Server) Pending() int { return s.store.Pending() }

// WipeForTest drops a hint's items — simulates a squatter wipe / storage
// loss in tests. Never part of the wire protocol.
func (s *Server) WipeForTest(hint []byte) {
	s.store.Collect(string(hint), 0)
}

// connState is per-connection abuse accounting: a sliding one-minute window
// per resource class (PA-0 fetch; PH-0 collect and writes). Zero value is a
// usable state — the windows start on first use.
type connState struct {
	fetches     int
	fetchWindow time.Time

	collects      int
	collectWindow time.Time

	writes      int
	writeWindow time.Time
}

// spend counts one use in a one-minute window and reports whether the
// caller is still inside its budget.
func spend(count *int, window *time.Time, limit int) bool {
	now := time.Now()
	if window.IsZero() || now.Sub(*window) > time.Minute {
		*window, *count = now, 0
	}
	*count++
	return *count <= limit
}

func (s *Server) serve(c *lan.Conn) {
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	idle := time.Now()
	cs := &connState{fetchWindow: time.Now()}
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
		if closed, _ := c.Closed(); closed {
			return
		}
		if time.Since(idle) > 2*time.Minute {
			c.Close()
			return
		}
		for _, pkt := range c.Poll() {
			idle = time.Now()
			msg, err := DecodeMsg(pkt)
			if err != nil {
				continue
			}
			reply := s.handle(msg, cs)
			if reply != nil {
				_ = c.Send(reply.Encode())
			}
		}
	}
}

func (s *Server) handle(m *Msg, cs *connState) *Msg {
	now := uint64(time.Now().Unix())
	switch m.Type {
	case msgPut:
		if len(m.Hint) != HintLen || len(m.Body) == 0 {
			return &Msg{Type: msgError, Reason: "malformed put"}
		}
		if !spend(&cs.writes, &cs.writeWindow, s.limits.writeRatePerMin()) {
			return &Msg{Type: msgError, Reason: "rate limited"}
		}
		expires := m.Expires
		maxExpiry := now + uint64(s.limits.MaxTTL/time.Second)
		if expires == 0 || expires > maxExpiry {
			expires = maxExpiry
		}
		ok := s.store.Put(Item{
			DestinationHint: string(m.Hint),
			ExpiresAt:       expires,
			Ciphertext:      m.Body,
		})
		if !ok {
			return &Msg{Type: msgError, Reason: "quota exceeded or item too large"}
		}
		// The receipt proves exactly accepted_by_relay and nothing more
		// (ADR-008): the expiry is echoed so the sender knows the deadline.
		return &Msg{Type: msgPutOK, Expires: expires}
	case msgCollect:
		// PH-1: knowing a hint is no longer enough to empty a mailbox. Refuse
		// loudly rather than answering "nothing here" — an empty drain and a
		// rejected drain must never look the same to an old client.
		return &Msg{Type: msgError, Reason: "collect requires a capability"}
	case msgCollectCap:
		// Draining is destructive, so it gets the same shape of bounds Fetch
		// has had since PA-0: rate, hint count, reply bytes. Without the byte
		// budget one collect could be asked to move the entire store.
		if !spend(&cs.collects, &cs.collectWindow, s.limits.collectRatePerMin()) {
			return &Msg{Type: msgError, Reason: "rate limited"}
		}
		if len(m.Caps) > s.limits.collectMaxHints() {
			return &Msg{Type: msgError, Reason: "too many hints"}
		}
		budget := s.limits.collectMaxBytes()
		var items [][]byte
		for _, c := range m.Caps {
			if len(c) != CapLen {
				return &Msg{Type: msgError, Reason: "malformed capability"}
			}
			got := s.store.CollectBudget(string(CollectHint(c)), now, budget)
			for _, it := range got {
				budget -= len(it)
			}
			items = append(items, got...)
			if budget <= 0 {
				break
			}
		}
		if items == nil {
			items = [][]byte{}
		}
		return &Msg{Type: msgItems, Items: items}
	case msgTime:
		// LR-2 calibration source: this clock's only property is that every
		// participant asking THIS relay gets the same one.
		return &Msg{Type: msgTimeOK, Now: uint64(time.Now().UnixMilli())}
	case msgReplace:
		if len(m.Hint) != HintLen || len(m.Body) == 0 {
			return &Msg{Type: msgError, Reason: "malformed replace"}
		}
		if !spend(&cs.writes, &cs.writeWindow, s.limits.writeRatePerMin()) {
			return &Msg{Type: msgError, Reason: "rate limited"}
		}
		expires := m.Expires
		maxExpiry := now + uint64(s.limits.MaxTTL/time.Second)
		if expires == 0 || expires > maxExpiry {
			expires = maxExpiry
		}
		if !s.store.Replace(Item{
			DestinationHint: string(m.Hint),
			ExpiresAt:       expires,
			Ciphertext:      m.Body,
		}) {
			return &Msg{Type: msgError, Reason: "quota exceeded or item too large"}
		}
		return &Msg{Type: msgPutOK, Expires: expires}
	case msgFetch:
		// Non-destructive read for public mailboxes: rate-limited per
		// connection, hint- and byte-capped per request.
		if time.Since(cs.fetchWindow) > time.Minute {
			cs.fetchWindow, cs.fetches = time.Now(), 0
		}
		cs.fetches++
		if cs.fetches > s.limits.fetchRatePerMin() {
			return &Msg{Type: msgError, Reason: "rate limited"}
		}
		if len(m.Hints) > s.limits.fetchMaxHints() {
			return &Msg{Type: msgError, Reason: "too many hints"}
		}
		budget := s.limits.fetchMaxBytes()
		var items [][]byte
		for _, h := range m.Hints {
			got := s.store.Fetch(string(h), now, budget)
			for _, it := range got {
				budget -= len(it)
			}
			items = append(items, got...)
			if budget <= 0 {
				break
			}
		}
		if items == nil {
			items = [][]byte{}
		}
		return &Msg{Type: msgFetchItems, Items: items}
	default:
		return &Msg{Type: msgError, Reason: "unknown message type"}
	}
}

// ---- Client ----

// ErrRelay wraps a relay-reported error.
type ErrRelay struct{ Reason string }

func (e ErrRelay) Error() string { return "relay: " + e.Reason }

// Client is one connection to a relay.
type Client struct {
	conn *lan.Conn
}

// DialClient connects to a relay with NO identity check — the local-lan
// trust profile (loopback and LAN, where identity lives in event
// signatures). Public relays are dialed through DialClientPinned.
func DialClient(addr string) (*Client, error) {
	node, err := lan.NewNodeWithMaxPacket(lan.RelayMaxPacket)
	if err != nil {
		return nil, err
	}
	c, err := node.Dial(addr)
	if err != nil {
		return nil, err
	}
	return &Client{conn: c}, nil
}

// DialClientPinned connects with SPKI verification (RR-1): verify gets the
// peer's pin during the handshake; returning an error aborts the
// connection before any protocol byte flows.
func DialClientPinned(addr string, verify func(pin string) error) (*Client, error) {
	node, err := lan.NewNodeWithMaxPacket(lan.RelayMaxPacket)
	if err != nil {
		return nil, err
	}
	c, err := node.DialPinned(addr, verify)
	if err != nil {
		return nil, err
	}
	return &Client{conn: c}, nil
}

// Close disconnects.
func (c *Client) Close() { c.conn.Close() }

func (c *Client) roundTrip(m *Msg, timeout time.Duration) (*Msg, error) {
	if err := c.conn.Send(m.Encode()); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, pkt := range c.conn.Poll() {
			reply, err := DecodeMsg(pkt)
			if err != nil {
				continue
			}
			if reply.Type == msgError {
				return nil, ErrRelay{Reason: reply.Reason}
			}
			return reply, nil
		}
		if closed, err := c.conn.Closed(); closed {
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, errors.New("relay: timed out waiting for reply")
}

// Put stores one opaque item; returns the relay's accepted deadline. This
// is a transport receipt: it proves accepted_by_relay, never delivery.
func (c *Client) Put(hint []byte, expiresAt uint64, body []byte) (uint64, error) {
	reply, err := c.roundTrip(&Msg{Type: msgPut, Hint: hint, Expires: expiresAt, Body: body}, 10*time.Second)
	if err != nil {
		return 0, err
	}
	if reply.Type != msgPutOK {
		return 0, errors.New("relay: unexpected reply")
	}
	return reply.Expires, nil
}

// Time asks the relay for its wall clock (unix ms) and reports the local
// monotonic round-trip. The caller derives offset = relay_now + rtt/2 - local
// and keeps the sample with the smallest RTT.
func (c *Client) Time() (nowMS uint64, rtt time.Duration, err error) {
	start := time.Now() // monotonic
	reply, err := c.roundTrip(&Msg{Type: msgTime}, 5*time.Second)
	if err != nil {
		return 0, 0, err
	}
	if reply.Type != msgTimeOK || reply.Now == 0 {
		return 0, 0, errors.New("relay: unexpected time reply")
	}
	return reply.Now, time.Since(start), nil
}

// Collect fetches (and removes) everything stored under the given hints.
// Collect drains the mailboxes these CAPABILITIES address (PH-1). The
// caller proves it may empty a box by holding the preimage of the box's
// address; hints alone no longer open anything.
func (c *Client) Collect(caps [][]byte) ([][]byte, error) {
	reply, err := c.roundTrip(&Msg{Type: msgCollectCap, Caps: caps}, 10*time.Second)
	if err != nil {
		return nil, err
	}
	if reply.Type != msgItems {
		return nil, errors.New("relay: unexpected reply")
	}
	return reply.Items, nil
}

// Fetch reads the given hints WITHOUT removing anything — the many-reader
// verb for public mailboxes (PA-0). Server-side budgets bound the reply.
func (c *Client) Fetch(hints [][]byte) ([][]byte, error) {
	reply, err := c.roundTrip(&Msg{Type: msgFetch, Hints: hints}, 10*time.Second)
	if err != nil {
		return nil, err
	}
	if reply.Type != msgFetchItems {
		return nil, errors.New("relay: unexpected reply")
	}
	return reply.Items, nil
}

// Replace atomically swaps a hint's contents with one item (the public
// projection mailbox). Same receipt semantics as Put: accepted, not
// delivered.
func (c *Client) Replace(hint []byte, expiresAt uint64, body []byte) (uint64, error) {
	reply, err := c.roundTrip(&Msg{Type: msgReplace, Hint: hint, Expires: expiresAt, Body: body}, 10*time.Second)
	if err != nil {
		return 0, err
	}
	if reply.Type != msgPutOK {
		return 0, errors.New("relay: unexpected reply")
	}
	return reply.Expires, nil
}
