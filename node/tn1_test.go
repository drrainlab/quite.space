package node

import (
	"runtime"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/routing"
	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports"
	"github.com/drrainlab/quiet_places/transports/loopback"
)

type testLink struct{ transports.Endpoint }

func (testLink) Closed() (bool, error) { return false, nil }

// appliedCh taps a space's sync-apply hook so a test can wait for the event
// it actually cares about instead of polling a clock. The engine is
// caller-serialized under r.mu, and the tap is installed under the same
// lock, so this races with nothing.
// appliedCh delivers the id of every event that lands in a space, from
// whichever door it came through.
//
// IT LISTENS ON THE SPACE'S OnAbsorb, NOT THE SYNC ENGINE'S OnApplied. An
// earlier form hooked the engine, and under load a test went red with every
// room holding exactly one message and the channel silent: a frame that
// arrives while the receiver holds it in custody is released by judgeFrame
// straight into Log.Ingest and AttachSyncApply — correct, and not through
// the engine's hook. OnAbsorb fires for every absorbed event, local or
// synced, by either path; it is the one door everything passes.
func appliedCh(t *testing.T, rt *Runtime, tid id.TerminalID) <-chan id.EventID {
	t.Helper()
	ch := make(chan id.EventID, 32)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	st, ok := rt.spaces[tid]
	if !ok {
		t.Fatalf("space %s not attached", tid.Hex()[:6])
	}
	prev := st.space.OnAbsorb
	st.space.OnAbsorb = func(a eventlog.Applied) {
		if prev != nil {
			prev(a)
		}
		select {
		case ch <- a.ID:
		default: // a slow reader must never stall the pump
		}
	}
	return ch
}

// TN-1 seam: a filtered link syncs ONLY the allowed spaces; the peer never
// sees the filtered-out space over that link.
func TestAdoptLinkFilteredScopesSpaces(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	tidA, err := alice.CreateSpace("Allowed")
	if err != nil {
		t.Fatal(err)
	}
	tidB, err := alice.CreateSpace("Filtered")
	if err != nil {
		t.Fatal(err)
	}
	for _, tid := range []id.TerminalID{tidA, tidB} {
		if _, err := alice.Say(tid, "hello from "+tid.Hex()[:6], SayOptions{}); err != nil {
			t.Fatal(err)
		}
		invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bob.JoinInvite(invite); err != nil {
			t.Fatal(err)
		}
	}

	allowed := appliedCh(t, bob, tidA)
	filtered := appliedCh(t, bob, tidB)
	// Joining already put each space's genesis in Bob's log. The leak test
	// is about GROWTH over the scoped link, not about emptiness.
	bBefore, _ := bob.spaceForTest(tidB)
	baseB := bBefore.Log.Len()

	pair := loopback.NewPair(loopback.Faults{Seed: 5})
	allowOnlyA := func(m routing.FrameMeta) bool { return m.Destination == tidA }
	alice.adoptLinkFiltered(testLink{pair.A}, 30*time.Millisecond, 200*time.Millisecond,
		"test", allowOnlyA)
	bob.adoptLink(testLink{pair.B}, 30*time.Millisecond, 200*time.Millisecond, "test")

	dump := func() string {
		bA, _ := bob.spaceForTest(tidA)
		bB, _ := bob.spaceForTest(tidB)
		aA, _ := alice.spaceForTest(tidA)
		aB, _ := alice.spaceForTest(tidB)
		return "alice log A=" + itoa(aA.Log.Len()) + " B=" + itoa(aB.Log.Len()) +
			" · bob log A=" + itoa(bA.Log.Len()) + " B=" + itoa(bB.Log.Len()) +
			" · bob msgs A=" + itoa(len(bA.State.Messages())) +
			" B=" + itoa(len(bB.State.Messages()))
	}
	// One generous ceiling for the whole exchange. It is a backstop for a
	// hung link, not the thing being measured — every wait below ends on an
	// event, so a healthy run finishes in milliseconds regardless.
	deadline := time.After(30 * time.Second)
	await := func(what string) {
		for {
			select {
			case <-allowed:
				return
			case eid := <-filtered:
				t.Fatalf("the filtered space leaked over the scoped link "+
					"(event %s while waiting for %s): %s", eid.Hex()[:8], what, dump())
			case <-deadline:
				buf := make([]byte, 1<<20)
				n := runtime.Stack(buf, true)
				t.Fatalf("timeout waiting for %s: %s\n=== GOROUTINES ===\n%s",
					what, dump(), buf[:n])
			}
		}
	}

	// The allowed space converges.
	await("the allowed space to sync")
	bA, _ := bob.spaceForTest(tidA)
	if len(bA.State.Messages()) == 0 {
		t.Fatalf("applied an event but no message surfaced: %s", dump())
	}

	// A SECOND event over the same link proves the filtered space had a full
	// pump round-trip to leak in and did not — a far stronger argument than
	// sleeping for a second and hoping that was long enough.
	if _, err := alice.Say(tidA, "still only this space", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	await("a second event over the same link")

	bB, _ := bob.spaceForTest(tidB)
	if n := len(bB.State.Messages()); n != 0 {
		t.Fatalf("filtered space leaked %d messages over the scoped link", n)
	}
	if grew := bB.Log.Len() - baseB; grew != 0 {
		t.Fatalf("filtered space gained %d frames over the scoped link "+
			"(had %d from the invite): %s", grew, baseB, dump())
	}
}

// Two spaces over ONE link must both converge — the bug this pins is not a
// filter question but a demultiplexing one.
//
// Each space has its own sync engine, and every engine used to call
// Pump(ep), which polls. Poll drains the whole queue. So the first engine
// in the list swallowed every packet on the link, discarded the ones
// addressed to its siblings as "not my terminal", and those spaces never
// synced at all. The order came from a map, so it presented as an
// occasional slow test rather than as a space that silently stopped
// working — which is what it was.
func TestOneLinkCarriesEveryJoinedSpace(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	const spaces = 4
	tids := make([]id.TerminalID, 0, spaces)
	for i := range spaces {
		tid, err := alice.CreateSpace("Room " + itoa(i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := alice.Say(tid, "hello from room "+itoa(i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
		invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bob.JoinInvite(invite); err != nil {
			t.Fatal(err)
		}
		tids = append(tids, tid)
	}

	waits := make([]<-chan id.EventID, spaces)
	for i, tid := range tids {
		waits[i] = appliedCh(t, bob, tid)
	}

	pair := loopback.NewPair(loopback.Faults{Seed: 9})
	alice.adoptLink(testLink{pair.A}, 30*time.Millisecond, 200*time.Millisecond, "test")
	bob.adoptLink(testLink{pair.B}, 30*time.Millisecond, 200*time.Millisecond, "test")

	deadline := time.After(30 * time.Second)
	for i, ch := range waits {
		select {
		case <-ch:
		case <-deadline:
			var stuck []string
			for j, tid := range tids {
				sp, _ := bob.spaceForTest(tid)
				stuck = append(stuck, "room"+itoa(j)+"="+itoa(len(sp.State.Messages())))
			}
			t.Fatalf("room %d never synced over the shared link; bob has %v "+
				"— one space is eating the others' packets", i, stuck)
		}
	}
	for i, tid := range tids {
		sp, _ := bob.spaceForTest(tid)
		if len(sp.State.Messages()) == 0 {
			t.Fatalf("room %d applied an event but shows no message", i)
		}
	}
}

// A packet for a terminal this link does not serve must be a no-op, not a
// blockage. The demux drops it; the valid packets behind it in the same
// batch still reach their engines.
func TestUnknownTerminalDoesNotBlockTheBatch(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	tid, err := alice.CreateSpace("Served")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "after the noise", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	applied := appliedCh(t, bob, tid)

	pair := loopback.NewPair(loopback.Faults{Seed: 11})
	// A stranger's summary lands on the wire first, every round: a terminal
	// neither side serves, which is the ordinary case on a shared segment.
	var stranger id.TerminalID
	stranger[0] = 0xFE
	noise := kernelsync.EncodeSummaryMessage(stranger, nil)
	frags, err := kernelsync.FragmentStream(kernelsync.NextStreamID(), noise, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range frags {
		if err := pair.A.Send(f); err != nil {
			t.Fatal(err)
		}
	}

	alice.adoptLink(testLink{pair.A}, 30*time.Millisecond, 200*time.Millisecond, "test")
	bob.adoptLink(testLink{pair.B}, 30*time.Millisecond, 200*time.Millisecond, "test")

	select {
	case <-applied:
	case <-time.After(30 * time.Second):
		t.Fatal("a packet for an unserved terminal stopped the batch behind it")
	}
	if msgCount(bob, tid) == 0 {
		t.Fatal("applied an event but no message surfaced")
	}
}

// Four spaces, one link, and an MTU small enough that every message is
// fragmented — so all four streams are in flight at once and interleaved on
// the wire. Reassembly must keep them apart and all four must converge.
func TestInterleavedFragmentedSpacesAllConverge(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	const spaces = 4
	// Long enough that one message cannot fit in a single fragment.
	body := ""
	for range 40 {
		body += "the same long sentence repeated to force fragmentation. "
	}

	tids := make([]id.TerminalID, 0, spaces)
	for i := range spaces {
		tid, err := alice.CreateSpace("Room " + itoa(i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := alice.Say(tid, "room "+itoa(i)+": "+body, SayOptions{}); err != nil {
			t.Fatal(err)
		}
		invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bob.JoinInvite(invite); err != nil {
			t.Fatal(err)
		}
		tids = append(tids, tid)
	}
	waits := make([]<-chan id.EventID, spaces)
	for i, tid := range tids {
		waits[i] = appliedCh(t, bob, tid)
	}

	// A real MTU with reordering: fragments from four streams arrive mixed.
	pair := loopback.NewPair(loopback.Faults{Seed: 13, MTU: 220, Reorder: true})
	alice.adoptLink(testLink{pair.A}, 20*time.Millisecond, 150*time.Millisecond, "test")
	bob.adoptLink(testLink{pair.B}, 20*time.Millisecond, 150*time.Millisecond, "test")

	deadline := time.After(40 * time.Second)
	for i, ch := range waits {
		select {
		case <-ch:
		case <-deadline:
			var have []string
			for j, tid := range tids {
				have = append(have, "room"+itoa(j)+"="+itoa(msgCount(bob, tid)))
			}
			t.Fatalf("room %d never converged with four fragmented streams "+
				"interleaved; bob has %v", i, have)
		}
	}
	for i, tid := range tids {
		if n := msgCount(bob, tid); n != 1 {
			t.Fatalf("room %d has %d messages, want exactly 1 — fragments from "+
				"different streams were spliced or duplicated", i, n)
		}
	}
}
