// AR-1b's central invariant, and the one the whole wave is judged on:
//
//	Opening an existing journal publishes NOT ONE historical notification,
//	and the next new event publishes exactly one.
//
// The hazard is real and specific. Space.OnAbsorb fires for every absorbed
// event, and `AttachLog` replays the WHOLE log through it at open — so a
// notification plane wired there naively turns a first run over a large
// journal into one "new message" per event ever written.
//
// The defence here is structural, not a filter: the sink is ARMED separately,
// and a host cannot arm it until Open has returned. There is no id set to get
// right and no frontier to forget to persist for THIS hazard. (The durable
// presentation frontier and the bounded id set still belong to the coordinator
// — they defend against reconnect and process restart, which are different
// hazards and get their own tests on the Android side.)
package node

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/relay"
)

func TestOpeningAJournalNotifiesNothingAndTheNextEventNotifiesOnce(t *testing.T) {
	dir := t.TempDir()

	// The number is deliberately small. The invariant is structural — history
	// cannot reach a sink that does not exist yet — so it holds identically at
	// 300 and at 16 000, and a three-minute unit test would only make people
	// run it less.
	const history = 300

	rt := openRuntime(t, dir, "author")
	tid, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < history; i++ {
		if _, err := rt.Say(tid, fmt.Sprintf("historical %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	rt.Close()

	// Reopen: every one of those events is replayed through the absorb funnel.
	rt2 := openRuntime(t, dir, "author")
	defer rt2.Close()

	var mu sync.Mutex
	var got []NotificationCandidate
	rt2.ArmNotifications(func(c NotificationCandidate) {
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
	})

	mu.Lock()
	n := len(got)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("%d historical notifications after reopening a %d-event journal — "+
			"the plane is seeing the replay, which is the whole failure this "+
			"invariant exists to prevent", n, history)
	}

	// And now the next new event, exactly once.
	eid, err := rt2.Say(tid, "the new one", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("a single new event produced %d candidates, want exactly 1", len(got))
	}
	c := got[0]
	if c.EventID != eid {
		t.Errorf("candidate carries %v, want the event that was just written (%v)", c.EventID, eid)
	}
	if c.SpaceID != tid {
		t.Errorf("candidate names space %v, want %v", c.SpaceID, tid)
	}
	if !c.AuthoredLocally {
		t.Error("an event this node wrote must be marked AuthoredLocally — a person " +
			"is not told about the thing they just did, and the host should not " +
			"have to work out who they are to know that")
	}
}

// Disarming is a supported state, not a hole. A person who refuses the
// notification permission is in an ORDINARY situation, and the core should
// stop producing candidates nobody can render rather than have the host drop
// them silently and hope.
func TestDisarmingStopsCandidatesRatherThanLeavingTheHostToDropThem(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "author")
	defer rt.Close()
	tid, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}

	var seen []id.EventID
	rt.ArmNotifications(func(c NotificationCandidate) { seen = append(seen, c.EventID) })
	if _, err := rt.Say(tid, "armed", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("armed: %d candidates, want 1", len(seen))
	}

	rt.ArmNotifications(nil)
	if _, err := rt.Say(tid, "disarmed", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("disarmed: %d candidates, want the count to stay at 1", len(seen))
	}
}

// The same hazard in different clothes: a space JOINED LATER, whose history
// travels with it.
//
// The Open path is safe for a structural reason — AttachLog runs before
// attach(), so during a reopen there is no absorb funnel to reach. A join is
// the case where the plane is ALREADY armed and a whole history arrives at
// once, and it is the one that would have turned "welcome to the space" into
// a notification per message ever written in it.
//
// memory=everything on purpose: this is the configuration where history
// actually travels, so it is the configuration where the hazard is real.
func TestJoiningASpaceWithHistoryDoesNotNotifyForThatHistory(t *testing.T) {
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

	tid, err := alice.CreateSpace("Long Room")
	if err != nil {
		t.Fatal(err)
	}
	const history = 25
	for i := 0; i < history; i++ {
		if _, err := alice.Say(tid, fmt.Sprintf("before bob %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	// Bob's plane is armed BEFORE he joins — a phone that has been running.
	var mu sync.Mutex
	var got []NotificationCandidate
	bob.ArmNotifications(func(c NotificationCandidate) {
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
	})

	info, err := alice.MintPass(tid, 1, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := bob.JoinByPass(info.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, reqID, JoinReady)

	mu.Lock()
	historical := len(got)
	mu.Unlock()
	if historical > 0 {
		t.Errorf("joining a space with %d prior messages produced %d notifications — "+
			"a person joining a long-running room would be told about every "+
			"message ever written in it", history, historical)
	}
}

// AR-1b.5.1 — the presentation snapshot, the cursor, and the units.
//
// Every one of these is a field a host will act on, and each has a way of
// being quietly wrong that only shows up on a device: a timestamp in the wrong
// unit renders as 1970, a cursor that does not advance makes "everything up to
// here" meaningless, and a label resolved from the wrong side turns somebody
// else's name onto a person's message.
func TestACandidateCarriesItsUnitsItsCursorAndWhatMayBeShown(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	defer rt.Close()

	tid, err := rt.CreateSpace("Long Room")
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []NotificationCandidate
	baseline := rt.AttachNotifications(func(c NotificationCandidate) {
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
	})

	before := time.Now().Add(-2 * time.Second).UnixMilli()
	if _, err := rt.Say(tid, "the first thing said", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Say(tid, "the second thing said", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	after := time.Now().Add(2 * time.Second).UnixMilli()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("%d candidates, want 2", len(got))
	}

	// MILLISECONDS, and the window is the assertion. A value in seconds would
	// land in January 1970 and fail this by fifty-six years — which is exactly
	// how the defect announced itself in a notification shade.
	for i, c := range got {
		if int64(c.OccurredAtUnixMs) < before || int64(c.OccurredAtUnixMs) > after {
			t.Errorf("candidate %d: occurred_at_unix_ms %d is outside [%d, %d] — "+
				"a value in seconds would land in 1970", i, c.OccurredAtUnixMs, before, after)
		}
	}

	// The cursor is monotonic and starts past the baseline the attach
	// returned. Without the second half, a cursor that always reported 1
	// would satisfy "monotonic" and tell a host nothing.
	if got[0].PresentationCursor <= baseline {
		t.Errorf("first cursor %d is not past the attach baseline %d",
			got[0].PresentationCursor, baseline)
	}
	if got[1].PresentationCursor <= got[0].PresentationCursor {
		t.Errorf("cursor did not advance: %d then %d",
			got[0].PresentationCursor, got[1].PresentationCursor)
	}

	// The presentation snapshot: shown, never acted on.
	if got[0].SpaceLabel != "Long Room" {
		t.Errorf("space label %q, want %q", got[0].SpaceLabel, "Long Room")
	}
	if got[0].SenderLabel != "alice" {
		t.Errorf("sender label %q, want %q", got[0].SenderLabel, "alice")
	}
	if got[0].PreviewText != "the first thing said" {
		t.Errorf("preview %q, want the message text", got[0].PreviewText)
	}
}

// A preview is bounded at the SOURCE. A host that receives a whole post has
// been handed more of a person's content than it can ever display, and the
// bound belongs where the content is, not where it is rendered.
func TestAPreviewIsBoundedBeforeItLeavesTheCore(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got NotificationCandidate
	rt.AttachNotifications(func(c NotificationCandidate) {
		mu.Lock()
		got = c
		mu.Unlock()
	})

	long := strings.Repeat("я", 4000) // multi-byte on purpose: the bound is runes
	if _, err := rt.Say(tid, long, SayOptions{}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if n := len([]rune(got.PreviewText)); n > maxPreviewRunes+1 {
		t.Fatalf("preview is %d runes, want at most %d plus an ellipsis",
			n, maxPreviewRunes)
	}
	if !strings.HasSuffix(got.PreviewText, "…") {
		t.Error("a clipped preview does not say it was clipped")
	}
}

// AR-1b.5.2 — attaching is ONE operation.
//
// The obvious shape is two: read the cursor, remember it as a baseline, then
// subscribe. An event applied between those two steps is past the baseline and
// before the sink — announced to nobody, recoverable by nobody, and reported
// as missing by nothing.
//
// The assertion is a CONTIGUITY claim rather than a count, because that is
// what the window actually breaks: every cursor a host sees must follow its
// baseline with no gap. A lost event leaves a hole in the sequence, and a hole
// is visible however the scheduler happened to interleave the goroutines.
//
// ITS SENSITIVITY IS HONEST ABOUT ITSELF. This detects a race, so it detects
// wide windows reliably and narrow ones probabilistically. Red-proofed against
// a two-step attach: with a 10 ms gap between reading the cursor and
// installing the sink it fails on the first round; with 200 µs it survived
// forty rounds. So it is a guard against the SHAPE returning, not a proof that
// no window of any size exists — the proof of that is the single critical
// section, which is right there to read.
func TestAttachingIsAtomicWithEmission(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}

	// A writer running the whole time, so attaching always lands in the
	// middle of a stream rather than in a quiet moment nobody races.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := rt.Say(tid, fmt.Sprintf("during %d", i), SayOptions{}); err != nil {
				return
			}
		}
	}()
	defer func() { close(stop); <-done }()

	for round := 0; round < 200; round++ {
		var mu sync.Mutex
		var seen []uint64
		baseline := rt.AttachNotifications(func(c NotificationCandidate) {
			// Acknowledged immediately, which is what a host does once it
			// holds a candidate durably. Without it every round would also
			// carry the previous rounds' unacknowledged candidates back as
			// redeliveries — correct behaviour (AR-1b.5.3), and noise for a
			// test about the live stream.
			rt.AckNotification(c.EventID)
			if c.PresentationCursor == 0 {
				return // a redelivery is not a position in this stream
			}
			mu.Lock()
			seen = append(seen, c.PresentationCursor)
			mu.Unlock()
		})
		time.Sleep(time.Millisecond)
		rt.AttachNotifications(nil)

		mu.Lock()
		got := append([]uint64(nil), seen...)
		mu.Unlock()

		want := baseline + 1
		for _, c := range got {
			if c != want {
				t.Fatalf("round %d: baseline %d then cursors %v — "+
					"a gap at %d means an event was applied between reading the "+
					"cursor and installing the sink, and nothing would ever "+
					"have reported it missing", round, baseline, got, want)
			}
			want++
		}
	}
}

// AR-1b.5.3 — the window between "applied" and "the host holds it".
//
// This is the failure the live-only plane could not survive: an event is
// applied, the candidate is on its way to Android, and the process dies. The
// runtime cursor cannot help — it is scoped to one runtime by design — so on
// the next start a naive baseline would call that event history and the
// notification would be gone with nothing anywhere reporting it.
//
// Killing a process is not something a unit test can do, so the shape is
// reproduced exactly: a runtime that receives a candidate and never
// acknowledges it, then closes. What must happen is that the next attach hands
// it over again.
func TestAnUnacknowledgedCandidateSurvivesTheProcess(t *testing.T) {
	dir := t.TempDir()

	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}

	// Activation: from here, history is history.
	rt.AttachNotifications(func(NotificationCandidate) {})

	// One event arrives and is deliberately NOT acknowledged — the host got it
	// and died before writing it down.
	lost, err := rt.Say(tid, "the message that must not be lost", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rt.Close()

	// A new process, a new runtime, a new epoch.
	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()

	var mu sync.Mutex
	var got []NotificationCandidate
	rt2.AttachNotifications(func(c NotificationCandidate) {
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
	})

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, c := range got {
		if c.EventID == lost {
			found = true
		}
	}
	if !found {
		t.Fatalf("the unacknowledged event was not redelivered after a restart — "+
			"%d candidates arrived, none of them the one nobody confirmed", len(got))
	}
}

// The other half, and the one a naive fix breaks: what WAS acknowledged must
// stay acknowledged. A ledger that redelivered everything after a restart
// would be as wrong as one that redelivered nothing — it would announce a
// person's whole history every time their phone rebooted.
func TestAnAcknowledgedCandidateIsNeverDeliveredAgain(t *testing.T) {
	dir := t.TempDir()

	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}
	rt.AttachNotifications(func(c NotificationCandidate) { rt.AckNotification(c.EventID) })
	for i := 0; i < 5; i++ {
		if _, err := rt.Say(tid, fmt.Sprintf("acknowledged %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	rt.Close() // flushes the watermark

	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()

	var mu sync.Mutex
	var got int
	rt2.AttachNotifications(func(NotificationCandidate) {
		mu.Lock()
		got++
		mu.Unlock()
	})

	mu.Lock()
	defer mu.Unlock()
	if got != 0 {
		t.Fatalf("%d acknowledged events were delivered again after a restart", got)
	}
}

// Activation happens ONCE. A restart is not a first run, and the difference is
// the whole reason the marker is durable: without it, every launch would take
// the current frontier as a new baseline, which reads as correct until the day
// a candidate is lost between the two.
func TestActivationHappensOnceAndARestartIsNotAFirstRun(t *testing.T) {
	dir := t.TempDir()

	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}
	const history = 20
	for i := 0; i < history; i++ {
		if _, err := rt.Say(tid, fmt.Sprintf("before activation %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	// FIRST activation: everything already written is history, silently.
	var first int
	rt.AttachNotifications(func(NotificationCandidate) { first++ })
	if first != 0 {
		t.Fatalf("activating over %d prior events produced %d candidates, want 0",
			history, first)
	}
	// One event nobody acknowledges, so there is something to resume with.
	if _, err := rt.Say(tid, "after activation", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	var mu sync.Mutex
	var second []NotificationCandidate
	rt2.AttachNotifications(func(c NotificationCandidate) {
		mu.Lock()
		second = append(second, c)
		mu.Unlock()
	})

	mu.Lock()
	defer mu.Unlock()
	// Exactly the unacknowledged one: not zero (which would mean the restart
	// re-baselined and ate it) and not twenty-one (which would mean activation
	// ran again and the marker meant nothing).
	if len(second) != 1 {
		t.Fatalf("after a restart: %d candidates, want exactly the 1 nobody "+
			"acknowledged — 0 means the baseline moved and ate it, %d would mean "+
			"activation ran a second time", len(second), history+1)
	}
}
