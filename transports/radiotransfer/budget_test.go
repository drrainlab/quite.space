// The repair budget: proving a transfer is finite no matter what arrives.
//
// The existing fakes model LOSS. The defect these tests exist for was a QUEUE:
// on two real boards every one of fourteen SACKs was delivered, none was lost,
// and the first was heard sixty-two seconds after it was sent. Feedback that
// late is a history rather than a report — each stale frame acknowledged one
// more fragment, each looked like progress, and progress reset the round
// budget. Fifteen windows ran against a MaxRounds of six.
//
// So there is a carrier here that holds feedback and releases it in order,
// which reproduces the mechanism instead of something that merely resembles it.
package radiotransfer

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports"
)

// delayedFeedbackAir carries frames promptly one way and LATE the other.
//
// Late, not lost: every frame arrives, in order, exactly as measured. It can
// also drop COMMITs, which is the other half of what the boards did — a peer
// holding the whole message and unable to say so.
type delayedFeedbackAir struct {
	mu       sync.Mutex
	mtu      int
	toPeer   chan []byte
	fromPeer chan []byte

	// holdFor delays everything this side RECEIVES, so the sender reads a
	// backlog of stale reports rather than the current one.
	holdFor time.Duration
	// dropCommits silently discards COMMIT frames this side would receive.
	// The key is needed to tell which they are, so it is set by the pair.
	dropCommits bool
	key         *TransferKey

	held    []timed
	dropped int
}

type timed struct {
	at time.Time
	b  []byte
}

func newDelayedPair(mtu int, key *TransferKey, holdFor time.Duration,
	dropCommits bool) (sender, receiver *delayedFeedbackAir) {
	a2b, b2a := make(chan []byte, 512), make(chan []byte, 512)
	// Only the SENDER side is impaired: it is the one whose view of the
	// window goes stale, and impairing both would prove less, not more.
	sender = &delayedFeedbackAir{mtu: mtu, toPeer: a2b, fromPeer: b2a,
		holdFor: holdFor, dropCommits: dropCommits, key: key}
	receiver = &delayedFeedbackAir{mtu: mtu, toPeer: b2a, fromPeer: a2b, key: key}
	return sender, receiver
}

func (a *delayedFeedbackAir) MTU() int { return a.mtu }

func (a *delayedFeedbackAir) Send(ctx context.Context, _ RadioAddress, frame []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case a.toPeer <- append([]byte(nil), frame...):
		return nil
	default:
		return ErrCarrierFull
	}
}

func (a *delayedFeedbackAir) Receive(ctx context.Context) (RadioAddress, []byte, error) {
	for {
		// Take whatever has arrived, without blocking, so the backlog builds
		// while the sender is busy transmitting — which is what a half-duplex
		// radio actually does to its own feedback.
		for {
			select {
			case b := <-a.fromPeer:
				if a.dropCommits && a.isCommit(b) {
					a.mu.Lock()
					a.dropped++
					a.mu.Unlock()
					continue
				}
				a.mu.Lock()
				a.held = append(a.held, timed{at: time.Now(), b: b})
				a.mu.Unlock()
				continue
			default:
			}
			break
		}

		a.mu.Lock()
		if len(a.held) > 0 && time.Since(a.held[0].at) >= a.holdFor {
			out := a.held[0].b
			a.held = a.held[1:]
			a.mu.Unlock()
			return RadioAddress("peer"), out, nil
		}
		a.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

func (a *delayedFeedbackAir) isCommit(b []byte) bool {
	f, err := Decode(b, a.key)
	return err == nil && f.Kind == KindCommit
}

func (a *delayedFeedbackAir) Credit() transports.Credit {
	return transports.Credit{Packets: 8, Known: true, RetryAfter: time.Millisecond}
}

// driveAir runs the one read loop a session is entitled to, for any carrier.
func driveAir(ctx context.Context, s *Session, air *delayedFeedbackAir) {
	go func() {
		for {
			rctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			_, raw, err := air.Receive(rctx)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				_, _ = s.Deliver(ctx, RadioAddress("peer"), raw)
			}
			s.PumpSACKs(ctx)
		}
	}()
}

// budgetLimits are shrunk so the tests run in milliseconds, with the repair
// budget stated explicitly rather than derived — a test that depends on a
// derivation is testing the derivation.
func budgetLimits(maxFrames int, maxRepair time.Duration) Limits {
	return Limits{
		Window: 4, MaxRounds: 20,
		AckTimeout: 60 * time.Millisecond,
		SACKDelay:  5 * time.Millisecond,
		SendFloor:  time.Millisecond,
		FrameGap:   time.Millisecond,
		Repair:     RepairBudget{MaxFrames: maxFrames, MaxDuration: maxRepair},
	}
}

// THE headline. Every stale SACK adds acknowledged fragments and may reset the
// futility counter; NONE of them returns airtime already spent, so the loop is
// finite even though the peer never stops appearing to make progress.
func TestStaleDripNeverRestoresTheRepairBudget(t *testing.T) {
	key := testKey(t)
	// The budget must be big enough that the stale drip STARTS before it
	// runs out — the first cut of this test spent six frames in two quick
	// rounds and gave up before the held feedback was ever released, which
	// proved nothing about staleness (and the guard below said so). Twenty
	// frames of budget against an 80 ms hold and 40 ms rounds means the
	// sender lives through many stale reports before the ceiling.
	lim := budgetLimits(20, 3*time.Second)
	lim.AckTimeout = 40 * time.Millisecond

	// Feedback arrives two windows late, and the COMMIT never arrives at
	// all: exactly the two conditions measured on the boards.
	sAir, rAir := newDelayedPair(200, key, 80*time.Millisecond, true)
	sender, err := NewSession(sAir, key, Options{Limits: lim})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewSession(rAir, key, Options{Limits: lim})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	driveAir(ctx, receiver, rAir)
	driveAir(ctx, sender, sAir)

	done := make(chan error, 1)
	go func() {
		done <- sender.Send(ctx, RadioAddress("peer"),
			bytes.Repeat([]byte("stale progress is still spent airtime. "), 20))
	}()

	select {
	case err := <-done:
		if !errors.Is(err, ErrDeliveryUnconfirmed) {
			t.Fatalf("a transfer fed stale progress ended with %v, want it to "+
				"be unconfirmed", err)
		}
		// It must NOT claim the peer received nothing: the whole point is
		// that the peer very likely holds the message.
		var du *DeliveryUnconfirmedError
		if !errors.As(err, &du) {
			t.Fatalf("error is not a DeliveryUnconfirmedError: %v", err)
		}
		if du.RepairFrames > lim.Repair.MaxFrames {
			t.Fatalf("spent %d repair frames against a budget of %d",
				du.RepairFrames, lim.Repair.MaxFrames)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the transfer did not end — stale progress kept restoring the " +
			"budget, which is the defect this test exists for")
	}

	st := sender.Stats()
	if st.Unconfirmed != 1 {
		t.Fatalf("one transfer ended unconfirmed, stats say %d: %+v",
			st.Unconfirmed, st)
	}
	if st.RepairDataFrames > lim.Repair.MaxFrames {
		t.Fatalf("session spent %d repair frames, budget %d",
			st.RepairDataFrames, lim.Repair.MaxFrames)
	}
	// The feedback really did arrive — late, in order, and useless. If none
	// had, this would be a test about a deaf link rather than a stale one.
	if st.FramesIn == 0 {
		t.Fatal("no feedback reached the sender at all, so this proved nothing " +
			"about STALE feedback")
	}
}

// The case a frame budget can never catch: every fragment reaches the peer on
// the first pass, so there is nothing to resend, and only the confirmation is
// lost. Without a deadline the loop waits for something to count forever.
func TestACompleteButUncommittedTransferEndsRatherThanSpinning(t *testing.T) {
	key := testKey(t)
	// A frame budget generous enough that it CANNOT be what stops this, so
	// the only thing left to stop it is the clock. That is the claim under
	// test: the two dimensions are not redundant.
	lim := budgetLimits(10_000, 400*time.Millisecond)

	// No delay at all — the peer hears everything immediately. Only its
	// confirmation is lost.
	sAir, rAir := newDelayedPair(200, key, 0, true)
	sender, _ := NewSession(sAir, key, Options{Limits: lim})
	receiver, _ := NewSession(rAir, key, Options{Limits: lim})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	driveAir(ctx, receiver, rAir)
	driveAir(ctx, sender, sAir)

	start := time.Now()
	err := sender.Send(ctx, RadioAddress("peer"),
		bytes.Repeat([]byte("every fragment arrived. "), 12))
	took := time.Since(start)

	if !errors.Is(err, ErrDeliveryUnconfirmed) {
		t.Fatalf("got %v, want an unconfirmed delivery", err)
	}
	if !errors.Is(err, ErrRepairDeadlineExhausted) {
		t.Fatalf("got %v, want the DEADLINE to be the cause — with a budget of "+
			"ten thousand frames nothing else could have stopped it, which is "+
			"the whole reason the deadline is not redundant", err)
	}
	if errors.Is(err, ErrRepairFrameBudgetExhausted) {
		t.Fatal("the frame budget fired despite being effectively unlimited")
	}
	// Generous, because the assertion is "it ends", not "it ends in exactly
	// this long": the deadline plus a round of slack.
	if took > 5*time.Second {
		t.Fatalf("took %s to give up on a lost confirmation", took)
	}
	if st := sender.Stats(); st.DeadlineExpired != 1 || st.Unconfirmed != 1 {
		t.Fatalf("stats do not name the deadline as the reason: %+v", st)
	}
}

// Per-FRAGMENT, not per-pass: a second window is fresh data and must cost
// nothing, or every long message would be charged for being long.
func TestOnlyResubmittedDataIsCharged(t *testing.T) {
	key := testKey(t)
	o, err := NewOutbound(bytes.Repeat([]byte("y"), 900), 200, Limits{Window: 4}, key)
	if err != nil {
		t.Fatal(err)
	}
	if o.Count() < 6 {
		t.Fatalf("this test needs several windows; got %d fragments", o.Count())
	}

	// First pass over the first window: nothing has been submitted, so
	// nothing is a retransmission.
	for i := range 4 {
		if o.WasSubmitted(i) {
			t.Fatalf("fragment %d counted as a resend on its first offer", i)
		}
		o.MarkSubmitted(i)
	}
	// The SECOND window is new data, not repair.
	for i := 4; i < o.Count(); i++ {
		if o.WasSubmitted(i) {
			t.Fatalf("fragment %d of a later window counted as a resend", i)
		}
		o.MarkSubmitted(i)
	}
	if o.RepairFrames() != 0 {
		t.Fatalf("a clean multi-window pass charged %d repair frames",
			o.RepairFrames())
	}
	// Now a genuine resend.
	if !o.WasSubmitted(2) {
		t.Fatal("fragment 2 was submitted and is not remembered as such")
	}
	o.ChargeRepairFrame(140)
	if o.RepairFrames() != 1 || o.RepairBytes() != 140 {
		t.Fatalf("a resend charged %d frames / %d bytes, want 1 / 140",
			o.RepairFrames(), o.RepairBytes())
	}
}

// A long first pass must not spend the budget that exists to bound its repair.
func TestALongFirstPassDoesNotStartTheRepairClock(t *testing.T) {
	key := testKey(t)
	o, err := NewOutbound(bytes.Repeat([]byte("z"), 900), 200, Limits{Window: 4}, key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	for i := range o.Count() - 1 { // every fragment but the last
		o.MarkSubmitted(i)
		if o.Repairing() {
			t.Fatalf("the repair clock started during the first pass, at "+
				"fragment %d of %d", i, o.Count())
		}
		if err := o.OverBudget(now.Add(time.Hour)); err != nil {
			t.Fatalf("a first pass was over budget after an hour of not "+
				"repairing: %v", err)
		}
	}
	if o.AllSubmitted() {
		t.Fatal("the last fragment has not been submitted yet")
	}
}

// The second way a repair phase begins: everything is with the carrier and
// nothing has confirmed it. Zero frames are involved, which is precisely why
// the deadline is not optional.
func TestAllSubmittedButUnconfirmedStartsTheRepairClock(t *testing.T) {
	key := testKey(t)
	o, err := NewOutbound(bytes.Repeat([]byte("q"), 400), 200, Limits{
		Window: 4, Repair: RepairBudget{MaxDuration: time.Minute}}, key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	for i := range o.Count() {
		o.MarkSubmitted(i)
	}
	if !o.AllSubmitted() {
		t.Fatal("every fragment was submitted and AllSubmitted says otherwise")
	}
	o.BeginRepair(now)
	if !o.Repairing() {
		t.Fatal("the repair clock did not start")
	}
	if o.RepairFrames() != 0 {
		t.Fatal("nothing was resent, so nothing may be charged")
	}
	if err := o.OverBudget(now.Add(30 * time.Second)); err != nil {
		t.Fatalf("inside the deadline and already over budget: %v", err)
	}
	if err := o.OverBudget(now.Add(2 * time.Minute)); !errors.Is(err,
		ErrRepairDeadlineExhausted) {
		t.Fatalf("past the deadline the budget said %v", err)
	}
	// And it stays begun: a later call must not move the start forward.
	o.BeginRepair(now.Add(time.Hour))
	if err := o.OverBudget(now.Add(2 * time.Minute)); !errors.Is(err,
		ErrRepairDeadlineExhausted) {
		t.Fatal("BeginRepair moved the start of the repair phase, which would " +
			"let a transfer renew its own deadline")
	}
}

// Progress may buy more ROUNDS. It may never buy back frames or time.
func TestFreshProgressResetsOnlyTheFutilityCounter(t *testing.T) {
	key := testKey(t)
	o, err := NewOutbound(bytes.Repeat([]byte("p"), 900), 200, Limits{
		Window: 4, MaxRounds: 2,
		Repair: RepairBudget{MaxFrames: 3, MaxDuration: time.Minute}}, key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	for i := range 4 {
		o.MarkSubmitted(i)
	}
	o.BeginRepair(now)
	o.ChargeRepairFrame(100)
	o.ChargeRepairFrame(100)

	// A SACK arrives and the window advances: rounds are forgiven.
	bitmap := make([]byte, 1)
	for i := range 4 {
		SetBit(bitmap, i)
	}
	o.NoteSACK(&Frame{Kind: KindSACK, Transfer: o.ID, Count: uint64(o.Count()),
		Base: 0, Bitmap: bitmap})
	if !o.NextRound() {
		t.Fatal("a window that advanced should have renewed the round budget")
	}
	if o.Rounds() != 0 {
		t.Fatalf("progress left the futility counter at %d", o.Rounds())
	}

	// ...and the absolute budget is untouched by that same progress.
	if o.RepairFrames() != 2 {
		t.Fatalf("progress returned spent frames: %d", o.RepairFrames())
	}
	if !o.Repairing() {
		t.Fatal("progress un-started the repair phase")
	}
	o.ChargeRepairFrame(100)
	if err := o.OverBudget(now); !errors.Is(err, ErrRepairFrameBudgetExhausted) {
		t.Fatalf("three frames against a budget of three reported %v", err)
	}
}

// The zero of MaxAirtime means disabled; the zero of the other two means
// "derive one". Two rules in one struct, so each is pinned.
func TestABudgetIsCoherentAfterWithDefaults(t *testing.T) {
	l := Limits{Window: 16}.withDefaults()
	if err := l.check(); err != nil {
		t.Fatal(err)
	}
	if want := 2 * l.MaxRounds * l.Window; l.Repair.MaxFrames != want {
		t.Fatalf("a window of 16 derived a budget of %d frames, want %d "+
			"(twice the caller's own MaxRounds×Window patience)",
			l.Repair.MaxFrames, want)
	}
	if l.Repair.MaxDuration <= 0 {
		t.Fatal("no repair deadline was derived")
	}
	// The deadline must not be tighter than an honestly futile run, or
	// ErrGaveUp becomes unreachable and two different answers collapse.
	futile := time.Duration(l.MaxRounds) *
		(time.Duration(l.Window)*l.FrameGap + l.AckTimeout)
	if l.Repair.MaxDuration <= futile {
		t.Fatalf("the repair deadline %s is inside a futile run of %s — the "+
			"clock would preempt the round budget every time",
			l.Repair.MaxDuration, futile)
	}
	if l.Repair.MaxAirtime != 0 {
		t.Fatalf("MaxAirtime was filled in with %s; zero means disabled until "+
			"a carrier can price a frame", l.Repair.MaxAirtime)
	}

	// An explicit budget is kept as written.
	l = Limits{Repair: RepairBudget{MaxFrames: 5, MaxDuration: time.Second}}.withDefaults()
	if l.Repair.MaxFrames != 5 || l.Repair.MaxDuration != time.Second {
		t.Fatalf("an explicit budget was overwritten: %+v", l.Repair)
	}

	// A negative is a mistake and is named, not quietly repaired.
	for _, bad := range []RepairBudget{
		{MaxFrames: -1}, {MaxDuration: -time.Second}, {MaxAirtime: -time.Second},
	} {
		if err := (Limits{Repair: bad}).withDefaults().check(); err == nil {
			t.Fatalf("a negative budget %+v was accepted", bad)
		}
	}
}
