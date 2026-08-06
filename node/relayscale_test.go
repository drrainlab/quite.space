// RR — how many spaces a person may hold before the relay stops answering.
//
// FOUND WHILE DEBUGGING SOMETHING ELSE, and it is not a rate limit. The relay
// bounds the number of capabilities in ONE Collect at CollectMaxHints (64),
// and the private pull path sends two per space — the current bucket and the
// previous one — plus a reply box per public space. So a node crosses the
// ceiling at around thirty-two spaces and its request is REFUSED, every tick,
// for as long as it holds them.
//
// The public ingress path already splits its capabilities into chunks of 64
// with a comment saying why. The private one, which is the one every ordinary
// space uses, does not. One path learned the lesson and the other did not.
//
// WHAT IT LOOKS LIKE TO A PERSON: nothing. The node keeps running, the relay
// stays reachable, sync reports failures that read as a flaky connection, and
// messages simply stop arriving. Twenty-five spaces is not an unusual number.
package node

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/transports/lan"
	"github.com/drrainlab/quiet_places/transports/relay"
)

func TestManySpacesDoNotOverflowOneCollect(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := "127.0.0.1:" + itoa(port)

	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	if err := rt.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}

	// Thirty-five spaces: two capabilities each is seventy, past the relay's
	// ceiling of sixty-four. A person with thirty-five conversations is not
	// doing anything unusual.
	const spaces = 35
	for i := 0; i < spaces; i++ {
		if _, err := rt.CreateSpace(fmt.Sprintf("room %d", i)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := rt.PullFromRelay(addr); err != nil {
		t.Fatalf("pulling with %d spaces failed: %v\n\n"+
			"The relay bounds one Collect at %d capabilities and this path sends "+
			"two per space. Past the ceiling every tick is refused, for as long "+
			"as the person holds that many spaces — and from the interface it "+
			"looks like messages have simply stopped.",
			spaces, err, relay.DefaultLimits().CollectMaxHints)
	}
}

// The split must be by HINTS, not by spaces — a space is not one capability.
//
// A private space costs two (the current bucket and the previous one) and a
// public one costs more, so a chunker that counted spaces would send a
// perfectly legal-looking thirty-two and still cross the ceiling the moment
// the set was mixed. The invariants below are about the expanded list, which
// is the only thing the server ever sees.
func TestCollectChunksAreBoundedAndLoseNothing(t *testing.T) {
	const limit = maxCapsPerCollect

	// EVERY START POSITION, because the cursor is where a cycle resumes after
	// a failure and a rotation that covers only part of the list starves the
	// same spaces forever.
	for _, n := range []int{0, 1, limit - 1, limit, limit + 1, 3*limit + 7} {
		for start := 0; start <= 4; start++ {
			caps := make([][]byte, n)
			for i := range caps {
				caps[i] = []byte{byte(i), byte(i >> 8)}
			}

			seen := map[string]int{}
			var requests [][][]byte
			rt := &Runtime{}
			out, err := rt.collectInChunks(caps, limit, start,
				func(part [][]byte) ([][]byte, error) {
					requests = append(requests, part)
					return part, nil
				},
				func(items [][]byte) (int, error) {
					for _, c := range items {
						seen[string(c)]++
					}
					return len(items), nil
				})
			if err != nil {
				t.Fatalf("n=%d start=%d: %v", n, start, err)
			}

			for i, c := range requests {
				if len(c) > limit {
					t.Fatalf("n=%d: request %d carries %d hints, past the "+
						"server's %d", n, i, len(c), limit)
				}
				if len(c) == 0 {
					t.Fatalf("n=%d: request %d is empty — an empty request is "+
						"a round trip that asks nothing", n, i)
				}
			}
			if len(seen) != n {
				t.Fatalf("n=%d start=%d: %d of %d hints were served in one "+
					"cycle — a hint left behind is a mailbox nobody drains, "+
					"and it is the SAME one every time", n, start, len(seen), n)
			}
			for c, times := range seen {
				if times != 1 {
					t.Fatalf("n=%d: a hint was collected %d times; Collect is "+
						"destructive, so asking twice is asking for nothing",
						n, times)
					_ = c
				}
			}
			if out.Applied != n {
				t.Fatalf("n=%d: reported %d applied out of %d", n, out.Applied, n)
			}
		}
	}
}

// Fifty spaces, and the events must come back from the first, the middle and
// the last of them: a chunker that served the beginning of the list and quietly
// stopped would look identical to one that worked, right up until somebody
// noticed their oldest conversation had gone quiet.
func TestFiftySpacesAreAllServedByOnePull(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := "127.0.0.1:" + itoa(port)

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	for _, rt := range []*Runtime{alice, bob} {
		if err := rt.SetSettings(Settings{Relay: addr}); err != nil {
			t.Fatal(err)
		}
	}

	const spaces = 50
	tids := make([]id.TerminalID, 0, spaces)
	for i := 0; i < spaces; i++ {
		tid, err := alice.CreateSpace(fmt.Sprintf("room %d", i))
		if err != nil {
			t.Fatal(err)
		}
		tids = append(tids, tid)
	}

	// Bob joins the three that matter: the first, the middle and the last.
	watched := []int{0, spaces / 2, spaces - 1}
	for _, i := range watched {
		info, err := alice.MintPass(tids[i], 1, 24, addr)
		if err != nil {
			t.Fatal(err)
		}
		reqID, err := bob.JoinByPass(info.Link)
		if err != nil {
			t.Fatal(err)
		}
		waitJoin(t, bob, reqID, JoinReady)
	}

	// AND BOB HOLDS FIFTY OF HIS OWN, which is the point: the capability
	// ceiling is on the side that DRAINS. Fifty spaces at the sender cost the
	// receiver nothing — with three joined spaces bob sends six hints and this
	// test passed against a chunker that served only the first request.
	// Creating them locally is cheap; joining fifty over a relay is minutes.
	for i := len(watched); i < spaces; i++ {
		if _, err := bob.CreateSpace(fmt.Sprintf("bob room %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(bob.relayMailboxSpaces()); n < spaces {
		t.Fatalf("bob holds %d spaces, not %d — his pull would fit in one "+
			"request and this test would be about the sender", n, spaces)
	}

	for _, i := range watched {
		if _, err := alice.Say(tids[i], fmt.Sprintf("hello from room %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// Pushed per space, which is what the sending side does anyway: the
	// ceiling this test is about is on the DRAINING side.
	for _, i := range watched {
		if _, _, err := alice.PushToRelay(addr, tids[i]); err != nil {
			t.Fatal(err)
		}
	}

	// One pull, fifty spaces: it must not be refused, and it must serve every
	// chunk rather than the first one.
	deadline := time.Now().Add(30 * time.Second)
	served := map[int]bool{}
	for time.Now().Before(deadline) && len(served) < len(watched) {
		if _, err := bob.PullFromRelay(addr); err != nil {
			t.Fatalf("a pull across %d spaces failed: %v", spaces, err)
		}
		for _, i := range watched {
			if served[i] {
				continue
			}
			sp, ok := bob.spaceForTest(tids[i])
			if ok && sp.Log.Len() > 0 {
				served[i] = true
			}
		}
		time.Sleep(200 * time.Millisecond)
	}

	for _, i := range watched {
		if !served[i] {
			t.Fatalf("room %d of %d was never served — the first, the middle and "+
				"the last must all arrive, or a chunker that stops early looks "+
				"exactly like one that works", i, spaces)
		}
	}
}

// PARTIAL IS ITS OWN ANSWER, and the reason is that Collect is DESTRUCTIVE:
// the relay hands items over and forgets them. So a chunk that fails after
// earlier ones succeeded must not discard what they drained — those messages
// exist nowhere else — and it must not be reported as a complete pass either,
// because the spaces in the failed chunk were never served.
//
// Driven against a collect that fails on the second call, which no real relay
// will do on request. An earlier version of this test asked a rate-limited
// server to misbehave at the right moment and could pass three different ways,
// including by skipping — a test with a skip in its success path is a test
// nobody has to look at.
func TestAFailedChunkKeepsWhatTheEarlierOnesDrained(t *testing.T) {
	caps := make([][]byte, 5)
	for i := range caps {
		caps[i] = []byte{byte(i)}
	}

	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	calls := 0
	var order []string
	out, err := rt.collectInChunks(caps, 2, 0,
		func(chunk [][]byte) ([][]byte, error) {
			calls++
			order = append(order, fmt.Sprintf("collect%d", calls))
			if calls == 2 {
				return nil, fmt.Errorf("relay: rate limited")
			}
			return [][]byte{[]byte("drained")}, nil
		},
		func(items [][]byte) (int, error) {
			order = append(order, "commit")
			return len(items), nil
		})

	if err == nil {
		t.Fatal("a failed chunk must be reported: the spaces in it were never " +
			"served, and calling the cycle complete would advance state as " +
			"though they had been")
	}
	if out.Applied != 1 {
		t.Fatalf("Applied=%d, want the 1 the first chunk drained — Collect is "+
			"destructive, so losing it loses that message on both sides", out.Applied)
	}
	if calls != 2 {
		t.Fatalf("%d collects: the loop must stop asking after a failure rather "+
			"than spending the rest of the chunks against a server that has "+
			"already refused", calls)
	}

	// PER-CHUNK DURABILITY, asserted as an ORDER rather than as a count. A
	// collector that gathered every chunk and applied them at the end would
	// satisfy every other assertion here and still put all of them inside one
	// window in which a dying process loses the lot.
	want := []string{"collect1", "commit", "collect2"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("order was %v, want %v — each chunk must be made durable "+
			"before the next is requested", order, want)
	}
	if !out.partial() {
		t.Fatalf("a cycle that served %d of %d chunks is partial", out.Served, out.Chunks)
	}
}

// After a failure the next cycle continues from the chunk that failed, not
// from the beginning.
//
// Otherwise a chunk that keeps being refused — which is what a rate limit
// produces once it starts biting — re-serves the same first spaces forever and
// the tail of a long list is never drained at all. The person sees their
// oldest conversations go quiet and nothing anywhere says why.
func TestAFailedCycleResumesWhereItStoppedRatherThanAtTheBeginning(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	caps := make([][]byte, 6) // three chunks of two
	for i := range caps {
		caps[i] = []byte{byte(i)}
	}
	noop := func(items [][]byte) (int, error) { return 0, nil }

	// The second chunk refuses.
	var asked []int
	out, err := rt.collectInChunks(caps, 2, 0,
		func(chunk [][]byte) ([][]byte, error) {
			asked = append(asked, int(chunk[0][0]))
			if chunk[0][0] == 2 {
				return nil, fmt.Errorf("relay: rate limited")
			}
			return nil, nil
		}, noop)
	if err == nil {
		t.Fatal("the second chunk was supposed to fail")
	}
	if out.NextChunk != 1 {
		t.Fatalf("NextChunk=%d, want the chunk that failed (1)", out.NextChunk)
	}

	// The next cycle starts there and comes back round to the beginning.
	asked = nil
	out2, err := rt.collectInChunks(caps, 2, out.NextChunk,
		func(chunk [][]byte) ([][]byte, error) {
			asked = append(asked, int(chunk[0][0]))
			return nil, nil
		}, noop)
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) == 0 || asked[0] != 2 {
		t.Fatalf("the next cycle asked %v first, want the chunk that was "+
			"refused — starting at the beginning again is how the tail starves", asked)
	}
	if out2.Served != out2.Chunks {
		t.Fatalf("a full pass served %d of %d chunks", out2.Served, out2.Chunks)
	}
	// And it wrapped: every chunk was asked exactly once.
	if len(asked) != 3 {
		t.Fatalf("asked %v — a cycle must come round to the ones it skipped", asked)
	}
}

// A CURSOR IS MEANINGLESS OVER A RANDOM ORDER, and the list it indexes came
// from a Go map — whose iteration order is deliberately randomised.
//
// So "resume at chunk two" pointed at a different set of spaces every cycle.
// The tail could still starve, and the starvation would MOVE, which is worse
// than a fixed one: it reads as flakiness rather than as a bug.
func TestTheSpacesADrainWalksComeBackInAStableOrder(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	for i := 0; i < 20; i++ {
		if _, err := rt.CreateSpace(fmt.Sprintf("room %d", i)); err != nil {
			t.Fatal(err)
		}
	}

	first := rt.relayMailboxSpaces()
	if len(first) < 20 {
		t.Fatalf("only %d spaces came back", len(first))
	}
	// Several times, because a random order that happens to repeat once proves
	// nothing.
	for round := 0; round < 8; round++ {
		again := rt.relayMailboxSpaces()
		if len(again) != len(first) {
			t.Fatalf("round %d returned %d spaces, want %d", round, len(again), len(first))
		}
		for i := range first {
			if again[i] != first[i] {
				t.Fatalf("round %d differs at position %d — a chunk cursor over "+
					"this list would point at different spaces every cycle, and "+
					"the starvation it causes would move around", round, i)
			}
		}
	}
}

// A collect that succeeded and a commit that failed are not the same as a
// collect that was refused: they call for opposite responses — wait for the
// relay, or stop asking it for anything until local storage works again — and
// the cursor must not step past the chunk in either case.
func TestACommitFailureIsNamedAndDoesNotAdvanceTheCursor(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	caps := make([][]byte, 6) // three chunks of two
	for i := range caps {
		caps[i] = []byte{byte(i)}
	}

	out, err := rt.collectInChunks(caps, 2, 0,
		func(chunk [][]byte) ([][]byte, error) {
			return [][]byte{[]byte("drained")}, nil
		},
		func(items [][]byte) (int, error) {
			return 0, fmt.Errorf("storage: disk full")
		})

	if err == nil {
		t.Fatal("a commit failure must be reported")
	}
	if out.Stop != stopCommitFailed {
		t.Fatalf("stop reason %q, want %q — waiting for a retry-after when the "+
			"real problem is local storage is the wrong action entirely",
			out.Stop, stopCommitFailed)
	}
	if out.NextChunk != 0 {
		t.Fatalf("NextChunk=%d after a failed commit on the first chunk — "+
			"stepping past it would start serving the tail while storage is "+
			"broken and hide the failure behind apparent progress", out.NextChunk)
	}
	if out.Served != 0 {
		t.Fatalf("Served=%d: a chunk whose commit failed was not served", out.Served)
	}
}

// A REFUSAL IS NOT A DEAD SOCKET, and the pool must not treat it as one.
//
// Discarding a healthy connection because the relay asked for less traffic is
// the worst possible response: the next attempt costs a fresh TLS handshake,
// which is more load on the relay that had just said it was busy. And a
// request the relay considers malformed will fail identically however many
// connections are opened for it.
func TestARelayRefusalDoesNotKillAHealthyConnection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
	}{
		{"a rate limit is a schedule", relay.ReasonRateLimited},
		{"a hint ceiling is a request-shape problem", relay.ReasonTooManyHints},
		{"a quota is about this item, not the socket", relay.ReasonQuotaExceeded},
		{"an unknown reason is not guessed at", "something a newer relay says"},
	} {
		if isConnFatal(relay.ErrRelay{Reason: tc.reason}) {
			t.Errorf("%s: %q was treated as a dead connection", tc.name, tc.reason)
		}
	}

	// And the things that ARE the connection dying still are.
	if !isConnFatal(io.EOF) {
		t.Error("EOF is the connection ending")
	}
	if !isConnFatal(lan.ErrConnClosed) {
		t.Error("a closed transport connection is fatal")
	}
}

// AR/RR — the relay says when to come back, and the node does not ask sooner.
//
// A rate limit refuses because it wants less traffic, so another request is
// not a retry: it is the thing being asked to stop, and it keeps the window
// from refilling. That is what a cooldown loop is from the outside — two sides
// spending a minute proving the same point to each other — and it is why one
// never recovers on its own.
func TestANodeWaitsAsLongAsTheRelayAsked(t *testing.T) {
	limits := relay.DefaultLimits()
	limits.CollectRatePerMin = 1
	srv, port, err := relay.StartServer("127.0.0.1:0", limits)
	if err != nil {
		t.Fatal(err)
	}
	// Closed twice on purpose below, so once: the deferred call is only a
	// safety net for a t.Fatal on the way there.
	closeRelay := sync.OnceFunc(func() { srv.Close() })
	defer closeRelay()
	addr := "127.0.0.1:" + itoa(port)

	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	if err := rt.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.CreateSpace("room"); err != nil {
		t.Fatal(err)
	}

	// Spend the budget until the relay refuses.
	var refusal error
	for i := 0; i < 6 && refusal == nil; i++ {
		_, refusal = rt.PullFromRelay(addr)
	}
	if refusal == nil {
		t.Skip("the relay never refused; nothing to wait out")
	}

	var re relay.ErrRelay
	if !errors.As(refusal, &re) || !re.Throttled() {
		t.Fatalf("expected a throttle, got %v", refusal)
	}
	if re.RetryAfter <= 0 {
		t.Fatal("the relay refused without saying how long to wait")
	}

	left, throttled := rt.relayThrottled(addr)
	if !throttled {
		t.Fatal("the node did not remember the deadline it was given — the next " +
			"tick would ask again immediately, which is exactly what was refused")
	}
	if left <= 0 || left > time.Minute {
		t.Fatalf("remaining wait %v is not inside the relay's own window", left)
	}

	// AND THE PROOF THAT NOTHING WENT OUT: the relay is closed. If the node
	// asks anyway it meets a dead socket and says so; if it honours the
	// deadline it answers "throttled" without touching the network.
	//
	// A timing check was tried first and did not discriminate — a local relay
	// refuses in under a millisecond, so "it was fast" is true whether or not
	// the request happened.
	closeRelay()
	_, err = rt.PullFromRelay(addr)
	if err == nil {
		t.Fatal("a pull during the wait should not have gone out")
	}
	var re2 relay.ErrRelay
	if !errors.As(err, &re2) || !re2.Throttled() {
		t.Fatalf("a pull during the wait reached the network instead of "+
			"answering from the deadline: %v", err)
	}
}

// Two joined spaces, messages alternating between them, and both must arrive.
//
// WRITTEN BECAUSE A PHONE SAID OTHERWISE. In the visual gate the second space
// joined received its first message and then nothing, while the first kept
// working for minutes — which would be a serious defect (a conversation that
// goes quiet with no error anywhere) or a harness artifact, and guessing
// between those is how a real one gets dismissed.
func TestBothJoinedSpacesKeepReceiving(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := "127.0.0.1:" + itoa(port)

	sender := openRuntime(t, t.TempDir(), "alice")
	defer sender.Close()
	phone := openRuntime(t, t.TempDir(), "bob")
	defer phone.Close()
	for _, rt := range []*Runtime{sender, phone} {
		if err := rt.SetSettings(Settings{Relay: addr}); err != nil {
			t.Fatal(err)
		}
	}

	var rooms []id.TerminalID
	for _, title := range []string{"room A", "room B"} {
		tid, err := sender.CreateSpace(title)
		if err != nil {
			t.Fatal(err)
		}
		info, err := sender.MintPass(tid, 1, 24, addr)
		if err != nil {
			t.Fatal(err)
		}
		reqID, err := phone.JoinByPass(info.Link)
		if err != nil {
			t.Fatal(err)
		}
		waitJoin(t, phone, reqID, JoinReady)
		rooms = append(rooms, tid)
	}

	// Alternating, the way the gate does it: warm both, then a message into
	// one, then a message into the other.
	deliver := func(tid id.TerminalID, text string) int {
		if _, err := sender.Say(tid, text, SayOptions{}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 60; i++ {
			_, _, _ = sender.PushToRelay(addr, tid)
			_, _ = phone.PullFromRelay(addr)
			if sp, ok := phone.spaceForTest(tid); ok {
				n := 0
				_ = sp.Log.Replay(func(a eventlog.Applied) error {
					if a.Env != nil && a.Env.Schema == schemas.MessageText {
						n++
					}
					return nil
				})
				if n > 0 {
					return n
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		return 0
	}

	for round, text := range []string{"warm A", "warm B", "one", "two", "three"} {
		tid := rooms[round%2]
		if got := deliver(tid, text); got == 0 {
			t.Fatalf("round %d: %q never arrived in room %d — a conversation "+
				"that goes quiet with nothing reporting it", round, text, round%2)
		}
	}
}
