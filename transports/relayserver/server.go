// Package relayserver is the relay SERVER: the process an operator stands up
// so that other people's devices can leave sealed items for each other.
//
// IT LIVES IN ITS OWN PACKAGE FOR A LICENCE REASON, and the reason is the whole
// point of the split. A Go package compiles as a unit, so whichever licence
// covers a directory covers everything a consumer links. While the server and
// the client shared one package there was no honest answer: mark it Apache and
// the relay server becomes permanently forkable into a closed public service,
// making the AGPL side of the project decorative; mark it AGPL and node/
// becomes an AGPL derivative, and with it the desktop and Android clients —
// the opposite of what the split is for.
//
//	transports/relay        Apache-2.0 — the wire protocol and the client.
//	                        A client needs it, and so does anyone writing an
//	                        independent relay.
//	transports/relayserver  AGPL-3.0-only — this. Free to run, to modify and
//	                        to charge for hosting; offer a modified version to
//	                        users over a network and those users can have that
//	                        version's source.
//
// Nothing about the wire format, the trust model or the behaviour changed when
// this moved. The message-type constants became exported in the same commit,
// which is not incidental: they are the first thing an independent relay
// implementer needs, and Apache-2.0 on the wire package is exactly the promise
// that they may have them.
//
// Networked blind relay (M1.5): serves the wire protocol over the framed TLS
// connections from transports/lan. Anyone can run one; none is mandatory
// (vision §5.4).
package relayserver

import (
	"crypto/tls"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/transports/lan"
	"github.com/drrainlab/quiet_places/transports/relay"
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
	// EN-2 listen bounds: hints one connection may park, and how long a
	// parked connection may be silent before the reaper takes it. Zero
	// values take the defaults.
	ListenMaxHints int
	ListenIdle     time.Duration

	// MaxConns caps concurrent connections (RR-6): every accepted conn
	// costs a goroutine and a poll ticker, so an accept flood must hit a
	// wall, not the scheduler. Over the cap the conn is closed cheaply.
	MaxConns int
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
	// Rate budgets are PER CONNECTION, and RR-2's pool made connections
	// long-lived: one control lane now carries everything a node does at
	// its 2s cadence instead of resetting the window on every fresh dial.
	// The arithmetic behind the numbers: per tick a node spends ~2 collects
	// (inbox+reply drain, batched ingress) ≈ 60/min, plus headroom for
	// retries and manual drains → 240. Writes: pushes to N recipients +
	// ingress uplinks + want answers, bursty on catch-up → 600. Fetches
	// ride the bulk lane: a projection per active public space per tick →
	// 240. These are still abuse rails, not throughput promises.
	return ServerLimits{
		// MaxItemBytes aligns with codec.MaxItemLen (RR-7): the old 16 MiB
		// cap let the relay accept items NO CLIENT could decode (the codec
		// refuses byte strings over 1 MiB) — a stall generator. Every real
		// producer already stays under the app-level 768 KiB.
		MaxItemBytes: 1 << 20, PerHint: 64, MaxTTL: 7 * 24 * time.Hour,
		FetchMaxHints: 64, FetchMaxBytes: 8 << 20, FetchRatePerMin: 240,
		CollectMaxHints: 64, CollectMaxBytes: 8 << 20, CollectRatePerMin: 240,
		WriteRatePerMin: 600,
		MaxConns:        4096,
	}
}

func (l ServerLimits) maxConns() int {
	if l.MaxConns <= 0 {
		return 4096
	}
	return l.MaxConns
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

// listenMaxHints bounds one connection's park (EN-2). Wider than a collect
// on purpose: a park is a standing statement, not a per-request cost, and a
// device with many spaces parks every inbox it owns plus its identity and
// knock mailboxes.
func (l ServerLimits) listenMaxHints() int {
	if l.ListenMaxHints <= 0 {
		return 256
	}
	return l.ListenMaxHints
}

// listenIdle is how long a parked connection may be silent before the
// reaper takes it. The client is told this figure (MsgListenOK) and pings
// well inside it; 45 minutes tolerates three missed 15-minute pings.
func (l ServerLimits) listenIdle() time.Duration {
	if l.ListenIdle <= 0 {
		return 45 * time.Minute
	}
	return l.ListenIdle
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
	connMu sync.Mutex
	conns  int

	// listeners maps a parked hint to the connections that asked to be
	// told about it (EN-2 relay push). The VALUE side is each connection's
	// own pending-notify set: a Put appends there under this one lock, and
	// the connection's own serve loop — the only goroutine that ever
	// writes to its socket — drains it on its next tick. Nothing here
	// makes the relay less blind: it already knew which hints a client
	// polls; parking states the same fact once instead of every 2s.
	listenMu  sync.Mutex
	listeners map[string]map[*connState]struct{}

	// push is the EN-3 doorbell-beyond-the-socket registry; see push.go.
	pushOnce sync.Once
	pushV    *pushRegistry
}

// pushRegs returns the registry, created on first use.
func (s *Server) pushRegs() *pushRegistry {
	s.pushOnce.Do(func() { s.pushV = newPushRegistry() })
	return s.pushV
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

// PendingBytes reports held ciphertext bytes (diagnostics).
func (s *Server) PendingBytes() int64 { return s.store.PendingBytes() }

// Conns reports current connections (diagnostics).
func (s *Server) Conns() int {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	return s.conns
}

// SetByteBudgets forwards operator byte quotas to the store (RR-7).
func (s *Server) SetByteBudgets(perHint int, total int64) {
	s.store.SetByteBudgets(perHint, total)
}

// loadClass derives the self-reported load from simple store facts
// (RR-3). Advisory: clients weigh it, they never owe it blind trust.
func (s *Server) loadClass() string {
	fill := s.store.FillRatio()
	switch {
	case fill > 0.9:
		return relay.LoadOverloaded
	case fill > 0.7:
		return relay.LoadBusy
	}
	return relay.LoadNormal
}

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

	// The park (EN-2): hints this connection listens for, and the
	// notifications waiting for its serve loop. pending is written by
	// OTHER connections' Put handlers under Server.listenMu; the serve
	// loop drains it under the same lock and sends on its own socket.
	listenHints map[string]struct{}
	pending     map[string]struct{}
	// pushEndpoint is what THIS connection last registered (EN-3), so an
	// empty keyPush on a later park can name what to remove.
	pushEndpoint string
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

// retryAfter is what is left of the current window, which is exactly how long
// a refused caller should wait. Reported so a client does not have to guess:
// a guess is either too eager, which is more load on a relay that just asked
// for less, or too patient, which is a person's messages sitting on a relay
// for no reason.
func retryAfter(window time.Time) uint64 {
	left := time.Minute - time.Since(window)
	if left < time.Second {
		left = time.Second // never answer "try again now"
	}
	return uint64(left / time.Millisecond)
}

func (s *Server) serve(c *lan.Conn) {
	// Connection cap (RR-6): every conn costs a goroutine and a ticker,
	// so an accept flood hits this wall instead of the scheduler.
	s.connMu.Lock()
	if s.conns >= s.limits.maxConns() {
		s.connMu.Unlock()
		c.Close()
		return
	}
	s.conns++
	s.connMu.Unlock()
	defer func() {
		s.connMu.Lock()
		s.conns--
		s.connMu.Unlock()
	}()
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	idle := time.Now()
	cs := &connState{fetchWindow: time.Now()}
	for {
		select {
		case <-s.stop:
			// STOPPING MEANS HANGING UP. Returning without closing left
			// the socket open until the GC got to it — the peer never saw
			// a FIN, its reader loop stayed blocked, and every request it
			// had in flight sat out its full deadline over a connection
			// that was, as far as it could tell, merely quiet. In the
			// suite that was Runtime.Close waiting ten seconds per node
			// on a relay that had "stopped"; in production it is what a
			// graceful relay restart does to every client still attached.
			// A closed listener refuses new callers; only closing the
			// connection tells the ones already here.
			s.unpark(cs)
			c.Close()
			return
		case <-t.C:
		}
		if closed, _ := c.Closed(); closed {
			s.unpark(cs)
			return
		}
		// A PARKED connection is allowed a much longer silence: silence is
		// its whole job, and its client pings inside listenIdle. Everyone
		// else keeps the short reaper.
		reap := 2 * time.Minute
		if len(cs.listenHints) > 0 {
			reap = s.limits.listenIdle()
		}
		if time.Since(idle) > reap {
			s.unpark(cs)
			c.Close()
			return
		}
		// Deliver queued notifications on THIS loop — the only goroutine
		// that ever writes to this socket.
		for _, h := range s.takePending(cs) {
			_ = c.Send((&relay.Msg{Type: relay.MsgNotify, Hint: []byte(h)}).Encode())
		}
		for _, pkt := range c.Poll() {
			idle = time.Now()
			msg, err := relay.DecodeMsg(pkt)
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

func (s *Server) handle(m *relay.Msg, cs *connState) *relay.Msg {
	now := uint64(time.Now().Unix())
	switch m.Type {
	case relay.MsgPut:
		if len(m.Hint) != relay.HintLen || len(m.Body) == 0 {
			return &relay.Msg{Type: relay.MsgError, Reason: "malformed put"}
		}
		if !spend(&cs.writes, &cs.writeWindow, s.limits.writeRatePerMin()) {
			return &relay.Msg{Type: relay.MsgError, Reason: relay.ReasonRateLimited,
				RetryAfterMs: retryAfter(cs.writeWindow)}
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
			return &relay.Msg{Type: relay.MsgError, Reason: relay.ReasonQuotaExceeded}
		}
		if s.notifyListeners(string(m.Hint)) == 0 {
			// Nobody parked is here to hear it — ring the out-of-band
			// doorbell, if one is registered for this hint (EN-3).
			s.pushRegs().ring(string(m.Hint))
		}
		// The receipt proves exactly accepted_by_relay and nothing more
		// (ADR-008): the expiry is echoed so the sender knows the deadline.
		return &relay.Msg{Type: relay.MsgPutOK, Expires: expires}
	case relay.MsgCollect:
		// PH-1: knowing a hint is no longer enough to empty a mailbox. Refuse
		// loudly rather than answering "nothing here" — an empty drain and a
		// rejected drain must never look the same to an old client. Metered
		// (RR-3): a dead verb must not be the cheapest thing to spin.
		if !spend(&cs.collects, &cs.collectWindow, s.limits.collectRatePerMin()) {
			return &relay.Msg{Type: relay.MsgError, Reason: relay.ReasonRateLimited,
				RetryAfterMs: retryAfter(cs.collectWindow)}
		}
		return &relay.Msg{Type: relay.MsgError, Reason: relay.ReasonNoCapability}
	case relay.MsgCollectCap:
		// Draining is destructive, so it gets the same shape of bounds Fetch
		// has had since PA-0: rate, hint count, reply bytes. Without the byte
		// budget one collect could be asked to move the entire store.
		if !spend(&cs.collects, &cs.collectWindow, s.limits.collectRatePerMin()) {
			return &relay.Msg{Type: relay.MsgError, Reason: relay.ReasonRateLimited,
				RetryAfterMs: retryAfter(cs.collectWindow)}
		}
		if len(m.Caps) > s.limits.collectMaxHints() {
			return &relay.Msg{Type: relay.MsgError, Reason: relay.ReasonTooManyHints}
		}
		budget := s.limits.collectMaxBytes()
		var items [][]byte
		for _, c := range m.Caps {
			if len(c) != relay.CapLen {
				return &relay.Msg{Type: relay.MsgError, Reason: "malformed capability"}
			}
			got := s.store.CollectBudget(string(relay.CollectHint(c)), now, budget)
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
		return &relay.Msg{Type: relay.MsgItems, Items: items}
	case relay.MsgTime:
		// LR-2 calibration source: this clock's only property is that every
		// participant asking THIS relay gets the same one. Metered (RR-3):
		// an unmetered echo is a free amplification target.
		if !spend(&cs.collects, &cs.collectWindow, s.limits.collectRatePerMin()) {
			return &relay.Msg{Type: relay.MsgError, Reason: relay.ReasonRateLimited,
				RetryAfterMs: retryAfter(cs.collectWindow)}
		}
		return &relay.Msg{Type: relay.MsgTimeOK, Now: uint64(time.Now().UnixMilli())}
	case relay.MsgProbe:
		// RR-3: the selection probe — protocol range, load class, accepting,
		// wall clock, nonce echoed. No durable state; metered like a collect
		// so a probe storm pays the same rail as any other read.
		if !spend(&cs.collects, &cs.collectWindow, s.limits.collectRatePerMin()) {
			return &relay.Msg{Type: relay.MsgError, Reason: relay.ReasonRateLimited,
				RetryAfterMs: retryAfter(cs.collectWindow)}
		}
		return &relay.Msg{
			Type: relay.MsgProbeOK, Nonce: m.Nonce,
			ProtoMin: relay.RelayProtocolMin, ProtoMax: relay.RelayProtocolVersion,
			Load: s.loadClass(), Accepting: 1,
			Now: uint64(time.Now().UnixMilli()),
		}
	case relay.MsgReplace:
		if len(m.Hint) != relay.HintLen || len(m.Body) == 0 {
			return &relay.Msg{Type: relay.MsgError, Reason: "malformed replace"}
		}
		if !spend(&cs.writes, &cs.writeWindow, s.limits.writeRatePerMin()) {
			return &relay.Msg{Type: relay.MsgError, Reason: relay.ReasonRateLimited,
				RetryAfterMs: retryAfter(cs.writeWindow)}
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
			return &relay.Msg{Type: relay.MsgError, Reason: relay.ReasonQuotaExceeded}
		}
		return &relay.Msg{Type: relay.MsgPutOK, Expires: expires}
	case relay.MsgFetch:
		// Non-destructive read for public mailboxes: rate-limited per
		// connection, hint- and byte-capped per request. Validation runs
		// BEFORE the budget is spent (RR-3 consistency fix — a rejected
		// oversize request used to charge the window anyway).
		if len(m.Hints) > s.limits.fetchMaxHints() {
			return &relay.Msg{Type: relay.MsgError, Reason: relay.ReasonTooManyHints}
		}
		if !spend(&cs.fetches, &cs.fetchWindow, s.limits.fetchRatePerMin()) {
			return &relay.Msg{Type: relay.MsgError, Reason: relay.ReasonRateLimited,
				RetryAfterMs: retryAfter(cs.fetchWindow)}
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
		return &relay.Msg{Type: relay.MsgFetchItems, Items: items}
	case relay.MsgListen:
		// Parking replaces this connection's whole set: re-listening with
		// fresh hints IS the bucket-rotation protocol, and a replace
		// cannot leak stale registrations the way an append could.
		// Metered like a collect — parking is cheap for the relay but not
		// free, and an unmetered verb is an amplification invitation.
		if !spend(&cs.collects, &cs.collectWindow, s.limits.collectRatePerMin()) {
			return &relay.Msg{Type: relay.MsgError, Reason: relay.ReasonRateLimited,
				RetryAfterMs: retryAfter(cs.collectWindow)}
		}
		if len(m.Hints) > s.limits.listenMaxHints() {
			return &relay.Msg{Type: relay.MsgError, Reason: relay.ReasonTooManyHints}
		}
		for _, h := range m.Hints {
			if len(h) != relay.HintLen {
				return &relay.Msg{Type: relay.MsgError, Reason: "malformed hint"}
			}
		}
		s.repark(cs, m.Hints)
		// EN-3: an endpoint riding the park registers the out-of-band
		// doorbell for the same hints; an EMPTY one removes whatever this
		// connection last registered — the off switch, named by the only
		// party who knows the endpoint. The registration deliberately
		// SURVIVES the connection: its whole purpose is the time when no
		// connection exists.
		if m.PushSet {
			if m.Push == "" {
				if cs.pushEndpoint != "" {
					s.pushRegs().remove(cs.pushEndpoint)
					cs.pushEndpoint = ""
				}
			} else {
				if err := relay.ValidatePushEndpoint(m.Push); err != nil {
					return &relay.Msg{Type: relay.MsgError, Reason: "malformed push endpoint"}
				}
				if cs.pushEndpoint != "" && cs.pushEndpoint != m.Push {
					s.pushRegs().remove(cs.pushEndpoint)
				}
				cs.pushEndpoint = m.Push
				s.pushRegs().register(m.Push, m.Hints)
			}
		}
		// The advisory hold: how long the reaper tolerates silence on a
		// parked connection. The client pings well inside it.
		return &relay.Msg{Type: relay.MsgListenOK,
			Expires: uint64(s.limits.listenIdle() / time.Second)}
	case relay.MsgPing:
		// The keepalive that holds NATs and the idle reaper between
		// notifications. Deliberately the cheapest verb on the wire and
		// still metered by the conn loop's own idle bookkeeping — a ping
		// flood is bounded by the read loop, not answered for free
		// forever (writes budget, the roomiest one).
		if !spend(&cs.writes, &cs.writeWindow, s.limits.writeRatePerMin()) {
			return &relay.Msg{Type: relay.MsgError, Reason: relay.ReasonRateLimited,
				RetryAfterMs: retryAfter(cs.writeWindow)}
		}
		return &relay.Msg{Type: relay.MsgPong}
	default:
		return &relay.Msg{Type: relay.MsgError, Reason: "unknown message type"}
	}
}

// repark replaces one connection's parked hints under the registry lock.
func (s *Server) repark(cs *connState, hints [][]byte) {
	s.listenMu.Lock()
	defer s.listenMu.Unlock()
	if s.listeners == nil {
		s.listeners = map[string]map[*connState]struct{}{}
	}
	for h := range cs.listenHints {
		if set := s.listeners[h]; set != nil {
			delete(set, cs)
			if len(set) == 0 {
				delete(s.listeners, h)
			}
		}
	}
	cs.listenHints = map[string]struct{}{}
	for _, h := range hints {
		k := string(h)
		cs.listenHints[k] = struct{}{}
		set := s.listeners[k]
		if set == nil {
			set = map[*connState]struct{}{}
			s.listeners[k] = set
		}
		set[cs] = struct{}{}
	}
}

// unpark removes a dying connection from the registry.
func (s *Server) unpark(cs *connState) {
	s.listenMu.Lock()
	defer s.listenMu.Unlock()
	for h := range cs.listenHints {
		if set := s.listeners[h]; set != nil {
			delete(set, cs)
			if len(set) == 0 {
				delete(s.listeners, h)
			}
		}
	}
	cs.listenHints = nil
}

// notifyListeners queues one hint's arrival for every parked connection.
// Queued as a SET: ten Puts between two of a client's reads are one
// notification, because the client's response — drain the mailbox — is
// the same either way.
func (s *Server) notifyListeners(hint string) int {
	s.listenMu.Lock()
	defer s.listenMu.Unlock()
	n := 0
	for cs := range s.listeners[hint] {
		if cs.pending == nil {
			cs.pending = map[string]struct{}{}
		}
		cs.pending[hint] = struct{}{}
		n++
	}
	return n
}

// takePending drains one connection's queued notifications.
func (s *Server) takePending(cs *connState) []string {
	s.listenMu.Lock()
	defer s.listenMu.Unlock()
	if len(cs.pending) == 0 {
		return nil
	}
	out := make([]string, 0, len(cs.pending))
	for h := range cs.pending {
		out = append(out, h)
	}
	cs.pending = nil
	return out
}

// ---- Client ----
