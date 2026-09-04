// LAN auto-sync (M1.1 wired into the runtime): announce rotating hints for
// every space, dial peers whose announces match, and pump the sync engine
// over each live connection. No coordinator, no server — two nodes on one
// network find each other and converge.
package node

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/drrainlab/quiet_places/kernel/routing"
	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/lan"
)

const (
	// announceEvery is 1.5 background cadences — 3s as shipped. Kept as a
	// ratio (see cadence.go) so the LAN beacon stays in step with the rest
	// of the node's heartbeat under test as well as in the world.
	announceEvery = 1.5
	pumpEvery     = 300 * time.Millisecond
)

// LANStatus reports transport diagnostics for the UI (plan §8.2: the user
// always sees how they are connected).
type LANStatus struct {
	Listening bool
	Port      int
	Peers     int
	// Bound counts peers AUTHENTICATED on those links (T6-LAN hello) — the
	// devices whose copies the room is actually carrying. Peers counts
	// sockets; Bound counts people the chip may honestly name.
	Bound int
}

// DefaultLANMulticast is the announce address production uses.
//
// Re-exported so an entry point need not import a transport package to start
// the ordinary local network. The desktop shell keeps its imports down to
// node and the lock gate deliberately — ADR-011 puts the UI boundary at the
// local HTTP API, and a shell that reaches past it stops being a host.
const DefaultLANMulticast = lan.MulticastAddr

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
	lanStop := make(chan struct{})
	r.mu.Lock()
	r.lanNode = n
	r.lanPort = port
	r.lanStop = lanStop
	r.mu.Unlock()

	// Announce loop.
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := cadenceTicker(announceEvery)
		defer t.Stop()
		for {
			// A pocket does not need to be discoverable. The multicast
			// TX every three seconds is pure spend with the screen dark —
			// LAN delivery is a convenience the relay covers the moment
			// it stops — so announcing pauses with attention and resumes
			// with it. The LISTENER stays up: a peer that still announces
			// can be heard for free, and the Android shell releases its
			// multicast lock in the background anyway.
			if r.foregrounded() || r.DiscoverableInBackground {
				r.announceOnce(announceAddr, port, selfNonce)
			}
			select {
			case <-r.stop:
				return
			case <-lanStop:
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
		select {
		case <-r.stop:
		case <-lanStop:
		}
		stopListen()
	}()
	return nil
}

// StopLAN ends the LAN lifecycle StartLAN began — listener, announces,
// discovery, and every live local link — without touching the rest of the
// runtime. The room emptied; the node lives on. Idempotent.
func (r *Runtime) StopLAN() {
	r.mu.Lock()
	n, stop := r.lanNode, r.lanStop
	r.lanNode, r.lanPort, r.lanStop = nil, 0, nil
	var conns []link
	for _, ll := range r.links {
		if transportOfLink(ll.label) == TransportLAN {
			conns = append(conns, ll.c)
		}
	}
	r.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if n != nil {
		n.Close()
	}
	// Closing the conns is what the peers can SEE: their pump ticks notice,
	// drop the link, and evict any observed route bound to it.
	for _, c := range conns {
		if cl, ok := c.(interface{ Close() error }); ok {
			cl.Close()
		}
	}
}

// lanPeerDevice reports whether dev is authenticated live on a local link —
// the observed-route query (T6-LAN). True requires BOTH halves: the device
// proved its key on this very TLS session, and the link is still up. This
// is the only door through which "local" may influence delivery.
func (r *Runtime) lanPeerDevice(dev id.DeviceID) bool {
	r.mu.Lock()
	l, ok := r.lanPeers[dev]
	r.mu.Unlock()
	if !ok || l == nil {
		return false
	}
	closed, _ := l.Closed()
	return !closed
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
	// A refused announce is a fact worth one line: macOS answers a
	// process without the Local Network permission with EPIPE on every
	// multicast write, and a node that logs "announcing to" while nothing
	// leaves the socket sends a person hunting a phantom (QI-B1 gate).
	if err := lan.AnnounceOnce(announceAddr, port, hints, nonce); err != nil {
		r.announceFailOnce.Do(func() {
			fmt.Println("LAN: announce refused —", err, "— peers and instruments cannot find this node by announcement")
		})
	}
}

// adoptConn hands a LAN connection to the classifier (QI-B1): the first
// message decides whether this is a peer or an instrument at the door.
func (r *Runtime) adoptConn(c *lan.Conn) {
	r.classifyLANConn(c)
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

// quietLinkFactor is how much less often a space whose log has not grown
// offers a summary on a metered link. Same intent as relaysync's quietEvery
// (node/relaysync.go:66-76), expressed in the pump's own vocabulary: a
// per-space timer rather than a global tick count. Renderer-side policy,
// never a contract.
const quietLinkFactor = 5

// airtimeFloorBytes is the least a transmission is charged against the link's
// airtime budget, whatever the payload weighs. On LoRa the cost of putting
// anything on the air is dominated by the transmission itself — about two
// seconds at LONG_FAST — so a byte budget that charged a small summary its
// small size would approve far more transmissions than the band can hold.
const airtimeFloorBytes = 200

// activeSpace is a space this link may carry on this tick, with its identity
// kept alongside it — the cadence below needs the terminal id to stagger and
// to remember, and spaceState does not carry its own.
type activeSpace struct {
	tid id.TerminalID
	st  *spaceState
}

// linkCadence decides which spaces may offer a summary on this tick.
//
// The relay loop already learned this and wrote it down (relaysync.go:66-76):
// a space that has not changed does not need announcing as often as one that
// has, and the quiet ones must not all wake on the same tick. The link pump
// never learned it, and on a radio the arithmetic is brutal — six spaces on
// ONE link-wide timer is six whole messages handed over in a single tick,
// every minute, to a carrier that moves about one message every thirty
// seconds. Oversubscribed threefold before anybody presses anything, which is
// why an invitation could sit behind minutes of sync and arrive at nobody.
//
// TWO REGIMES, because two carriers:
//
//	unmetered (LAN)  every active space offers on one shared timer, exactly as
//	                 before. There is room, and pacing would only slow a link
//	                 that was never the bottleneck.
//	metered (radio)  at most ONE space offers per tick — a space whose log
//	                 grew going first, then the longest-waiting — and only if
//	                 the airtime budget can pay for it.
type linkCadence struct {
	paced   bool
	air     *routing.TokenBucket
	shared  time.Time // the unmetered regime's single timer
	last    map[id.TerminalID]time.Time
	seenLen map[id.TerminalID]int
}

func newLinkCadence(paced bool, now time.Time) *linkCadence {
	lc := &linkCadence{paced: paced,
		last: map[id.TerminalID]time.Time{}, seenLen: map[id.TerminalID]int{}}
	if paced {
		// SYNC ONLY, and that is the whole point of putting the budget here
		// rather than inside the endpoint. Airtime spent on a summary is
		// background traffic; a control message somebody is standing in a
		// field waiting for never asks this budget for permission. Background
		// yields to the person.
		lc.air = routing.DefaultAirtime(now)
	}
	return lc
}

// due returns the spaces that may offer a summary now, and charges whatever
// budget they spend.
func (lc *linkCadence) due(active []activeSpace, every time.Duration,
	now time.Time) []activeSpace {

	if !lc.paced {
		if now.Sub(lc.shared) <= every {
			return nil
		}
		lc.shared = now
		return active
	}

	var best activeSpace
	var bestAt time.Time
	bestGrew, found := false, false
	for _, a := range active {
		n := a.st.space.Log.Len()
		grew := n != lc.seenLen[a.tid]
		prev, seen := lc.last[a.tid]
		if !seen {
			// A space this link has not announced yet is due AT ONCE.
			//
			// The one-per-tick rule below is what prevents the burst; six new
			// spaces simply take six ticks. Staggering the FIRST offer as well
			// would mean a space created during a session waits a whole
			// interval before anybody hears of it — a minute of silence on a
			// radio, which is the late-space bug wearing a different hat and
			// is exactly what mesh_latespace_test.go caught when this line
			// tried to be clever.
			prev = now.Add(-every)
			lc.last[a.tid] = prev
		}
		wait := every
		if !grew {
			wait = every * quietLinkFactor
		}
		if now.Sub(prev) < wait {
			continue
		}
		if !found || (grew && !bestGrew) ||
			(grew == bestGrew && prev.Before(bestAt)) {
			best, bestAt, bestGrew, found = a, prev, grew, true
		}
	}
	if !found {
		return nil
	}
	// Priced from the encoder, not guessed — but floored, because on LoRa a
	// packet costs a packet. Airtime is dominated by per-transmission overhead,
	// not by payload size, so charging an 86-byte summary 86 bytes would let
	// the budget approve far more transmissions than the band has room for. The
	// floor makes 2000 bytes a minute mean about ten transmissions a minute for
	// background sync, leaving the rest of the air for what a person is waiting
	// on.
	//
	// A budget the link cannot pay leaves lc.last untouched, so the space is
	// still owed its turn on the next tick rather than silently losing it.
	cost := best.st.eng.SummarySize()
	if cost < airtimeFloorBytes {
		cost = airtimeFloorBytes
	}
	if lc.air != nil && !lc.air.Take(cost, now) {
		return nil
	}
	return []activeSpace{best}
}

// done records what actually happened to a summary this cadence released.
//
// A carrier that had no room did not carry it, so the turn is NOT spent —
// exactly as an unaffordable airtime budget does not spend it. Advancing the
// timer on a send that never left is the same class of mistake as the old
// shared timer advancing whether or not anything was sent.
func (lc *linkCadence) done(a activeSpace, err error, now time.Time) {
	if !lc.paced || errors.Is(err, kernelsync.ErrCarrierFull) {
		return
	}
	lc.last[a.tid] = now
	lc.seenLen[a.tid] = a.st.space.Log.Len()
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
	r.adoptLinkFilteredOpts(c, pump, summaryEvery, label, allow, true)
}

// adoptLinkFilteredOpts is adoptLinkFiltered with the hello made explicit:
// the classifier in front of the LAN accept (QI-B1) says hello BEFORE it
// reads, so the adoption that follows must not say it twice.
func (r *Runtime) adoptLinkFilteredOpts(c link, pump, summaryEvery time.Duration,
	label string, allow func(routing.FrameMeta) bool, sayHello bool) {
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

	// Introduce this device on the new wire (T6-LAN). Only links that can
	// bind a signature to their own session say anything; the rest stay
	// anonymous and never become route candidates.
	if sayHello {
		r.sendLANHello(c)
	}

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
		cad := newLinkCadence(linkKind == TransportRadio, time.Now())
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
			active := make([]activeSpace, 0, len(byTerm))
			for tid, st := range byTerm {
				if !conn.allows(kind, tid) {
					continue
				}
				route, has := r.routeForSpaceLocked(conn, tid, now)
				if has && route != kind {
					continue // something to send, but not by this road
				}
				active = append(active, activeSpace{tid: tid, st: st})
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
			// A FULL CARRIER IS NOT A BROKEN ONE.
			//
			// sendErr feeds noteTransportResult, which decides whether this
			// transport is healthy. A radio that is momentarily out of queue
			// room is working exactly as intended — counting that as a
			// failure would mark the busiest links unhealthy and eventually
			// stop using them, which is the opposite of what backpressure is
			// for. It is filtered here, once, rather than at each site.
			var sendErr error
			note := func(err error) {
				if err == nil || errors.Is(err, kernelsync.ErrCarrierFull) {
					return
				}
				if sendErr == nil {
					sendErr = err
				}
			}
			// The cadence decides WHO offers, and charges what they spend.
			// Note what it deliberately does not do: it never advances a
			// space's timer for a summary the carrier refused, so a link that
			// is merely busy still owes that space its turn. The old shared
			// timer advanced unconditionally, which is how six spaces stayed
			// exactly one minute apart from each other forever.
			for _, a := range cad.due(active, summaryEvery, now) {
				err := a.st.eng.SendSummary(c)
				note(err)
				cad.done(a, err, now)
			}
			// Poll ONCE and route by terminal. Letting each space call
			// Pump would mean each one draining the shared queue: the first
			// engine in the list swallowed every packet, discarded the ones
			// addressed to its siblings, and those spaces then never synced
			// over this link at all. It looked like an occasional slow test
			// because the list order comes from a map.
			var beacons [][]byte
			var hellos [][]byte
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
				// A hello is about the LINK, like a beacon: no terminal id,
				// nothing to route. Verified after the lock (noteLANHello
				// takes r.mu itself).
				if h, ok := kernelsync.ExtractHello(raw); ok {
					hellos = append(hellos, h)
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
					if _, _, err := st.eng.Handle(c, raw); err != nil {
						note(err)
					}
				}
			}
			r.curLink = ""
			r.mu.Unlock()

			for _, b := range beacons {
				r.noteBeacon(label, b, time.Now())
			}
			for _, h := range hellos {
				r.noteLANHello(c, h)
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
	var deadEP interface{ Close() error }
	r.mu.Lock()
	// A LINK THAT DIES TAKES ITS SLOT WITH IT. The radio is one-per-node, and
	// the flag saying so was only ever cleared by DetachRadio — the explicit
	// switch. So a modem that was simply unplugged left the slot held by
	// nothing, and plugging it back in was refused. Reaping the link is
	// exactly the moment the node learns the radio is gone, so it is where
	// the slot is given back.
	//
	// The SEED and the remembered record stay. Unplugging is not detaching:
	// somebody who pulls a cable still means to have that radio, and asking
	// them for their segment phrase again would be punishing them for it.
	if r.rnodeLink != nil && r.rnodeLink == c {
		if r.rnodeEP != nil {
			deadEP = r.rnodeEP
		}
		r.rnodeRadio, r.rnodeEP, r.rnodeLink = nil, nil, nil
		r.meshSupervised = false
	}
	boundDied := false
	for dev, l := range r.lanPeers {
		if l == c {
			delete(r.lanPeers, dev)
			boundDied = true
		}
	}
	for _, st := range r.spaces {
		kept := st.conns[:0]
		for _, x := range st.conns {
			if x != c {
				kept = append(kept, x)
			}
		}
		st.conns = kept
		// Link-scoped sync state dies with the link.
		st.eng.ForgetEndpoint(c)
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
	r.mu.Unlock()
	// A device whose relay copies were being suppressed just left the wire
	// (T6-LAN). Whatever was handed to that wire and not yet converged has
	// no other carrier now — so the relay push progress resets and the next
	// cycle re-offers the whole log once. Dedup makes the re-offer
	// idempotent; skipping it makes a quiet space lossy.
	if boundDied {
		if rs := r.relaySync; rs != nil {
			rs.mu.Lock()
			rs.lastLen = map[id.TerminalID]int{}
			rs.mu.Unlock()
		}
	}
	// Outside the lock: closing an endpoint can reach back into the runtime,
	// and a deadlock here would freeze every space at once.
	if deadEP != nil {
		_ = deadEP.Close()
	}
}

// LAN reports transport diagnostics.
//
// PEERS MEANS PEERS ON THE LOCAL NETWORK, which it did not use to. The count
// was every live link in the registry, so a radio — one link, to the air —
// arrived here as a LAN peer. Nothing noticed while the chip preferred radio
// whenever a modem was attached; the moment it started preferring a live
// local link, two devices talking over LoRa in a field with no Wi-Fi at all
// announced themselves as "Direct · 1".
//
// A field named lan.peers is read as a fact by everything downstream. It is
// counted from the link registry's own labels now, which is where the
// transport of a link has been recorded all along.
func (r *Runtime) LAN() LANStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	peers := 0
	for _, l := range r.links {
		if transportOfLink(l.label) != TransportLAN {
			continue
		}
		if closed, _ := l.c.Closed(); closed {
			continue
		}
		peers++
	}
	bound := 0
	for _, l := range r.lanPeers {
		if l == nil {
			continue
		}
		if closed, _ := l.Closed(); closed {
			continue
		}
		bound++
	}
	return LANStatus{Listening: r.lanNode != nil, Port: r.lanPort, Peers: peers, Bound: bound}
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
