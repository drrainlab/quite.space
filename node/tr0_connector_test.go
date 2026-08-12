// TR-0c — the connector seam's invariants (plan rev 4), red first.
//
// External input becomes durable local state BEFORE it becomes anybody's
// space event; one transport id is one event; a route change is a temporal
// boundary that nothing crosses in either direction; and a crash between
// the emit and the settle is reconciled against the space's own log —
// exactly once, or honestly downgraded.
package node

import (
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/terminals/gateway"
)

// countImported counts entries whose text matches AND that carry foreign
// provenance — the projector's own product, not just any message.
func countImported(t *testing.T, rt *Runtime, tid id.TerminalID, text string) int {
	t.Helper()
	n := 0
	_ = rt.withSpace(tid, func(st *spaceState) error {
		for _, e := range st.space.State.Entries() {
			if e.Content.Text != nil && e.Content.Text.Text == text &&
				e.Content.Text.External != nil {
				n++
			}
		}
		return nil
	})
	return n
}

func fixtureEnv(extID, text string) ExternalEnvelope {
	return ExternalEnvelope{
		ExternalID: extID, Kind: "fixture",
		Address: "someone@example.org", Text: text,
		ObservedAt: time.Now().Unix(),
	}
}

func waitProjected(t *testing.T, rt *Runtime, tid id.TerminalID, text string) {
	t.Helper()
	waitUntil(t, 15*time.Second, "the ingress never became a space event: "+text, func() bool {
		return countImported(t, rt, tid, text) >= 1
	})
}

// One transport id, one event — across duplicate deliveries and however
// many projector passes happen to run.
func TestExternalIDIsIdempotent(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "term")
	defer rt.Close()
	tid, err := rt.CreateSpace("✉ mailbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.ConnectorRoute("fix", tid); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := rt.ConnectorIngest("fix", fixtureEnv("mail-1", "одно письмо")); err != nil {
			t.Fatal(err)
		}
	}
	waitProjected(t, rt, tid, "одно письмо")
	time.Sleep(2 * time.Second)
	if n := countImported(t, rt, tid, "одно письмо"); n != 1 {
		t.Fatalf("one external id produced %d events", n)
	}
	st, err := rt.ConnectorStatus("fix")
	if err != nil {
		t.Fatal(err)
	}
	if st.Published != 1 || st.Pending != 0 {
		t.Fatalf("journal disagrees: %+v", st)
	}
	// And the projection is honestly marked: imported authorship, key 7.
	_ = rt.withSpace(tid, func(sst *spaceState) error {
		for _, e := range sst.space.State.Entries() {
			if e.Content.Text == nil || e.Content.Text.External == nil {
				continue
			}
			if e.Content.Text.External.ConnectorKind != "fixture" {
				t.Fatalf("provenance mangled: %+v", e.Content.Text.External)
			}
		}
		return nil
	})
}

// Receiving is a fact, projecting is a hope: ingress bound toward a space
// this node has not finished joining stays durably pending — across a
// restart — and completes the moment the space arrives.
func TestConnectorIngressIsDurableBeforeProjection(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	setPersonalRelay(t, alice, addr)
	tid, err := alice.CreateSpace("✉ mailbox")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(tid, 1, 24, addr)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	term := openRuntime(t, dir, "term")
	setPersonalRelay(t, term, addr)
	// The operator binds the route BEFORE the join finishes — a legitimate
	// order on a fresh server.
	if _, err := term.ConnectorRoute("mail", tid); err != nil {
		t.Fatal(err)
	}
	if err := term.ConnectorIngest("mail", fixtureEnv("m-1", "ждёт пространство")); err != nil {
		t.Fatal(err)
	}
	if err := term.ConnectorIngest("mail", fixtureEnv("m-2", "тоже ждёт")); err != nil {
		t.Fatal(err)
	}
	st, _ := term.ConnectorStatus("mail")
	if st.Pending != 2 {
		t.Fatalf("ingress not journaled as pending: %+v", st)
	}

	// Restart with the join still not done: nothing may be lost.
	term.Close()
	term = openRuntime(t, dir, "term")
	defer term.Close()
	st, _ = term.ConnectorStatus("mail")
	if st.Pending != 2 {
		t.Fatalf("a restart lost pending ingress: %+v", st)
	}

	// The join completes; the projector — whose intent survived the
	// restart — delivers both, exactly once each.
	req, err := term.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, term, req, JoinReady)
	waitProjected(t, term, tid, "ждёт пространство")
	waitProjected(t, term, tid, "тоже ждёт")
	waitUntil(t, 30*time.Second, "alice never received the projections", func() bool {
		return countMsg(t, alice, tid, "ждёт пространство") >= 1 &&
			countMsg(t, alice, tid, "тоже ждёт") >= 1
	})
	time.Sleep(2 * time.Second)
	if n := countImported(t, term, tid, "ждёт пространство"); n != 1 {
		t.Fatalf("resume duplicated the projection: %d", n)
	}
}

// The temporal boundary, forward direction: a route change affects only
// ingress observed AFTER it. The connector itself survives the move — it
// was never bound to a space's identity. (The proposal's
// TestConnectorIsNotBoundToSpaceIdentity lives here.)
func TestRouteChangeAffectsOnlyNewIngress(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "term")
	defer rt.Close()
	spaceA, err := rt.CreateSpace("A")
	if err != nil {
		t.Fatal(err)
	}
	spaceB, err := rt.CreateSpace("B")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.ConnectorRoute("fix", spaceA); err != nil {
		t.Fatal(err)
	}
	if err := rt.ConnectorIngest("fix", fixtureEnv("mail-1", "первое письмо")); err != nil {
		t.Fatal(err)
	}
	waitProjected(t, rt, spaceA, "первое письмо")

	if _, err := rt.ConnectorRoute("fix", spaceB); err != nil {
		t.Fatal(err)
	}
	if err := rt.ConnectorIngest("fix", fixtureEnv("mail-2", "второе письмо")); err != nil {
		t.Fatal(err)
	}
	waitProjected(t, rt, spaceB, "второе письмо")
	time.Sleep(2 * time.Second)

	if n := countImported(t, rt, spaceB, "первое письмо"); n != 0 {
		t.Fatalf("history crossed the binding: mail-1 appeared in B %d times", n)
	}
	if n := countImported(t, rt, spaceA, "второе письмо"); n != 0 {
		t.Fatalf("new ingress leaked into the OLD space %d times", n)
	}
	if n := countImported(t, rt, spaceA, "первое письмо"); n != 1 {
		t.Fatalf("mail-1 in A: %d", n)
	}
}

// The temporal boundary, pending direction — THE important one: ingress
// whose projection never finished under binding N must NEVER surface in
// binding N+1's target. It is orphaned, on disk, with a name.
func TestPendingIngressDoesNotCrossRouteGeneration(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	setPersonalRelay(t, alice, addr)
	remote, err := alice.CreateSpace("远 remote")
	if err != nil {
		t.Fatal(err)
	}

	term := openRuntime(t, t.TempDir(), "term")
	defer term.Close()
	// Bound toward a space this terminal never joins: projection can never
	// finish — the honest stand-in for "Space A is gone".
	if _, err := term.ConnectorRoute("fix", remote); err != nil {
		t.Fatal(err)
	}
	if err := term.ConnectorIngest("fix", fixtureEnv("mail-1", "застрявшее письмо")); err != nil {
		t.Fatal(err)
	}
	st, _ := term.ConnectorStatus("fix")
	if st.Pending != 1 {
		t.Fatalf("premise: mail-1 must be pending, got %+v", st)
	}

	spaceB, err := term.CreateSpace("B")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := term.ConnectorRoute("fix", spaceB); err != nil {
		t.Fatal(err)
	}
	if err := term.ConnectorIngest("fix", fixtureEnv("mail-2", "новое письмо")); err != nil {
		t.Fatal(err)
	}
	waitProjected(t, term, spaceB, "новое письмо")
	time.Sleep(3 * time.Second)

	if n := countImported(t, term, spaceB, "застрявшее письмо"); n != 0 {
		t.Fatalf("pending ingress crossed the route generation: %d", n)
	}
	st, _ = term.ConnectorStatus("fix")
	if st.Orphaned != 1 {
		t.Fatalf("the stuck record was not orphaned visibly: %+v", st)
	}
	if st.Pending != 0 || st.Published != 1 {
		t.Fatalf("journal off after rebinding: %+v", st)
	}
}

// The crash window between Emit and settle, both halves, reconstructed on
// disk exactly as a killed process would leave it: one record whose emit
// LANDED (must settle, never re-emit) and one whose emit never happened
// (must emit exactly once on resume). The space's own log is the recovery
// authority (plan rev 2, R2).
func TestProjectionRecoveryReconcilesAgainstOwnLog(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "term")
	tid, err := rt.CreateSpace("✉ mailbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.ConnectorRoute("fix", tid); err != nil {
		t.Fatal(err)
	}

	// Two envelopes journaled, then hand-driven into the Emitting state —
	// bypassing the live projector so the "crash" is deterministic.
	cs, err := rt.connector("fix")
	if err != nil {
		t.Fatal(err)
	}
	gen, _, _ := cs.journal.Binding()
	stall := func(extID, text string) [32]byte {
		env := fixtureEnv(extID, text)
		key := connExternalKey("fix", env.ExternalID)
		env.ExternalRef = "ref-" + extID
		body := mustJSON(t, env)
		if err := rt.root.SaveSealed(connSealedName(key), body); err != nil {
			t.Fatal(err)
		}
		if _, _, err := cs.journal.Ingest(key, id.Hash{},
			sha256.Sum256([]byte(env.ExternalRef)), [32]byte{}, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := cs.journal.Update(gen, key, time.Now().Unix(), func(in *IngressRecord) bool {
			in.State = ConnEmitting
			return true
		}); err != nil {
			t.Fatal(err)
		}
		return key
	}
	_ = stall("landed", "письмо, чей эмит успел")
	_ = stall("lost", "письмо, чей эмит не случился")

	// The "landed" half: the event exists in the log, the journal does not
	// know. This is byte-for-byte the state after a crash between Emit and
	// the settle write.
	if err := rt.withSpace(tid, func(st *spaceState) error {
		gw, err := rt.ensureGatewayLocked()
		if err != nil {
			return err
		}
		_, err = gateway.Import(gw, st.space, "письмо, чей эмит успел",
			&schemas.ExternalOrigin{ConnectorKind: "fixture", ExternalRef: "ref-landed"},
			nil, uint64(time.Now().Unix()))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// The crash: down, and back up.
	rt.Close()
	rt = openRuntime(t, dir, "term")
	defer rt.Close()

	waitProjected(t, rt, tid, "письмо, чей эмит не случился")
	time.Sleep(2 * time.Second)
	if n := countImported(t, rt, tid, "письмо, чей эмит успел"); n != 1 {
		t.Fatalf("recovery re-emitted a landed event: %d copies", n)
	}
	if n := countImported(t, rt, tid, "письмо, чей эмит не случился"); n != 1 {
		t.Fatalf("recovery lost (or duplicated) an unlanded emit: %d copies", n)
	}
	st, _ := rt.ConnectorStatus("fix")
	if st.Pending != 0 || st.Published != 2 {
		t.Fatalf("journal did not settle honestly: %+v", st)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
