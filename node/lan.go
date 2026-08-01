// LAN auto-sync (M1.1 wired into the runtime): announce rotating hints for
// every space, dial peers whose announces match, and pump the sync engine
// over each live connection. No coordinator, no server — two nodes on one
// network find each other and converge.
package node

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"time"

	"github.com/drrainlab/quiet_places/kernel/routing"
	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/lan"
)

const (
	announceEvery = 3 * time.Second
	pumpEvery     = 300 * time.Millisecond
)

// LANStatus reports transport diagnostics for the UI (plan §8.2: the user
// always sees how they are connected).
type LANStatus struct {
	Listening bool
	Port      int
	Peers     int
}

// StartLAN begins listening, announcing, and discovering. announceAddr is
// the UDP address for beacons (lan.MulticastAddr in production, a localhost
// address in tests). listenAddr is the TCP bind ("" ⇒ all interfaces).
func (r *Runtime) StartLAN(listenAddr, announceAddr string) error {
	n, err := lan.NewNode()
	if err != nil {
		return err
	}
	// A random nonce distinguishes our own announces from peers'.
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	selfNonce := binary.BigEndian.Uint64(nonce[:])

	port, err := n.Listen(listenAddr, func(c *lan.Conn) { r.adoptConn(c) })
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.lanNode = n
	r.lanPort = port
	r.mu.Unlock()

	// Announce loop.
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := time.NewTicker(announceEvery)
		defer t.Stop()
		for {
			r.announceOnce(announceAddr, port, selfNonce)
			select {
			case <-r.stop:
				return
			case <-t.C:
			}
		}
	}()

	// Discovery listener: dial peers that carry hints for our spaces.
	dialed := map[string]bool{}
	_, stopListen, err := lan.ListenAnnounces(announceAddr, func(a lan.Announcement) {
		if a.Nonce == selfNonce {
			return
		}
		now := uint64(time.Now().Unix())
		match := false
		// Same list as the announcer's: never let a local-only space be the
		// reason this node dials somebody.
		for _, tid := range r.announcedSpaces() {
			if lan.MatchHint(a, tid, now) {
				match = true
				break
			}
		}
		r.mu.Lock()
		already := dialed[a.Addr]
		if match && !already {
			dialed[a.Addr] = true
		}
		r.mu.Unlock()
		if !match || already {
			return
		}
		c, err := n.Dial(a.Addr)
		if err != nil {
			r.mu.Lock()
			delete(dialed, a.Addr)
			r.mu.Unlock()
			return
		}
		r.adoptConn(c)
	})
	if err != nil {
		return err
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		<-r.stop
		stopListen()
	}()
	return nil
}

// announcedSpaces is which spaces this node is willing to tell the LAN it
// holds. It is one function, used by the announcer AND by the inbound
// matcher, so "which spaces are visible on the network" has exactly one
// answer and a test can read it.
//
// A local-only space is absent: an announcement is how the LAN learns this
// device holds something, which for the assistant's space would be telling
// the network about a conversation that never leaves the machine (AI-0).
func (r *Runtime) announcedSpaces() []id.TerminalID {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]id.TerminalID, 0, len(r.spaces))
	for tid := range r.spaces {
		if r.ks.Spaces[tid].LocalOnly {
			continue
		}
		out = append(out, tid)
	}
	return out
}

func (r *Runtime) announceOnce(announceAddr string, port int, nonce uint64) {
	now := uint64(time.Now().Unix())
	visible := r.announcedSpaces()
	hints := make([][]byte, 0, len(visible))
	for _, tid := range visible {
		hints = append(hints, lan.Hint(tid, lan.Bucket(now)))
	}
	if len(hints) == 0 {
		return
	}
	_ = lan.AnnounceOnce(announceAddr, port, hints, nonce)
}

// adoptConn attaches a LAN connection with realtime cadence.
func (r *Runtime) adoptConn(c *lan.Conn) {
	r.adoptLink(c, pumpEvery, 2*time.Second, "lan")
}

// liveLink is one adopted link plus the filter it was adopted under. The
// filter is kept because it is what decides whether a space created later
// belongs on this link at all — re-asking it is the only honest way to
// wire that space in.
type liveLink struct {
	c     link
	label string
	allow func(routing.FrameMeta) bool
}

// wireLiveLinksLocked attaches every currently adopted link this space is
// allowed on. Caller holds r.mu (every attach() call site does).
func (r *Runtime) wireLiveLinksLocked(tid id.TerminalID, st *spaceState) {
	for _, l := range r.links {
		if l.allow != nil && !l.allow(routing.FrameMeta{
			Destination: tid, IngressLink: routing.LinkID(l.label),
		}) {
			continue
		}
		st.conns = append(st.conns, l.c)
	}
}

// adoptLink attaches any link to every space and pumps it until it dies.
// Frames for other terminals are simply not matched by the engines — each
// engine checks its own terminal id. Cadence is per-transport: a LAN link
// pumps in milliseconds; a LoRa link must respect airtime (plan §19 T6).
// label names the link kind for the delivery-route projection (ADR-015).
func (r *Runtime) adoptLink(c link, pump, summaryEvery time.Duration, label string) {
	r.adoptLinkFiltered(c, pump, summaryEvery, label, nil)
}

// adoptLinkFiltered is the TN-1 seam: allow (when non-nil) scopes which
// spaces sync over this link, judged by FrameMeta of the space's identity
// (destination = the space terminal). nil = bit-identical attach-all
// behavior. This is the hook a node needs to serve a bridge a subset of
// its spaces without adopting the full router.
func (r *Runtime) adoptLinkFiltered(c link, pump, summaryEvery time.Duration,
	label string, allow func(routing.FrameMeta) bool) {
	linkKind := transportOfLink(label)
	r.mu.Lock()
	for tid, st := range r.spaces {
		if allow != nil && !allow(routing.FrameMeta{
			Destination: tid, IngressLink: routing.LinkID(label),
		}) {
			continue
		}
		st.conns = append(st.conns, c)
	}
	// Register the link so a space created LATER can be wired into it
	// (attach). A radio is adopted ONCE at startup, when the space set is
	// usually empty; without this registry every space made during the
	// session was invisible to it until a restart.
	r.links = append(r.links, liveLink{c: c, label: label, allow: allow})
	if r.liveLinks == nil {
		r.liveLinks = map[TransportKind]int{}
	}
	r.liveLinks[linkKind]++
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() {
			r.mu.Lock()
			r.liveLinks[linkKind]--
			r.mu.Unlock()
		}()
		t := time.NewTicker(pump)
		defer t.Stop()
		// ONE reassembler for the link, not one per space. Fragments from
		// every terminal share this wire, and reassembly is a property of
		// the wire.
		reasm := kernelsync.NewReassembler()
		lastSummary := time.Time{}
		for {
			select {
			case <-r.stop:
				return
			case <-t.C:
			}
			if closed, _ := c.Closed(); closed {
				r.dropConn(c)
				return
			}
			// Read the policy BEFORE taking the lock: connectivity() reads
			// settings, which takes r.mu itself.
			conn := r.connectivity()
			kind := transportOfLink(label)
			now := time.Now()

			r.mu.Lock()
			r.curLink = label // OnSent closures read this under the same lock
			// Membership is read FRESH every tick, never snapshotted when the
			// link was adopted. A radio is adopted once at startup — usually
			// with no spaces yet — so a snapshot meant everything a person
			// created during the session was carried by nothing and received
			// by nothing on that link until both sides restarted. The lock is
			// already held here, so the fresh read costs a map walk.
			byTerm := make(map[id.TerminalID]*spaceState, len(r.spaces))
			for tid, st := range r.spaces {
				if allow != nil && !allow(routing.FrameMeta{
					Destination: tid, IngressLink: routing.LinkID(label),
				}) {
					continue
				}
				byTerm[tid] = st
			}
			// Two different questions, answered separately.
			//
			// POLICY decides whether this link may carry a space at all. A
			// forbidden transport carries nothing in either direction — not
			// a frame, not a summary, not the shape of what we would ask
			// for.
			//
			// ROUTE decides where a space that has something outstanding
			// should send it. That gate applies to SENDING only: a space
			// with nothing to send keeps listening on every permitted link,
			// because going deaf on the mesh whenever the internet happens
			// to be preferred would be a strange way to run a radio node.
			sending := map[id.TerminalID]bool{}
			active := make([]*spaceState, 0, len(byTerm))
			for tid, st := range byTerm {
				if !conn.allows(kind, tid) {
					continue
				}
				route, has := r.routeForSpaceLocked(conn, tid, now)
				if has && route != kind {
					continue // something to send, but not by this road
				}
				active = append(active, st)
				sending[tid] = has
				// Stamp the responsibility token BEFORE anything can go
				// out. openAttempt fsyncs it, so a crash between minting
				// and sending cannot leave us minting a second token for
				// the same epoch — an acknowledgement already in flight
				// would then name a hand-off we no longer recognise.
				if tok, ok := r.openAttempt(tid, now); ok {
					st.eng.AttemptToken = tok[:]
				}
			}
			var sendErr error
			if time.Since(lastSummary) > summaryEvery {
				for _, st := range active {
					if err := st.eng.SendSummary(c); err != nil && sendErr == nil {
						sendErr = err
					}
				}
				lastSummary = time.Now()
			}
			// Poll ONCE and route by terminal. Letting each space call
			// Pump would mean each one draining the shared queue: the first
			// engine in the list swallowed every packet, discarded the ones
			// addressed to its siblings, and those spaces then never synced
			// over this link at all. It looked like an occasional slow test
			// because the list order comes from a map.
			var beacons [][]byte
			for _, pkt := range c.Poll() {
				raw, err := reasm.Feed(pkt)
				if errors.Is(err, kernelsync.ErrNotFragment) {
					raw, err = pkt, nil // a wrapper may deliver whole messages
				}
				if err != nil || raw == nil {
					continue
				}
				// A gateway beacon is about the SEGMENT, not about a space:
				// it carries no terminal id, so it cannot be routed to an
				// engine. Collected here and folded in after the lock, since
				// noteBeacon takes r.mu itself.
				if b, ok := kernelsync.ExtractBeacon(raw); ok {
					beacons = append(beacons, b)
					continue
				}
				term, ok := kernelsync.PeekTerminal(raw)
				if !ok {
					continue
				}
				// A forbidden transport is not read either: accepting frames
				// on it would still mean this device is talking there.
				if !conn.allows(kind, term) {
					continue
				}
				if st := byTerm[term]; st != nil {
					if _, _, err := st.eng.Handle(c, raw); err != nil && sendErr == nil {
						sendErr = err
					}
				}
			}
			r.curLink = ""
			r.mu.Unlock()

			for _, b := range beacons {
				r.noteBeacon(label, b, time.Now())
			}

			// Health is recorded from what ACTUALLY happened on the wire,
			// not from what policy hoped for — and only when we tried to
			// send. A pass where this space had nothing outstanding says
			// nothing about whether the route works, and counting it as a
			// success would keep a dead link looking healthy forever.
			if len(sending) > 0 {
				r.noteTransportResult(kind, sendErr, now)
			}
		}
	}()
}

func (r *Runtime) dropConn(c link) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, st := range r.spaces {
		kept := st.conns[:0]
		for _, x := range st.conns {
			if x != c {
				kept = append(kept, x)
			}
		}
		st.conns = kept
	}
	// And out of the registry, or a space created after this link died
	// would be wired into a corpse.
	keptLinks := r.links[:0]
	for _, l := range r.links {
		if l.c != c {
			keptLinks = append(keptLinks, l)
		}
	}
	r.links = keptLinks
}

// LAN reports transport diagnostics.
func (r *Runtime) LAN() LANStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	peers := map[link]bool{}
	for _, st := range r.spaces {
		for _, c := range st.conns {
			if closed, _ := c.Closed(); !closed {
				peers[c] = true
			}
		}
	}
	return LANStatus{Listening: r.lanNode != nil, Port: r.lanPort, Peers: len(peers)}
}

// ConnectPeer dials a peer directly (manual connection, also used in tests).
func (r *Runtime) ConnectPeer(addr string) error {
	r.mu.Lock()
	n := r.lanNode
	r.mu.Unlock()
	if n == nil {
		return errNoLAN
	}
	c, err := n.Dial(addr)
	if err != nil {
		return err
	}
	r.adoptConn(c)
	return nil
}

var errNoLAN = errLAN("node: LAN not started")

type errLAN string

func (e errLAN) Error() string { return string(e) }
