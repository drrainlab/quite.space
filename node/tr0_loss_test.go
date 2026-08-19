// TR-0f — the loss/recovery matrix (plan rev 4, §8). Four ways a space
// stops being writable, one promise each time: the journal holds, nothing
// is lost, nothing is doubled, and recovery — where recovery is honest —
// produces exactly one instance. States are DISTINGUISHED, never glued:
// unavailable is not removed, removed is not revoked (ADR-020's lesson,
// one layer up). The fourth state (public not-discoverable) does not apply
// to a private-space connector and is recorded as such in the plan.
package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// (a) UNAVAILABLE: the relay dies. Projection is a LOCAL act — mail keeps
// landing in the terminal's replica while the network is gone — and the
// members' copies converge exactly once when the relay returns, because
// that half is RT-0's promise and the connector merely inherits it.
func TestRelayOutageDoesNotDropOrDuplicateIngress(t *testing.T) {
	srv, addr := startRelay(t)
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

	term := openRuntime(t, t.TempDir(), "term")
	defer term.Close()
	setPersonalRelay(t, term, addr)
	req, err := term.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, term, req, JoinReady)
	if _, err := term.ConnectorRoute("fix", tid); err != nil {
		t.Fatal(err)
	}
	// THE TERMINAL MUST HAVE HEARD THE OWNER BEFORE THE OUTAGE, and JoinReady
	// does not say that: it says the pass was accepted. Alice's own frames
	// reach the terminal on her next sync tick, and until they do the
	// terminal's recipient set — authors it has seen, plus Members(), which
	// is controller-only and empty for a joiner — does not contain her. Cut
	// the relay before that tick and the letter has nobody to go to; at the
	// shipped 2s beat alice always won that race, at a faster one she did
	// not, and the test measured the start-up race instead of the outage.
	waitUntil(t, 20*time.Second, "the terminal never heard alice", func() bool {
		heard := false
		_ = term.withSpace(tid, func(st *spaceState) error {
			_ = st.space.Log.Replay(func(a eventlog.Applied) error {
				if a.Env.Device == alice.Device.ID {
					heard = true
				}
				return nil
			})
			return nil
		})
		return heard
	})

	// The outage begins; a letter arrives anyway.
	srv.Close()
	if err := term.ConnectorIngest("fix", fixtureEnv("m-1", "письмо в разрыв")); err != nil {
		t.Fatal(err)
	}
	waitProjected(t, term, tid, "письмо в разрыв")
	if n := countMsg(t, alice, tid, "письмо в разрыв"); n != 0 {
		t.Fatal("delivered through a dead relay?")
	}

	// The relay returns at the same address; RT-0 re-offers; alice
	// receives exactly once.
	srv2, _, err := relayserver.StartServer(addr, relayserver.DefaultLimits())
	if err != nil {
		t.Skipf("could not rebind %s: %v", addr, err)
	}
	defer srv2.Close()
	waitUntil(t, 60*time.Second, "the outage swallowed the letter", func() bool {
		return countMsg(t, alice, tid, "письмо в разрыв") >= 1
	})
	time.Sleep(3 * time.Second)
	if n := countMsg(t, alice, tid, "письмо в разрыв"); n != 1 {
		t.Fatalf("recovery duplicated the letter: %d", n)
	}
}

// (b) REMOVED LOCALLY: the target space is deleted on the terminal. New
// ingress parks pending — durable, restart-proof — and the recovery is an
// OPERATOR act with rev-3 semantics: a new binding carries only what it
// observes; the parked record is orphaned, on disk, with a name.
func TestLocalSpaceDeletionParksTheConnector(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "term")
	tid, err := rt.CreateSpace("A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.ConnectorRoute("fix", tid); err != nil {
		t.Fatal(err)
	}
	if err := rt.ConnectorIngest("fix", fixtureEnv("m-1", "до удаления")); err != nil {
		t.Fatal(err)
	}
	waitProjected(t, rt, tid, "до удаления")

	if err := rt.DeleteSpace(tid); err != nil {
		t.Fatal(err)
	}
	if err := rt.ConnectorIngest("fix", fixtureEnv("m-2", "после удаления")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 10*time.Second, "ingress after deletion was not parked", func() bool {
		st, _ := rt.ConnectorStatus("fix")
		return st.Pending == 1
	})

	// Restart: the parked record survives; the deleted space stays deleted.
	rt.Close()
	rt = openRuntime(t, dir, "term")
	defer rt.Close()
	st, _ := rt.ConnectorStatus("fix")
	if st.Pending != 1 {
		t.Fatalf("restart lost the parked ingress: %+v", st)
	}

	// The operator recovers deliberately: a NEW space, a NEW binding.
	spaceB, err := rt.CreateSpace("B")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.ConnectorRoute("fix", spaceB); err != nil {
		t.Fatal(err)
	}
	if err := rt.ConnectorIngest("fix", fixtureEnv("m-3", "новая жизнь")); err != nil {
		t.Fatal(err)
	}
	waitProjected(t, rt, spaceB, "новая жизнь")
	time.Sleep(2 * time.Second)
	if n := countImported(t, rt, spaceB, "после удаления"); n != 0 {
		t.Fatal("a parked record crossed the binding into B")
	}
	st, _ = rt.ConnectorStatus("fix")
	if st.Orphaned != 1 || st.Pending != 0 {
		t.Fatalf("the parked record was not orphaned visibly: %+v", st)
	}
}

// (c) REVOKED: the terminal is removed from the space and the epoch
// rotates. The projector's write fails closed (no current epoch key), the
// journal parks the letter — pending, honestly, across a restart — and
// nothing is ever guessed at.
func TestRevokedMembershipHoldsProjection(t *testing.T) {
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
	defer term.Close()
	setPersonalRelay(t, term, addr)
	req, err := term.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, term, req, JoinReady)
	if _, err := term.ConnectorRoute("fix", tid); err != nil {
		t.Fatal(err)
	}
	if err := term.ConnectorIngest("fix", fixtureEnv("m-1", "пока член")); err != nil {
		t.Fatal(err)
	}
	waitProjected(t, term, tid, "пока член")
	waitUntil(t, 30*time.Second, "alice never saw the pre-revocation letter", func() bool {
		return countMsg(t, alice, tid, "пока член") >= 1
	})

	// Alice removes the terminal's DEVICES and rotates. The gateway signs
	// with its own device, but epoch access arrived through the join — both
	// of the terminal's devices go.
	if err := alice.withSpace(tid, func(st *spaceState) error {
		for dev := range st.space.Members() {
			if dev != alice.Device.ID {
				st.space.RemoveMember(dev)
			}
		}
		_, err := alice.Self.RotateEpoch(st.space)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "после ротации", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	// The revoked terminal discovers its state by trying: the new epoch
	// event arrives, the key does not.
	waitUntil(t, 30*time.Second, "the terminal never absorbed the rotation", func() bool {
		got := false
		_ = term.withSpace(tid, func(st *spaceState) error {
			got = st.space.Undecryptable > 0
			return nil
		})
		return got
	})

	if err := term.ConnectorIngest("fix", fixtureEnv("m-2", "письмо изгнаннику")); err != nil {
		t.Fatal(err)
	}
	// Parked, not projected, not lost — and honest about why on write.
	time.Sleep(4 * time.Second)
	st, _ := term.ConnectorStatus("fix")
	if st.Pending != 1 {
		t.Fatalf("revocation did not park the letter: %+v", st)
	}
	if n := countImported(t, term, tid, "письмо изгнаннику"); n != 0 {
		t.Fatal("a revoked member projected into the space")
	}

	// Restart changes nothing: still parked, still exactly one record.
	term.Close()
	term2 := openRuntime(t, dir, "term")
	defer term2.Close()
	time.Sleep(3 * time.Second)
	st, _ = term2.ConnectorStatus("fix")
	if st.Pending != 1 {
		t.Fatalf("restart lost the parked letter: %+v", st)
	}
}
