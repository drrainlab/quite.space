// LAN auto-sync (M1.1 wired into the runtime): announce rotating hints for
// every space, dial peers whose announces match, and pump the sync engine
// over each live connection. No coordinator, no server — two nodes on one
// network find each other and converge.
package node

import (
	"crypto/rand"
	"encoding/binary"
	"time"

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
		r.mu.Lock()
		match := false
		for tid := range r.spaces {
			if lan.MatchHint(a, tid, now) {
				match = true
				break
			}
		}
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

func (r *Runtime) announceOnce(announceAddr string, port int, nonce uint64) {
	now := uint64(time.Now().Unix())
	r.mu.Lock()
	hints := make([][]byte, 0, len(r.spaces))
	for tid := range r.spaces {
		hints = append(hints, lan.Hint(tid, lan.Bucket(now)))
	}
	r.mu.Unlock()
	if len(hints) == 0 {
		return
	}
	_ = lan.AnnounceOnce(announceAddr, port, hints, nonce)
}

// adoptConn attaches a LAN connection with realtime cadence.
func (r *Runtime) adoptConn(c *lan.Conn) {
	r.adoptLink(c, pumpEvery, 2*time.Second)
}

// adoptLink attaches any link to every space and pumps it until it dies.
// Frames for other terminals are simply not matched by the engines — each
// engine checks its own terminal id. Cadence is per-transport: a LAN link
// pumps in milliseconds; a LoRa link must respect airtime (plan §19 T6).
func (r *Runtime) adoptLink(c link, pump, summaryEvery time.Duration) {
	r.mu.Lock()
	states := make([]*spaceState, 0, len(r.spaces))
	for _, st := range r.spaces {
		st.conns = append(st.conns, c)
		states = append(states, st)
	}
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := time.NewTicker(pump)
		defer t.Stop()
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
			r.mu.Lock()
			if time.Since(lastSummary) > summaryEvery {
				for _, st := range states {
					_ = st.eng.SendSummary(c)
				}
				lastSummary = time.Now()
			}
			for _, st := range states {
				_, _, _ = st.eng.Pump(c)
			}
			r.mu.Unlock()
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
