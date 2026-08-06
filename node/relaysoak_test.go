package node

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// RR — the soaks. Fifty spaces, pulled at a person's cadence, for long enough
// that a slow leak has room to show.
//
// WHY A SOAK AND NOT ANOTHER UNIT TEST. The chunking bug was not a crash. The
// node kept running, the relay stayed reachable, and messages stopped arriving
// — and every single-shot test passed, because one pull across fifty spaces
// does work. What does not work is the FIFTIETH minute: a cursor that rotates
// one chunk short, a wait that is re-armed a little early every cycle, a
// backlog that grows by one round every ten. None of those are visible in a
// second, and all of them look exactly like a flaky connection to the person
// holding the phone.
//
// THEY ARE GATED, AND COMPILED ANYWAY. `QUIET_RELAY_SOAK=1` runs them; without
// it they skip in microseconds. The gate is an environment variable rather
// than a build tag on purpose — a tagged file stops being compiled, stops
// being vetted, and quietly rots until the day somebody needs it. Skipping is
// cheap; bit rot is not.
//
//	QUIET_RELAY_SOAK=1 go test ./node/ -run Soak -v -timeout 30m
//	QUIET_RELAY_SOAK=1 QUIET_RELAY_SOAK_SECONDS=90 go test ./node/ -run Soak -v
const (
	soakSpaces  = 50
	soakCadence = 2 * time.Second
)

func soakDuration(t *testing.T) time.Duration {
	t.Helper()
	if os.Getenv("QUIET_RELAY_SOAK") != "1" {
		t.Skip("set QUIET_RELAY_SOAK=1 to run the soaks (they take minutes)")
	}
	if s := os.Getenv("QUIET_RELAY_SOAK_SECONDS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			t.Fatalf("QUIET_RELAY_SOAK_SECONDS=%q is not a number of seconds", s)
		}
		return time.Duration(n) * time.Second
	}
	return 10 * time.Minute
}

// A soak fixture: fifty spaces at one node, three of them — the first, the
// middle and the last — joined by another. Those three are the ones that
// answer the only question a chunker can fail: not "did anything arrive" but
// "did the far end of the list arrive too".
type soakFixture struct {
	addr    string
	alice   *Runtime
	bob     *Runtime
	tids    []id.TerminalID
	watched []id.TerminalID // first, middle and last IN THE ORDER THAT IS CHUNKED
}

func newSoakFixture(t *testing.T, limits relay.ServerLimits) *soakFixture {
	t.Helper()
	srv, port, err := relay.StartServer("127.0.0.1:0", limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	addr := "127.0.0.1:" + itoa(port)

	alice := openRuntime(t, t.TempDir(), "alice")
	t.Cleanup(func() { alice.Close() })
	bob := openRuntime(t, t.TempDir(), "bob")
	t.Cleanup(func() { bob.Close() })
	for _, rt := range []*Runtime{alice, bob} {
		if err := rt.SetSettings(Settings{Relay: addr}); err != nil {
			t.Fatal(err)
		}
	}

	tids := make([]id.TerminalID, 0, soakSpaces)
	for i := 0; i < soakSpaces; i++ {
		tid, err := alice.CreateSpace(fmt.Sprintf("room %d", i))
		if err != nil {
			t.Fatal(err)
		}
		tids = append(tids, tid)
	}

	// BOB JOINS ALL FIFTY, and that is the whole fixture rather than a detail.
	// The first version had him join only the three spaces the messages go to
	// and it tested nothing: the capability ceiling is on the side that
	// DRAINS, and a node with three spaces sends six hints. Fifty spaces at
	// the sender cost the receiver nothing. Both red-proofs passed against a
	// deliberately broken chunker before this was found — the soak was
	// measuring a node that never had a second chunk to serve.
	for i := range tids {
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
	// And the fixture says so out loud: if a pull fits in one chunk, every
	// assertion below is about something else.
	order := bob.relayMailboxSpaces()
	if len(order) < soakSpaces {
		t.Fatalf("bob holds %d of %d spaces — the pull would not be chunked",
			len(order), soakSpaces)
	}

	// THE WATCHED SPACES COME FROM THAT ORDER, not from the order they were
	// created in. Capabilities are sorted by terminal id, so creation index 49
	// sits wherever its id falls — and a "last space" that happens to land in
	// the first chunk makes the far end of the list a coin toss. Both
	// red-proofs passed against a chunker pinned to the head of the list
	// before this was fixed.
	watched := []id.TerminalID{order[0], order[len(order)/2], order[len(order)-1]}

	return &soakFixture{addr: addr, alice: alice, bob: bob, tids: tids, watched: watched}
}

// How many of the rounds' messages bob actually holds.
//
// MESSAGES, NOT LOG LENGTH. The first version subtracted a baseline taken
// right after joining and reported more messages than were ever sent: the
// membership events of the LAST space joined were still replicating when the
// baseline was read, so the space that mattered most — the far end of the
// list — was the one measured wrong. A count that only ever counts what the
// test itself sent cannot drift like that.
func (f *soakFixture) delivered(tid id.TerminalID) int {
	sp, ok := f.bob.spaceForTest(tid)
	if !ok {
		return 0
	}
	n := 0
	_ = sp.Log.Replay(func(a eventlog.Applied) error {
		if a.Env != nil && a.Env.Schema == schemas.MessageText {
			n++
		}
		return nil
	})
	return n
}

// One round: a line into each watched space, pushed by the sender.
func (f *soakFixture) send(t *testing.T, round int) {
	t.Helper()
	for _, tid := range f.watched {
		if _, err := f.alice.Say(tid, fmt.Sprintf("round %d", round), SayOptions{}); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if _, _, err := f.alice.PushToRelay(f.addr, tid); err != nil {
			t.Fatalf("round %d: push: %v", round, err)
		}
	}
}

// Pull until everything sent has arrived or the deadline passes. Returns the
// worst backlog seen, in rounds.
func (f *soakFixture) drain(t *testing.T, sent int, deadline time.Time) int {
	t.Helper()
	worst := 0
	for {
		behind := 0
		for _, tid := range f.watched {
			if b := sent - f.delivered(tid); b > behind {
				behind = b
			}
		}
		if behind > worst {
			worst = behind
		}
		if behind <= 0 || !time.Now().Before(deadline) {
			return worst
		}
		if _, err := f.bob.PullFromRelay(f.addr); err != nil {
			var re relay.ErrRelay
			if errors.As(err, &re) && re.Throttled() {
				// Waiting is the correct behaviour, not a failure. The
				// clean soak asserts separately that it never got here.
				time.Sleep(time.Second)
				continue
			}
			t.Fatalf("draining failed: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// A person with fifty spaces, syncing every two seconds for ten minutes.
//
// THE THING THIS CATCHES that a single pull cannot: falling behind. Every
// round is one message into the first space, the middle one and the last, and
// every round the backlog is measured. A chunker that serves fifty spaces in
// one pass keeps the backlog at zero. One that serves forty-eight of them
// looks fine for a minute and is two hundred messages behind by the end —
// and the two are indistinguishable at t=1s.
func TestSoakFiftySpacesStayCurrentUnderASustainedPull(t *testing.T) {
	run := soakDuration(t)
	f := newSoakFixture(t, relay.DefaultLimits())

	end := time.Now().Add(run)
	report := time.Now().Add(30 * time.Second)
	rounds, worst, refusals := 0, 0, 0

	for time.Now().Before(end) {
		rounds++
		f.send(t, rounds)

		if _, err := f.bob.PullFromRelay(f.addr); err != nil {
			refusals++
			t.Errorf("round %d was refused, and at a person's cadence nothing "+
				"should be: %v", rounds, err)
		}

		behind := 0
		for _, tid := range f.watched {
			if b := rounds - f.delivered(tid); b > behind {
				behind = b
			}
		}
		if behind > worst {
			worst = behind
		}
		if time.Now().After(report) {
			t.Logf("%d rounds, worst backlog %d, refusals %d", rounds, worst, refusals)
			report = time.Now().Add(30 * time.Second)
		}
		time.Sleep(soakCadence)
	}

	// A last drain: the final round's push and pull race by design.
	f.drain(t, rounds, time.Now().Add(30*time.Second))

	for at, tid := range f.watched {
		if got := f.delivered(tid); got != rounds {
			t.Errorf("watched space %d of 3 received %d of %d rounds — the "+
				"first, the middle and the LAST of the capability list must "+
				"all keep up, or the tail is being dropped", at+1, got, rounds)
		}
	}
	if refusals > 0 {
		t.Errorf("%d refusals in %d rounds: a two-second cadence across %d "+
			"spaces is ordinary use, not abuse", refusals, rounds, soakSpaces)
	}
	// Two rounds of slack for the in-flight push; a trend shows up as tens.
	if worst > 2 {
		t.Errorf("backlog reached %d rounds — steady state is zero, and "+
			"anything that grows is a chunker that never quite catches up", worst)
	}
	t.Logf("clean: %d rounds over %v, worst backlog %d", rounds, run, worst)
}

// The same fifty spaces against a relay that WILL refuse, because the pull
// cadence is deliberately above its limit.
//
// WHAT MUST BE TRUE AFTERWARDS is not "no refusals" — refusals are the point
// — but that a refusal is a pause rather than an ending: the node waits the
// window out, comes back, resumes at the chunk that was refused, and loses
// nothing. Collect is destructive, so "loses nothing" is load-bearing: a
// refusal must happen BEFORE anything is handed over, or the messages in that
// chunk exist nowhere at all.
func TestSoakFiftySpacesRecoverFromRepeatedThrottling(t *testing.T) {
	run := soakDuration(t)
	limits := relay.DefaultLimits()
	// Well under one pull per two seconds, so the window runs out mid-run and
	// keeps running out.
	limits.CollectRatePerMin = 10
	f := newSoakFixture(t, limits)

	end := time.Now().Add(run)
	report := time.Now().Add(30 * time.Second)
	rounds, refusals, recoveries := 0, 0, 0
	wasThrottled := false

	for time.Now().Before(end) {
		rounds++
		f.send(t, rounds)

		_, err := f.bob.PullFromRelay(f.addr)
		switch {
		case err == nil:
			if wasThrottled {
				recoveries++
				wasThrottled = false
			}
		default:
			var re relay.ErrRelay
			if !errors.As(err, &re) || !re.Throttled() {
				t.Fatalf("round %d failed for something other than a rate "+
					"limit: %v", rounds, err)
			}
			refusals++
			wasThrottled = true
		}

		if time.Now().After(report) {
			t.Logf("%d rounds, %d refusals, %d recoveries", rounds, refusals, recoveries)
			report = time.Now().Add(30 * time.Second)
		}
		time.Sleep(soakCadence)
	}

	if refusals == 0 {
		t.Fatal("nothing was throttled, so nothing about recovery was tested — " +
			"the cadence or the limit is wrong")
	}
	if recoveries == 0 {
		t.Fatal("the node was refused and never came back: a rate limit became " +
			"a permanent disconnection, which is the failure this exists to catch")
	}

	// Everything sent must still arrive. Two minutes of slack: a wait can be
	// the remainder of a minute, and the tail may need more than one window.
	f.drain(t, rounds, time.Now().Add(2*time.Minute))
	for at, tid := range f.watched {
		if got := f.delivered(tid); got != rounds {
			t.Errorf("watched space %d of 3 received %d of %d rounds after "+
				"recovery — a throttle must cost time, never messages",
				at+1, got, rounds)
		}
	}
	t.Logf("recovery: %d rounds, %d refusals, %d recoveries, nothing lost",
		rounds, refusals, recoveries)
}
