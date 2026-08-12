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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/relayserver"
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
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
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

// AR-1b.5.3a — damage is not a first run.
//
// The behaviour this replaces sounded cautious and lost messages silently: a
// damaged checkpoint was read as "never activated", so the next start took the
// current frontier as a fresh baseline and every unacknowledged event became
// history with nobody told. Silence about a flood is a good failure; silence
// about a message is not.
func TestADamagedCheckpointFallsBackToThePreviousGeneration(t *testing.T) {
	dir := t.TempDir()

	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}
	rt.AttachNotifications(func(c NotificationCandidate) { rt.AckNotification(c.EventID) })
	for i := 0; i < 3; i++ {
		if _, err := rt.Say(tid, fmt.Sprintf("acknowledged %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	rt.Close()

	// Two generations exist by now; tear the current one in half, exactly as
	// a phone losing power mid-write would.
	cur := filepath.Join(dir, notifyLedgerFile)
	if _, err := os.Stat(filepath.Join(dir, notifyLedgerPrevFile)); err != nil {
		t.Skipf("no previous generation was written yet: %v", err)
	}
	if err := os.WriteFile(cur, []byte(`{"schema_version":1,"activa`), 0o600); err != nil {
		t.Fatal(err)
	}

	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	if got := rt2.NotificationPlaneState(); got != NotifyPlaneActive {
		t.Fatalf("plane state %q after losing ONE generation, want %q — the "+
			"previous generation is intact and is exactly what it is for",
			got, NotifyPlaneActive)
	}
}

func TestLosingBothGenerationsIsNamedRatherThanTreatedAsAFreshStart(t *testing.T) {
	dir := t.TempDir()

	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}
	rt.AttachNotifications(func(c NotificationCandidate) { rt.AckNotification(c.EventID) })
	if _, err := rt.Say(tid, "acknowledged", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	for _, name := range []string{notifyLedgerFile, notifyLedgerPrevFile} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()

	if got := rt2.NotificationPlaneState(); got != NotifyPlaneDamaged {
		t.Fatalf("plane state %q, want %q — reading damage as a first run is "+
			"what turns an unacknowledged event into history nobody hears about",
			got, NotifyPlaneDamaged)
	}

	// NO SILENT REBASELINE: attaching must not write a new baseline over an
	// unknown one, because that is the act that makes the loss permanent and
	// invisible.
	before, _ := os.ReadFile(filepath.Join(dir, notifyLedgerFile))
	rt2.AttachNotifications(func(NotificationCandidate) {})
	after, _ := os.ReadFile(filepath.Join(dir, notifyLedgerFile))
	if string(before) != string(after) {
		t.Fatal("attaching over a damaged checkpoint rewrote it — a baseline invented " +
			"on top of an unknown one is exactly the silent loss this state prevents")
	}
	if got := rt2.NotificationPlaneState(); got != NotifyPlaneDamaged {
		t.Fatalf("plane state %q after attaching, want it to stay %q", got, NotifyPlaneDamaged)
	}

	// Live events still reach the host while damaged: durability is off, and
	// making the person deaf as well would be a second loss on top of the
	// first.
	var live int
	rt2.AttachNotifications(func(NotificationCandidate) { live++ })
	if _, err := rt2.Say(tid, "while damaged", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if live == 0 {
		t.Error("a damaged checkpoint silenced live notifications too")
	}

	// And the way out is deliberate, never automatic.
	if !rt2.ResetNotificationPlane() {
		t.Fatal("an explicit reset did not recover the plane")
	}
	if got := rt2.NotificationPlaneState(); got != NotifyPlaneActive {
		t.Fatalf("plane state %q after an explicit reset, want %q", got, NotifyPlaneActive)
	}
}

// AR-1b.5.4, gate 3 — activation racing with events being applied.
//
// Activation and delivery are two acts against a moving log, and the claim is
// not that they cannot interleave — it is that an event can only land on one
// side of the line. Either it is behind the baseline, and history, or it is
// past it and owed to the host. What must never happen is an event that is
// neither: past the baseline and never delivered, which is a message nobody
// hears about and nothing reports.
//
// WHAT THIS TEST PROVES, AND WHY IT SURVIVES A BROKEN ORDERING. Attaching the
// sink before reading the baseline is the obvious defence, and inverting it —
// read the frontier, sleep, then subscribe — does NOT fail this test. That is
// not a weakness of the assertion; it is the second defence doing its job: an
// event lost in that window is past the watermark, so the next attach
// redelivers it. Disabling redelivery instead DOES fail (the unacknowledged
// test above catches it).
//
// So this asserts the property the product actually needs — nothing past the
// watermark is unreachable — and it takes BOTH mechanisms breaking to fail.
// The ordering has its own test (TestAttachingIsAtomicWithEmission), where it
// is the only thing standing between a host and a gap in its cursor stream.
func TestActivationRacingWithAppliesLosesNothing(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	delivered := map[id.EventID]bool{}

	// The writer runs across the activation, so the first attach happens in
	// the middle of a stream rather than in a quiet moment nobody races.
	stop := make(chan struct{})
	done := make(chan struct{})
	written := make(chan id.EventID, 512)
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			eid, err := rt.Say(tid, fmt.Sprintf("during activation %d", i), SayOptions{})
			if err != nil {
				return
			}
			written <- eid
		}
	}()

	time.Sleep(5 * time.Millisecond) // let some events land BEFORE activation
	rt.AttachNotifications(func(c NotificationCandidate) {
		mu.Lock()
		delivered[c.EventID] = true
		mu.Unlock()
	})
	time.Sleep(20 * time.Millisecond)
	close(stop)
	<-done
	close(written)

	// Detaching and attaching again is what a host does after a restart: it
	// asks for everything still unacknowledged. Nothing here acknowledges, so
	// every event past the baseline must come back.
	rt.AttachNotifications(nil)
	rt.AttachNotifications(func(c NotificationCandidate) {
		mu.Lock()
		delivered[c.EventID] = true
		mu.Unlock()
	})

	// THE INVARIANT, stated against the watermark rather than against a
	// hopeful proxy. An event at or below the confirmed sequence is history
	// and is meant to be silent; every event ABOVE it is owed to the host and
	// must have been delivered — live, or by the redelivery above.
	//
	// An earlier version of this test asked the ledger whether an event was
	// "still pending", which is also false for an event that was never
	// delivered at all: it would have passed on exactly the loss it exists to
	// catch. The log's own sequence numbers are the only honest comparison.
	rt.mu.Lock()
	st := rt.spaces[tid]
	var late []string
	_ = st.space.Log.Replay(func(a eventlog.Applied) error {
		if a.Env == nil {
			return nil
		}
		confirmed := rt.notifyLedger.confirmedSeq(tid, a.Env.Device)
		mu.Lock()
		got := delivered[a.ID]
		mu.Unlock()
		if a.Env.Sequence > confirmed && !got {
			late = append(late, fmt.Sprintf("seq %d (watermark %d)", a.Env.Sequence, confirmed))
		}
		return nil
	})
	rt.mu.Unlock()

	if len(late) > 0 {
		t.Fatalf("%d events are past the watermark and were never delivered, "+
			"live or on a fresh attach: %v — each is a message nobody would ever "+
			"be told about, and nothing anywhere reports it", len(late), late)
	}
}

// AR-1b.5.4, gate 12 — what log compaction may not cross.
func TestTheRetentionFloorHoldsBackWhatNobodyHasConfirmed(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}

	acked := 0
	rt.AttachNotifications(func(c NotificationCandidate) {
		// Only the first two are acknowledged: the rest are still owed.
		if acked < 2 {
			rt.AckNotification(c.EventID)
			acked++
		}
	})
	for i := 0; i < 5; i++ {
		if _, err := rt.Say(tid, fmt.Sprintf("event %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	floor := rt.NotificationDeliveredThrough()[tid][rt.Device.ID]
	if floor == 0 {
		t.Fatal("the floor did not move at all — acknowledgements are not reaching it")
	}
	// The floor must be BEHIND the tip: everything after it is still owed to
	// the host, and a compactor that collapsed it would leave the watermark
	// pointing at events that no longer exist.
	var tip uint64
	r := rt // the log's own view
	r.mu.Lock()
	for _, ch := range r.spaces[tid].space.Log.Summary() {
		if ch.Device == r.Device.ID {
			tip = ch.ContiguousUntil
		}
	}
	r.mu.Unlock()
	if floor >= tip {
		t.Fatalf("retention floor %d is at or past the tip %d while %d events are "+
			"unacknowledged — compaction guided by this would drop what the host "+
			"is still owed", floor, tip, 5-acked)
	}
}

// AR-1b.5.5 gates 1 and 2 — an acknowledgement out of order does not move the
// watermark past the gap, and closing the gap releases everything at once.
//
// The failure this prevents is quiet: acknowledging 40 while 38 is still owed
// would, on a crash, resume from 40 and treat 38 and 39 as history. The
// person's two missing messages would never be mentioned again.
func TestAnAcknowledgementOutOfOrderDoesNotJumpTheGap(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []NotificationCandidate
	rt.AttachNotifications(func(c NotificationCandidate) {
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
	})
	for i := 0; i < 4; i++ {
		if _, err := rt.Say(tid, fmt.Sprintf("event %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	all := append([]NotificationCandidate(nil), got...)
	mu.Unlock()
	if len(all) != 4 {
		t.Fatalf("%d candidates, want 4", len(all))
	}

	before := rt.NotificationDeliveredThrough()[tid][rt.Device.ID]

	// Acknowledge the LAST one only. The watermark must not move at all: the
	// three before it are still owed.
	rt.AckNotification(all[3].EventID)
	if now := rt.NotificationDeliveredThrough()[tid][rt.Device.ID]; now != before {
		t.Fatalf("the watermark moved to %d on an out-of-order acknowledgement "+
			"(was %d) — the events in the gap would become history", now, before)
	}

	// Close the gap. The watermark should now pass ALL of them, including the
	// one acknowledged early: the out-of-order set exists exactly so that
	// acknowledgement is not thrown away.
	rt.AckNotification(all[0].EventID)
	rt.AckNotification(all[1].EventID)
	rt.AckNotification(all[2].EventID)

	after := rt.NotificationDeliveredThrough()[tid][rt.Device.ID]
	if after != all[3].SourceSequence {
		t.Fatalf("after closing the gap the watermark is %d, want %d — the early "+
			"acknowledgement was dropped rather than remembered",
			after, all[3].SourceSequence)
	}
}

// AR-1b.5.5 gate 4/5 — what a consumer may compact must survive a checkpoint
// ROLLBACK, not merely the current generation.
func TestRetainFromIsBehindTheOlderGenerationNotTheNewerOne(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}

	rt.AttachNotifications(func(c NotificationCandidate) { rt.AckNotification(c.EventID) })
	if _, err := rt.Say(tid, "first", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	rt.notifyLedger.flush() // generation 1 on disk

	early := rt.NotificationDeliveredThrough()[tid][rt.Device.ID]
	for i := 0; i < 3; i++ {
		if _, err := rt.Say(tid, fmt.Sprintf("later %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	rt.notifyLedger.flush() // current moves ahead; the older one steps back

	through := rt.NotificationDeliveredThrough()[tid][rt.Device.ID]
	retain := rt.NotificationRetainFrom()[tid][rt.Device.ID]

	if through <= early {
		t.Fatalf("the current watermark did not advance (%d), so this proves nothing", through)
	}
	// The floor must sit behind what the PREVIOUS generation knew, because a
	// damaged current file resumes from that one and replays the difference.
	// Trimming to `through` would leave the core replaying events a host had
	// already forgotten, which is how a dismissed notification comes back.
	if retain > early+1 {
		t.Fatalf("retain-from is %d, past the older generation's watermark %d — "+
			"a rollback would replay events a consumer had already trimmed, and "+
			"a dismissed notification would return as news", retain, early)
	}
}

// AR-1b.5.6 scenario 01 — a space the plane has never seen, whose log is
// already full.
//
// A space JOINED AFTER activation has no entry in the watermark, and
// confirmedSeq answers zero for anything it has never heard of. The live path
// is safe — join history is installed before the absorb funnel exists — but
// REDELIVERY walks the log from the watermark, and a watermark of zero means
// the whole imported history is "unacknowledged". The first restart after
// joining a long-running room would hand the host every message ever written
// in it.
//
// THE SITUATION IS BUILT DIRECTLY RATHER THAN THROUGH A JOIN, and that is a
// correction rather than a shortcut. The first version of this test joined a
// real space over a real relay — and the history never arrived before the node
// closed, so it passed identically with the fix removed. A test that cannot
// reach its hazard is worse than no test: it reports safety it never checked.
//
// So the state is constructed exactly: a full log, and a ledger that has never
// heard of the space — which is what a join, a restore, or an imported backup
// all leave behind.
func TestASpaceThePlaneHasNeverSeenDoesNotReplayItsWholeLog(t *testing.T) {
	dir := t.TempDir()

	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpace("Long Room")
	if err != nil {
		t.Fatal(err)
	}
	rt.AttachNotifications(func(c NotificationCandidate) { rt.AckNotification(c.EventID) })
	const history = 25
	for i := 0; i < history; i++ {
		if _, err := rt.Say(tid, fmt.Sprintf("already read %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	rt.Close()

	// Forget the space, keeping the plane activated: the state a join leaves.
	l := newNotifyLedger(dir)
	l.mu.Lock()
	delete(l.state.Confirmed, tid.Hex())
	l.saveLocked()
	l.mu.Unlock()

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
	if got > 0 {
		t.Fatalf("a space with %d events that the plane had never seen produced %d "+
			"candidates — everything already in it must be history, or joining a "+
			"long-running room means being told about every message ever written "+
			"in it", history, got)
	}
}

// AR-1b.6b.6 — a person renames themselves, and the next notification says so.
//
// FOUND BY THE VISUAL GATE ON A PHONE, where the shade went on showing the old
// name after the device had already applied the rename — its member list said
// one thing and its notification said another. A name is the one thing on a
// notification a person checks before deciding whether to look, so a stale one
// is not cosmetic.
//
// The rename is its own event: renaming republishes a manifest into every
// space, and the label is resolved when the candidate is DECORATED. So the
// test does what the gate does — rename, sync, then speak — and asserts on the
// candidate the host would receive.
func TestARenameReachesTheNextNotification(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
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

	tid, err := alice.CreateSpace("a room")
	if err != nil {
		t.Fatal(err)
	}
	info, err := alice.MintPass(tid, 1, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := bob.JoinByPass(info.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, reqID, JoinReady)

	var mu sync.Mutex
	var got []NotificationCandidate
	bob.AttachNotifications(func(c NotificationCandidate) {
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
	})

	if err := alice.SetName("Renamed Alice"); err != nil {
		t.Fatal(err)
	}
	// The rename travels first, on its own. Then the message.
	sync := func() {
		for i := 0; i < 20; i++ {
			_, _, _ = alice.PushToRelay(addr, tid)
			_, _ = bob.PullFromRelay(addr)
			time.Sleep(100 * time.Millisecond)
		}
	}
	sync()
	if _, err := alice.Say(tid, "after the rename", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	sync()

	mu.Lock()
	defer mu.Unlock()
	var last string
	for _, c := range got {
		if c.PreviewText == "after the rename" {
			last = c.SenderLabel
		}
	}
	if last == "" {
		t.Fatalf("no candidate for the message; got %d candidate(s)", len(got))
	}
	if last != "Renamed Alice" {
		t.Fatalf("the notification would say %q — a name a person changed and "+
			"the device has already applied", last)
	}
}
