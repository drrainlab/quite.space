// Adapter conformance suite (M0.7 acceptance): the same sync scenario must
// pass over every transport with zero kernel changes — clean loopback,
// hostile loopback, every low-bandwidth simulator profile, and bundle files.
// Convergence check: identical reducer digests on both nodes (M0.6
// acceptance: two offline nodes reach the same state).
package sync

import (
	"bytes"
	"crypto/ed25519"
	"path/filepath"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/reducers"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/transports"
	"github.com/drrainlab/quiet_places/transports/bundle"
	"github.com/drrainlab/quiet_places/transports/loopback"
	"github.com/drrainlab/quiet_places/transports/simulator"
)

type author struct {
	priv ed25519.PrivateKey
	dev  id.DeviceID
	prin id.PrincipalID
	term id.TerminalID
	seq  uint64
	tip  id.EventID
	clk  uint64
}

func newAuthor(t *testing.T, term id.TerminalID, seed byte) *author {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	a := &author{priv: priv, term: term}
	copy(a.dev[:], priv.Public().(ed25519.PublicKey))
	a.prin[0] = seed
	return a
}

func (a *author) frame(t *testing.T, schema string, payload []byte) []byte {
	t.Helper()
	a.seq++
	a.clk++
	env := &signal.Envelope{
		Terminal: a.term, Principal: a.prin, Device: a.dev,
		Sequence: a.seq, Schema: schema, LogicalClock: a.clk,
		ProducedBy: signal.AuthorshipHuman, PayloadEncoding: signal.PayloadCBOR,
		Payload: payload, Priority: signal.PriorityMessage,
	}
	if a.seq > 1 {
		prev := a.tip
		env.Previous = &prev
	}
	f, err := env.Sign(a.priv)
	if err != nil {
		t.Fatal(err)
	}
	a.tip = id.EventIDOf(f)
	return f
}

func (a *author) message(t *testing.T, text string) []byte {
	t.Helper()
	p, err := (&schemas.TextMessage{Text: text}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return a.frame(t, schemas.MessageText, p)
}

type node struct {
	log   *eventlog.Log
	eng   *Engine
	state *reducers.State
}

func newNode(term id.TerminalID) *node {
	n := &node{log: eventlog.New(term, nil), state: reducers.NewState()}
	n.eng = NewEngine(n.log)
	n.eng.OnApplied = func(a eventlog.Applied) { n.state.Apply(a.Env, a.ID) }
	return n
}

func (n *node) write(t *testing.T, frames ...[]byte) {
	t.Helper()
	for _, f := range frames {
		applied, err := n.log.Ingest(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range applied {
			n.state.Apply(a.Env, a.ID)
		}
	}
}

// scenario: both nodes write offline (messages + a shared card story), then
// converge over the given link.
func buildScenario(t *testing.T, term id.TerminalID) (*node, *node) {
	t.Helper()
	nodeA, nodeB := newNode(term), newNode(term)
	alice := newAuthor(t, term, 0x51)
	bob := newAuthor(t, term, 0x52)

	for i := 0; i < 8; i++ {
		nodeA.write(t, alice.message(t, "alice offline note"))
		nodeB.write(t, bob.message(t, "bob offline note"))
	}
	// Shared object edited on both sides (deterministic merge via LWW).
	cardPayload, err := (&schemas.Card{Title: "Record field ambience", Status: "open"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	cardFrame := alice.frame(t, schemas.CardCreated, cardPayload)
	cardID := id.EventIDOf(cardFrame)
	nodeA.write(t, cardFrame)

	alice.clk = 20 // concurrent-ish edits with distinct clocks
	updA, err := (&schemas.Card{Title: "Record field ambience", Status: "open", Card: &cardID}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	nodeA.write(t, alice.frame(t, schemas.CardUpdated, updA))

	bob.clk = 30
	updB, err := (&schemas.Card{Title: "Record field ambience", Status: "done", Card: &cardID}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	nodeB.write(t, bob.frame(t, schemas.CardUpdated, updB))
	return nodeA, nodeB
}

func converge(t *testing.T, a, b *node, epA, epB transports.Endpoint, maxRounds int) {
	t.Helper()
	for round := 0; round < maxRounds; round++ {
		if err := a.eng.SendSummary(epA); err != nil {
			t.Fatal(err)
		}
		if err := b.eng.SendSummary(epB); err != nil {
			t.Fatal(err)
		}
		// Drain until both sides go quiet this round.
		for i := 0; i < 64; i++ {
			na, _, err := a.eng.Pump(epA)
			if err != nil {
				t.Fatal(err)
			}
			nb, _, err := b.eng.Pump(epB)
			if err != nil {
				t.Fatal(err)
			}
			if na == 0 && nb == 0 && len(epAPeek(epA)) == 0 {
				break
			}
		}
		if a.state.Digest() == b.state.Digest() && a.log.Len() == b.log.Len() && a.log.Len() > 0 {
			return
		}
	}
	t.Fatalf("no convergence after %d rounds: lenA=%d lenB=%d", maxRounds, a.log.Len(), b.log.Len())
}

// epAPeek is a no-op placeholder (Poll drains; emptiness is implied by
// na==nb==0 across a pass). Kept for readability of the loop condition.
func epAPeek(transports.Endpoint) [][]byte { return nil }

func checkConverged(t *testing.T, a, b *node) {
	t.Helper()
	if a.state.Digest() != b.state.Digest() {
		t.Fatal("states diverged")
	}
	msgs := a.state.Messages()
	if len(msgs) != 16 {
		t.Fatalf("expected 16 messages, got %d", len(msgs))
	}
	cards := a.state.Cards()
	if len(cards) != 1 || cards[0].Status != "done" {
		t.Fatalf("card merge wrong: %+v", cards)
	}
}

func TestSyncCleanLoopback(t *testing.T) {
	term := id.TerminalID{0xC0}
	a, b := buildScenario(t, term)
	pair := loopback.NewPair(loopback.Faults{Seed: 1})
	converge(t, a, b, pair.A, pair.B, 10)
	checkConverged(t, a, b)
}

func TestSyncHostileLoopback(t *testing.T) {
	term := id.TerminalID{0xC1}
	a, b := buildScenario(t, term)
	pair := loopback.NewPair(loopback.Faults{
		Seed: 42, DropRate: 0.25, DuplicateRate: 0.10, Reorder: true,
	})
	converge(t, a, b, pair.A, pair.B, 200)
	checkConverged(t, a, b)
}

func TestSyncSimulatorProfiles(t *testing.T) {
	for _, profile := range []simulator.Profile{
		simulator.Lora64, simulator.Lora128, simulator.Mesh240,
	} {
		t.Run(profile.Name, func(t *testing.T) {
			term := id.TerminalID{0xC2}
			a, b := buildScenario(t, term)
			pair := simulator.NewPair(profile, 7)
			converge(t, a, b, pair.A, pair.B, 2000)
			checkConverged(t, a, b)
		})
	}
}

func TestSyncPartitionResume(t *testing.T) {
	term := id.TerminalID{0xC3}
	a, b := buildScenario(t, term)
	pair := loopback.NewPair(loopback.Faults{Seed: 3})

	// Start syncing, then cut the link mid-flight.
	a.eng.SendSummary(pair.A)
	b.eng.SendSummary(pair.B)
	a.eng.Pump(pair.A)
	pair.Partition(true)
	a.eng.Pump(pair.A)
	b.eng.Pump(pair.B)

	// Both keep writing while partitioned.
	carol := newAuthor(t, term, 0x53)
	dave := newAuthor(t, term, 0x54)
	a.write(t, carol.message(t, "written during partition on A"))
	b.write(t, dave.message(t, "written during partition on B"))

	// Reconnect: sync resumes from summaries, no state reset needed.
	pair.Partition(false)
	converge(t, a, b, pair.A, pair.B, 20)
	if a.state.Digest() != b.state.Digest() {
		t.Fatal("states diverged after partition")
	}
	if len(a.state.Messages()) != 18 {
		t.Fatalf("expected 18 messages, got %d", len(a.state.Messages()))
	}
}

func TestSyncBundleTransport(t *testing.T) {
	term := id.TerminalID{0xC4}
	a, b := buildScenario(t, term)
	dir := t.TempDir()

	// Sneakernet round trip: A exports, B imports; B exports, A imports.
	exchange := func(from, to *node, name string) {
		t.Helper()
		var frames [][]byte
		if err := from.log.Replay(func(ap eventlog.Applied) error {
			frames = append(frames, ap.Frame)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := bundle.Write(path, term, frames); err != nil {
			t.Fatal(err)
		}
		gotTerm, gotFrames, err := bundle.Read(path)
		if err != nil {
			t.Fatal(err)
		}
		if gotTerm != term {
			t.Fatal("terminal id mangled in bundle")
		}
		for _, f := range gotFrames {
			applied, err := to.log.Ingest(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, ap := range applied {
				to.state.Apply(ap.Env, ap.ID)
			}
		}
	}
	exchange(a, b, "a.terminal-bundle")
	exchange(b, a, "b.terminal-bundle")
	checkConverged(t, a, b)
	// Re-import is a no-op (idempotency).
	before := a.log.Len()
	exchange(b, a, "b2.terminal-bundle")
	if a.log.Len() != before {
		t.Fatal("re-import duplicated events")
	}
}
