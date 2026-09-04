package lan

import (
	"crypto/tls"
	"strconv"
	"sync"
	"testing"
	"time"

	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/human"
)

// syncNode couples a space replica with a sync engine for tests.
type syncNode struct {
	space *terminals.Space
	eng   *kernelsync.Engine
}

func newSyncNode(t *testing.T, s *terminals.Space) *syncNode {
	t.Helper()
	n := &syncNode{space: s, eng: kernelsync.NewEngine(s.Log)}
	n.eng.OnApplied = s.AttachSyncApply
	return n
}

// drive pumps both engines over a live connection until both states agree
// or the deadline passes.
func drive(t *testing.T, a, b *syncNode, ca, cb *Conn) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := a.eng.SendSummary(ca); err != nil {
			t.Fatal(err)
		}
		if err := b.eng.SendSummary(cb); err != nil {
			t.Fatal(err)
		}
		for range 20 {
			a.eng.Pump(ca)
			b.eng.Pump(cb)
			time.Sleep(5 * time.Millisecond)
			if a.space.Log.Len() == b.space.Log.Len() &&
				a.space.State.Digest() == b.space.State.Digest() &&
				a.space.Log.Len() > 0 {
				return
			}
		}
	}
	t.Fatalf("no convergence: lenA=%d lenB=%d", a.space.Log.Len(), b.space.Log.Len())
}

func buildSpaces(t *testing.T) (*syncNode, *syncNode, *terminals.Participant) {
	t.Helper()
	alice, err := human.New("alice")
	if err != nil {
		t.Fatal(err)
	}
	spaceA, err := terminals.NewSpace("LAN Session", alice.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := human.Say(alice, spaceA, "hello over the LAN", human.SayOptions{}, 100); err != nil {
		t.Fatal(err)
	}
	spaceB := terminals.Replica(spaceA.ID)
	return newSyncNode(t, spaceA), newSyncNode(t, spaceB), alice
}

func TestDirectDialAndSync(t *testing.T) {
	a, b, _ := buildSpaces(t)

	server, err := NewNode()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	connCh := make(chan *Conn, 1)
	port, err := server.Listen("127.0.0.1:0", func(c *Conn) { connCh <- c })
	if err != nil {
		t.Fatal(err)
	}

	client, err := NewNode()
	if err != nil {
		t.Fatal(err)
	}
	cb, err := client.Dial("127.0.0.1:" + itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	ca := <-connCh

	drive(t, a, b, ca, cb)
	if len(b.space.State.Messages()) != 1 {
		t.Fatal("message did not arrive over LAN")
	}
}

func TestReconnectResumes(t *testing.T) {
	a, b, alice := buildSpaces(t)

	server, err := NewNode()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	connCh := make(chan *Conn, 4)
	port, err := server.Listen("127.0.0.1:0", func(c *Conn) { connCh <- c })
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewNode()
	if err != nil {
		t.Fatal(err)
	}

	// First session.
	cb, err := client.Dial("127.0.0.1:" + itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	ca := <-connCh
	drive(t, a, b, ca, cb)

	// Drop the link; more writes happen while disconnected.
	cb.Close()
	if _, err := human.Say(alice, a.space, "written while disconnected", human.SayOptions{}, 200); err != nil {
		t.Fatal(err)
	}

	// Redial: summaries resume the sync with no special-case state.
	cb2, err := client.Dial("127.0.0.1:" + itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	ca2 := <-connCh
	drive(t, a, b, ca2, cb2)
	if len(b.space.State.Messages()) != 2 {
		t.Fatalf("reconnect did not resume: %d messages", len(b.space.State.Messages()))
	}
}

func TestSendOnClosedConnErrors(t *testing.T) {
	server, err := NewNode()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	connCh := make(chan *Conn, 1)
	port, err := server.Listen("127.0.0.1:0", func(c *Conn) { connCh <- c })
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewNode()
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.Dial("127.0.0.1:" + itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	<-connCh
	c.Close()
	time.Sleep(20 * time.Millisecond)
	if err := c.Send([]byte{1}); err == nil {
		t.Fatal("send on closed connection succeeded")
	}
}

func TestDiscoveryHints(t *testing.T) {
	term := id.TerminalID{0xD0}
	other := id.TerminalID{0xD1}
	now := uint64(1_753_142_400)

	// Hints rotate per bucket and never expose the terminal id.
	h1 := Hint(term, Bucket(now))
	h2 := Hint(term, Bucket(now)+1)
	if string(h1) == string(h2) {
		t.Fatal("hints do not rotate")
	}
	for i := range term {
		if i+len(h1) <= len(term) && string(term[i:i+len(h1)]) == string(h1) {
			t.Fatal("hint leaks terminal id bytes")
		}
	}

	// Announce over localhost UDP; the matching terminal is found, the
	// other is not.
	var mu sync.Mutex
	var got []Announcement
	addr, stop, err := ListenAnnounces("127.0.0.1:0", func(a Announcement) {
		mu.Lock()
		got = append(got, a)
		mu.Unlock()
	})
	if err != nil {
		t.Skipf("UDP unavailable in this environment: %v", err)
	}
	defer stop()
	if err := AnnounceOnce(addr, 4242, [][]byte{Hint(term, Bucket(now))}, 77); err != nil {
		t.Skipf("UDP send unavailable: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("announce not received")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	a := got[0]
	mu.Unlock()
	if a.Port != 4242 {
		t.Fatalf("port mangled: %d", a.Port)
	}
	if !MatchHint(a, term, now) {
		t.Fatal("known terminal not matched")
	}
	if MatchHint(a, other, now) {
		t.Fatal("unrelated terminal matched")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// QI-B1 Ф0: the TLS-floor decision, pinned. Two Go nodes must still
// negotiate TLS 1.3 between themselves — lowering the FLOOR to 1.2 for an
// ESP32's sake does not downgrade peer-to-peer, because crypto/tls picks
// the highest both offer. And the exported keying material the instrument
// door's knock signs over must be available on the negotiated session.
func TestPeersStillNegotiateTLS13AndExportKeyingMaterial(t *testing.T) {
	server, err := NewNode()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	connCh := make(chan *Conn, 1)
	port, err := server.Listen("127.0.0.1:0", func(c *Conn) { connCh <- c })
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewNode()
	if err != nil {
		t.Fatal(err)
	}
	cb, err := client.Dial("127.0.0.1:" + itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	ca := <-connCh

	// The exporter forces the lazy accept-side handshake to complete.
	ekmA, okA := ca.SessionBinding("qp-instr-door-v0")
	ekmB, okB := cb.SessionBinding("qp-instr-door-v0")
	if !okA || !okB {
		t.Fatal("no exported keying material on a peer session")
	}
	if len(ekmA) != 32 || len(ekmB) != 32 {
		t.Fatal("exported keying material is the wrong length")
	}
	// Same label, same session, both ends: the two exporters MUST match —
	// that agreement is exactly what makes the door's knock unforgeable.
	if string(ekmA) != string(ekmB) {
		t.Fatal("the two ends exported different keying material")
	}

	sa, ok := ca.c.(*tls.Conn)
	if !ok {
		t.Fatal("not a TLS conn")
	}
	if v := sa.ConnectionState().Version; v != tls.VersionTLS13 {
		t.Fatalf("two Go peers negotiated 0x%04x, not TLS 1.3 — the floor leaked into the ceiling", v)
	}
}
