// Honest physical time: the carrier distinguishes "accepted" from
// "transmitted", and the transfer layer stops confusing the two.
//
// The mechanism under test is the AirtimeModel seam. A carrier that can price
// a frame gets three behaviours it could not have before: the sender's queue
// is bounded in AIR rather than frames, the wait for an answer starts at TX
// DRAIN rather than at enqueue, and the repair budget can be spent in the
// unit that a radio actually spends.
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

// modelAir is a carrier that knows exactly how long a frame takes, because
// the test told it: perFrame of air for a full-MTU frame, proportionally less
// for a smaller one — a SACK or COMMIT is a short frame on a real radio too,
// which matters, because an answer priced like a whole window would make the
// answer itself outlive any honest patience. A frame is DELIVERED at the
// moment the model says it finished transmitting.
type modelAir struct {
	mu       sync.Mutex
	mtu      int
	perFrame time.Duration
	estEnd   time.Time
	toPeer   chan []byte
	fromPeer chan []byte

	// maxBacklog records the worst queued-airtime observed at the moment a
	// frame was ACCEPTED — which is exactly the number MaxQueuedAirtime
	// promises to bound.
	maxBacklog time.Duration
	// dropCommits drops every CONFIRMATION — COMMIT and the tombstone's
	// complete-SACK alike — to force the endless-repair shape when asked.
	dropCommits bool
	key         *TransferKey
}

func newModelPair(mtu int, perFrame time.Duration, key *TransferKey,
	dropCommits bool) (a, b *modelAir) {
	a2b, b2a := make(chan []byte, 256), make(chan []byte, 256)
	a = &modelAir{mtu: mtu, perFrame: perFrame, toPeer: a2b, fromPeer: b2a,
		dropCommits: dropCommits, key: key}
	b = &modelAir{mtu: mtu, perFrame: perFrame, toPeer: b2a, fromPeer: a2b,
		key: key}
	return a, b
}

func (m *modelAir) MTU() int { return m.mtu }

func (m *modelAir) Send(ctx context.Context, _ RadioAddress, frame []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	if backlog := time.Until(m.estEnd); backlog > m.maxBacklog {
		m.maxBacklog = backlog
	}
	start := time.Now()
	if m.estEnd.After(start) {
		start = m.estEnd
	}
	m.estEnd = start.Add(m.frameTime(len(frame)))
	deliverAt := m.estEnd
	m.mu.Unlock()

	// Delivered when the model says the frame has FINISHED transmitting —
	// this is the whole fake: a frame handed over is not a frame heard, and
	// the gap between the two is exactly per-frame airtime times the queue.
	b := append([]byte(nil), frame...)
	time.AfterFunc(time.Until(deliverAt), func() {
		select {
		case m.toPeer <- b:
		default:
		}
	})
	return nil
}

func (m *modelAir) Receive(ctx context.Context) (RadioAddress, []byte, error) {
	for {
		select {
		case b := <-m.fromPeer:
			if m.dropCommits {
				if f, err := Decode(b, m.key); err == nil &&
					(f.Kind == KindCommit || (f.Kind == KindSACK && f.Reassembled)) {
					continue
				}
			}
			return RadioAddress("peer"), b, nil
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

func (m *modelAir) Credit() transports.Credit {
	return transports.Credit{Known: false}
}

// frameTime scales the configured per-frame cost by size, floored so a frame
// is never free.
func (m *modelAir) frameTime(n int) time.Duration {
	d := m.perFrame * time.Duration(n) / time.Duration(m.mtu)
	return max(d, 10*time.Millisecond)
}

func (m *modelAir) FrameAirtime(n int) time.Duration { return m.frameTime(n) }

func (m *modelAir) EstimatedTxEnd() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.estEnd
}

var _ AirtimeModel = (*modelAir)(nil)

func driveModel(ctx context.Context, s *Session, air *modelAir, out chan<- []byte) {
	go func() {
		for {
			rctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			_, raw, err := air.Receive(rctx)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				got, _ := s.Deliver(ctx, RadioAddress("peer"), raw)
				if got != nil && out != nil {
					select {
					case out <- got.Message:
					default:
					}
				}
			}
			s.PumpSACKs(ctx)
		}
	}()
}

// The queue bound: on a carrier that prices its frames, the sender never
// buries the modem. The measured failure queued ~30 seconds of air; this
// pins the ceiling the fix promises.
func TestQueuedAirtimeStaysBounded(t *testing.T) {
	key := testKey(t)
	const perFrame = 60 * time.Millisecond
	lim := Limits{Window: 8, AckTimeout: 500 * time.Millisecond,
		SACKDelay: 5 * time.Millisecond, SendFloor: time.Millisecond,
		FrameGap:         time.Millisecond, // deliberately far below perFrame
		MaxQueuedAirtime: 2 * perFrame}

	air, peerAir := newModelPair(200, perFrame, key, false)
	sender, err := NewSession(air, key, Options{Limits: lim})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewSession(peerAir, key, Options{Limits: lim})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	arrived := make(chan []byte, 4)
	driveModel(ctx, receiver, peerAir, arrived)
	driveModel(ctx, sender, air, nil)

	msg := bytes.Repeat([]byte("bounded air. "), 60) // several fragments
	if err := sender.Send(ctx, RadioAddress("peer"), msg); err != nil {
		t.Fatalf("the transfer failed under a queue bound: %v", err)
	}
	select {
	case got := <-arrived:
		if !bytes.Equal(got, msg) {
			t.Fatal("the message arrived changed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was delivered")
	}

	air.mu.Lock()
	worst := air.maxBacklog
	air.mu.Unlock()
	// One frame of slack over the configured bound: the check happens before
	// the offer, and the offered frame itself then joins the queue.
	if ceiling := lim.MaxQueuedAirtime + perFrame; worst > ceiling {
		t.Fatalf("the carrier's queue reached %s of air against a bound of %s "+
			"(+1 frame slack = %s) — the FrameGap of %s was outrunning the "+
			"model exactly the way the real boards were outrun",
			worst, lim.MaxQueuedAirtime, ceiling, lim.FrameGap)
	}
}

// The wait for an answer starts when the air is expected to be quiet, not
// when the bytes were handed over. Red against the pre-RD-6b code: there the
// sender's patience ran while its own modem was still radiating, so a slow
// carrier made the sender time out ON ITSELF and resend a frame the peer had
// not yet finished hearing.
//
// One fragment, on purpose. A wider window would also exercise the mid-window
// SACK reaction — a real defect, and RD-6c's, not this gate's. One fragment
// isolates precisely the claim under test: enqueue-based patience of 300 ms
// against 800 ms of queued air fails; drain-based patience does not.
func TestThePatienceForAnAnswerStartsAtTxDrain(t *testing.T) {
	key := testKey(t)
	const perFrame = 800 * time.Millisecond
	lim := Limits{Window: 4, MaxRounds: 3,
		AckTimeout: 300 * time.Millisecond,
		SACKDelay:  5 * time.Millisecond, SendFloor: time.Millisecond,
		FrameGap:         time.Millisecond,
		MaxQueuedAirtime: 4 * perFrame} // the bound must not interfere here

	air, peerAir := newModelPair(200, perFrame, key, false)
	sender, _ := NewSession(air, key, Options{Limits: lim})
	receiver, _ := NewSession(peerAir, key, Options{Limits: lim})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	arrived := make(chan []byte, 4)
	driveModel(ctx, receiver, peerAir, arrived)
	driveModel(ctx, sender, air, nil)

	msg := []byte("one frame, slow air, honest patience")
	if err := sender.Send(ctx, RadioAddress("peer"), msg); err != nil {
		t.Fatalf("a slow carrier made the sender give up on itself: %v", err)
	}
	select {
	case got := <-arrived:
		if !bytes.Equal(got, msg) {
			t.Fatal("the message arrived changed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was delivered")
	}
	// The proof is in the spend: with patience from drain, a lossless carrier
	// needs no repair at all. Enqueue-based patience resent this frame.
	if st := sender.Stats(); st.RepairDataFrames != 0 {
		t.Fatalf("a lossless carrier cost %d repair frames — the sender was "+
			"timing out on its own transmission", st.RepairDataFrames)
	}
}

// The airtime dimension of the repair budget, live end to end: when a
// carrier prices frames and the budget is on, a lost-confirmation transfer
// stops because of AIR, and says so.
func TestTheAirtimeBudgetStopsARepairAndNamesItself(t *testing.T) {
	key := testKey(t)
	const perFrame = 30 * time.Millisecond
	lim := Limits{Window: 4, MaxRounds: 50,
		AckTimeout: 40 * time.Millisecond,
		SACKDelay:  5 * time.Millisecond, SendFloor: time.Millisecond,
		FrameGap:         time.Millisecond,
		MaxQueuedAirtime: 8 * perFrame,
		Repair: RepairBudget{
			// Frames and duration far too generous to fire; only air can.
			MaxFrames:   10_000,
			MaxDuration: time.Hour,
			MaxAirtime:  5 * perFrame,
		}}

	// COMMITs never arrive, so the sender repairs a message the peer holds.
	air, peerAir := newModelPair(200, perFrame, key, true)
	sender, _ := NewSession(air, key, Options{Limits: lim})
	receiver, _ := NewSession(peerAir, key, Options{Limits: lim})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	driveModel(ctx, receiver, peerAir, nil)
	driveModel(ctx, sender, air, nil)

	err := sender.Send(ctx, RadioAddress("peer"),
		bytes.Repeat([]byte("air is the unit. "), 30))
	if !errors.Is(err, ErrDeliveryUnconfirmed) {
		t.Fatalf("got %v, want an unconfirmed delivery", err)
	}
	if !errors.Is(err, ErrRepairAirtimeBudgetExhausted) {
		t.Fatalf("got %v, want the AIRTIME budget to be the named cause", err)
	}
	if st := sender.Stats(); st.AirtimeExhausted != 1 || st.Unconfirmed != 1 {
		t.Fatalf("stats do not name the airtime budget: %+v", st)
	}
}

// A carrier that says nothing about time changes nothing: the seam is
// optional, and the old behaviour is byte-for-byte the fallback.
func TestACarrierWithoutAnAirtimeModelIsUntouched(t *testing.T) {
	key := testKey(t)
	lim := Limits{Window: 4, MaxRounds: 20,
		AckTimeout: 400 * time.Millisecond,
		SACKDelay:  10 * time.Millisecond, SendFloor: time.Millisecond,
		FrameGap: time.Millisecond,
		// An aggressive queue bound that MUST stay inert without a model.
		MaxQueuedAirtime: time.Nanosecond}

	air, peerAir := newAir(200, 0.2, 42) // the classic lossy pair, no model
	sender, _ := NewSession(air, key, Options{Limits: lim})
	receiver, _ := NewSession(peerAir, key, Options{Limits: lim})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	arrived := make(chan []byte, 8)
	drive(ctx, receiver, peerAir, arrived)
	drive(ctx, sender, air, nil)

	msg := bytes.Repeat([]byte("no model, no change. "), 40)
	if err := sender.Send(ctx, RadioAddress("peer"), msg); err != nil {
		t.Fatalf("a model-less carrier failed under the new code: %v", err)
	}
	select {
	case got := <-arrived:
		if !bytes.Equal(got, msg) {
			t.Fatal("the message arrived changed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was delivered")
	}
}
