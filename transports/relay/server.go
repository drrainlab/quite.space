// Networked blind relay (M1.5): serves the wire protocol over the framed
// TLS connections from transports/lan. Anyone can run one; none is
// mandatory (vision §5.4).
package relay

import (
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
	FetchMaxHints    int
	FetchMaxBytes    int
	FetchRatePerMin  int
}

// DefaultLimits are conservative community-node settings.
func DefaultLimits() ServerLimits {
	return ServerLimits{
		MaxItemBytes: 16 << 20, PerHint: 64, MaxTTL: 7 * 24 * time.Hour,
		FetchMaxHints: 64, FetchMaxBytes: 8 << 20, FetchRatePerMin: 60,
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

// Server is a running relay.
type Server struct {
	store  *Store
	limits ServerLimits
	node   *lan.Node
	stop   chan struct{}
}

// StartServer listens on addr (TLS, same session semantics as LAN: the
// channel is private, identity is not claimed).
func StartServer(addr string, limits ServerLimits) (*Server, int, error) {
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

// connState is per-connection abuse accounting (PA-0 fetch rate limit).
type connState struct {
	fetches     int
	fetchWindow time.Time
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
		var items [][]byte
		for _, h := range m.Hints {
			items = append(items, s.store.Collect(string(h), now)...)
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

// DialClient connects to a relay.
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
func (c *Client) Collect(hints [][]byte) ([][]byte, error) {
	reply, err := c.roundTrip(&Msg{Type: msgCollect, Hints: hints}, 10*time.Second)
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
