package radiotransfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports"
)

// lossyAir is a carrier that behaves like the one this was measured on: it
// carries small frames, drops them one at a time, and never reorders more
// than the queue depth.
//
// The loss rate is a parameter because the whole question is what a given
// per-frame loss does to a whole MESSAGE, and that relationship is the reason
// this layer exists.
type lossyAir struct {
	mu       sync.Mutex
	mtu      int
	loss     float64
	rng      *rand.Rand
	toPeer   chan []byte
	fromPeer chan []byte
	sent     int
	dropped  int
	// full makes the carrier refuse every nth frame, so the pacing path is
	// exercised rather than assumed.
	full   int
	offers int
}

func newAir(mtu int, loss float64, seed uint64) (*lossyAir, *lossyAir) {
	a2b := make(chan []byte, 256)
	b2a := make(chan []byte, 256)
	a := &lossyAir{mtu: mtu, loss: loss, rng: rand.New(rand.NewPCG(seed, 1)),
		toPeer: a2b, fromPeer: b2a}
	b := &lossyAir{mtu: mtu, loss: loss, rng: rand.New(rand.NewPCG(seed, 2)),
		toPeer: b2a, fromPeer: a2b}
	return a, b
}

func (l *lossyAir) MTU() int { return l.mtu }

func (l *lossyAir) Send(ctx context.Context, _ RadioAddress, frame []byte) error {
	l.mu.Lock()
	l.offers++
	if l.full > 0 && l.offers%l.full == 0 {
		l.mu.Unlock()
		return ErrCarrierFull
	}
	if len(frame) > l.mtu {
		l.mu.Unlock()
		l.fail("a frame of %d bytes went to a carrier whose MTU is %d",
			len(frame), l.mtu)
	}
	l.sent++
	drop := l.rng.Float64() < l.loss
	if drop {
		l.dropped++
	}
	l.mu.Unlock()
	if drop {
		return nil // gone on the air, exactly as a real loss looks
	}
	select {
	case l.toPeer <- append([]byte(nil), frame...):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrCarrierFull
	}
}

func (l *lossyAir) Receive(ctx context.Context) (RadioAddress, []byte, error) {
	select {
	case b := <-l.fromPeer:
		return RadioAddress("peer"), b, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

func (l *lossyAir) Credit() transports.Credit {
	return transports.Credit{Packets: 8, Known: true, RetryAfter: time.Millisecond}
}

// fail panics rather than calling t.Fatal, because Send runs on whichever
// goroutine the session is using and t.Fatal from a non-test goroutine is
// silently ignored — which would turn a real MTU violation into a passing
// test.
func (l *lossyAir) fail(format string, args ...any) {
	panic("lossyAir: " + fmt.Sprintf(format, args...))
}

// drive runs the ONE read loop a session is entitled to.
//
// Every session needs one, including a sending one: acknowledgements for our
// own transfer arrive on the same receiver as somebody else's data, and
// Session deliberately does not read the carrier itself — two consumers of
// one Receive steal each other's frames, and the loss looks exactly like the
// carrier losing them.
func drive(ctx context.Context, s *Session, air *lossyAir, out chan<- []byte) {
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

// THE test this whole layer exists for.
//
// A carrier losing one frame in twenty delivers a ten-fragment message about
// six times in ten if nothing repairs it — and the previous behaviour
// repaired nothing at all: the fragments that DID arrive had already spent
// their airtime, and no part of the system noticed which one was missing or
// asked for it again.
func TestAMessageSurvivesACarrierThatLosesFrames(t *testing.T) {
	// A fifth of every frame lost, in both directions — worse than anything
	// measured on the real boards (1-5%), because a repair layer that only
	// works on a good day is not a repair layer.
	const loss = 0.2
	key := testKey(t)
	lim := Limits{Window: 4, MaxRounds: 20, AckTimeout: 400 * time.Millisecond,
		SACKDelay: 10 * time.Millisecond, SendFloor: time.Millisecond, FrameGap: time.Millisecond}

	air, peerAir := newAir(200, loss, 42)
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
	arrived := make(chan []byte, 8)
	drive(ctx, receiver, peerAir, arrived)
	drive(ctx, sender, air, nil)

	msg := bytes.Repeat([]byte("a quiet event travelling by radio. "), 40) // ~1.4 KiB
	if err := sender.Send(ctx, RadioAddress("peer"), msg); err != nil {
		t.Fatalf("a message did not survive %.0f%% frame loss: %v", loss*100, err)
	}
	select {
	case got := <-arrived:
		if !bytes.Equal(got, msg) {
			t.Fatal("the message arrived changed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sender reported the transfer complete and nothing was delivered")
	}
	st := sender.Stats()
	if st.CompleteTransferRate() != 1 {
		t.Fatalf("complete transfer rate %.2f", st.CompleteTransferRate())
	}
	air.mu.Lock()
	outbound := air.dropped
	air.mu.Unlock()
	peerAir.mu.Lock()
	inbound := peerAir.dropped
	peerAir.mu.Unlock()
	if outbound+inbound == 0 {
		t.Fatal("the carrier dropped nothing, so this proved nothing about loss")
	}
	t.Logf("%d frames offered, %d DATA and %d control frames lost on the air",
		st.FramesOut, outbound, inbound)
}

// Selective repeat, stated as a property rather than an intention: after a
// SACK reporting holes, the sender must offer the MISSING fragments and not
// the whole window. On a carrier where one packet costs two seconds,
// resending seven to recover one is fourteen seconds spent on six that
// already arrived.
func TestOnlyTheGapsAreResent(t *testing.T) {
	key := testKey(t)
	o, err := NewOutbound(bytes.Repeat([]byte("x"), 900), 200,
		Limits{Window: 8}, key)
	if err != nil {
		t.Fatal(err)
	}
	if o.Count() < 6 {
		t.Fatalf("the message became %d fragments; this test needs several", o.Count())
	}

	// The receiver holds everything in the first window except fragments 2
	// and 5.
	bitmap := make([]byte, 1)
	for i := range 8 {
		if i != 2 && i != 5 && i < o.Count() {
			SetBit(bitmap, i)
		}
	}
	o.NoteSACK(&Frame{Kind: KindSACK, Transfer: o.ID, Count: uint64(o.Count()),
		Base: 0, Bitmap: bitmap})

	want := []int{2, 5}
	got := o.Pending()
	if len(got) < len(want) || got[0] != 2 || got[1] != 5 {
		t.Fatalf("pending = %v, want the gaps %v first", got, want)
	}
	for _, i := range got {
		if i > 5 && i < 8 {
			t.Fatalf("fragment %d was resent although the receiver reported it "+
				"arrived", i)
		}
	}
}

// A stale SACK must not un-acknowledge anything. On a lossy carrier an old
// SACK can arrive after a new one, and resending work that already succeeded
// is airtime spent to go backwards.
func TestAStaleSACKDoesNotUndoProgress(t *testing.T) {
	key := testKey(t)
	o, err := NewOutbound(bytes.Repeat([]byte("y"), 900), 200, Limits{Window: 4}, key)
	if err != nil {
		t.Fatal(err)
	}
	full := []byte{0xFF}
	o.NoteSACK(&Frame{Kind: KindSACK, Transfer: o.ID, Count: uint64(o.Count()),
		Base: 0, Bitmap: full})
	before := o.base()
	if before == 0 {
		t.Fatal("a full SACK acknowledged nothing")
	}
	// The same window again, this time reporting nothing arrived.
	o.NoteSACK(&Frame{Kind: KindSACK, Transfer: o.ID, Count: uint64(o.Count()),
		Base: 0, Bitmap: []byte{0}})
	if o.base() != before {
		t.Fatalf("a stale SACK moved the base back from %d to %d", before, o.base())
	}
}

// A transfer that stops making progress is evicted. Today's reassembler has
// no timeout at all, and on a broadcast segment its map is reachable by every
// neighbour.
func TestAStalledTransferIsEvicted(t *testing.T) {
	lim := Limits{ReassemblyTimeout: time.Minute}
	r := NewReceiver(lim)
	key := testKey(t)
	o, err := NewOutbound(bytes.Repeat([]byte("z"), 900), 200, lim, key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	if _, _, err := r.Accept("neighbour", o.Frame(0), now); err != nil {
		t.Fatal(err)
	}
	if n, b := r.Inflight(); n != 1 || b == 0 {
		t.Fatalf("inflight = %d transfers, %d bytes", n, b)
	}
	// Silence. Not a slow transfer — no fragment arrives at all.
	if _, _, err := r.Accept("neighbour", o.Frame(1), now.Add(2*time.Minute)); err == nil {
		// Accepting frame 1 after the timeout starts a NEW transfer under the
		// same id, which is correct; what matters is that the old buffer went.
		_ = err
	}
	if _, b := r.Inflight(); b > len(o.chunks[0])+len(o.chunks[1]) {
		t.Fatalf("a stalled transfer's bytes were still held: %d", b)
	}
}

// One neighbour must not be able to hold as much memory as they care to send.
func TestOneNeighbourCannotFillTheSegment(t *testing.T) {
	lim := Limits{MaxInflightTransfersPerPeer: 2}
	r := NewReceiver(lim)
	key := testKey(t)
	now := time.Now()
	refused := 0
	for range 5 {
		o, err := NewOutbound(bytes.Repeat([]byte("q"), 900), 200, lim, key)
		if err != nil {
			t.Fatal(err)
		}
		_, reply, err := r.Accept("loud", o.Frame(0), now)
		if err != nil {
			t.Fatal(err)
		}
		if reply != nil && reply.Kind == KindCancel && reply.Reason == ReasonBusy {
			refused++
		}
	}
	if refused == 0 {
		t.Fatal("a single peer opened five transfers unchallenged — the cap does " +
			"not hold, and on a broadcast segment that memory is reachable by " +
			"anyone in radio range")
	}
	if n, _ := r.Inflight(); n > 2 {
		t.Fatalf("%d transfers held from one peer, the cap is 2", n)
	}
}

// The digest catches what no per-fragment check can: a message assembled from
// fragments that were each individually fine.
func TestASplicedReassemblyIsRefused(t *testing.T) {
	lim := Limits{}
	key := testKey(t)
	r := NewReceiver(lim)
	now := time.Now()

	o, err := NewOutbound(bytes.Repeat([]byte("the message that was actually sent. "), 20),
		120, lim, key)
	if err != nil {
		t.Fatal(err)
	}
	if o.Count() < 2 {
		t.Fatal("this test needs a message of several fragments")
	}
	// Every fragment but the last, then a last fragment carrying somebody
	// else's bytes under the same transfer, same count, same digest.
	for i := range o.Count() - 1 {
		if _, _, err := r.Accept("peer", o.Frame(i), now); err != nil {
			t.Fatal(err)
		}
	}
	spliced := o.Frame(o.Count() - 1)
	spliced.Chunk = bytes.Repeat([]byte("!"), len(spliced.Chunk))
	got, reply, err := r.Accept("peer", spliced, now)
	if err == nil || got != nil {
		t.Fatal("a spliced reassembly was delivered as the message")
	}
	if reply == nil || reply.Kind != KindCancel || reply.Reason != ReasonDigest {
		t.Fatalf("the sender was not told why: %+v", reply)
	}
}

// A re-delivery must not surface the same message twice, and must not be met
// with silence either — the sender is retrying because it did not hear the
// first COMMIT, and silence is what made it retry.
func TestARedeliveryIsAnsweredButNotDeliveredTwice(t *testing.T) {
	lim := Limits{}
	key := testKey(t)
	r := NewReceiver(lim)
	now := time.Now()
	o, err := NewOutbound([]byte("short enough for one fragment"), 200, lim, key)
	if err != nil {
		t.Fatal(err)
	}
	got, reply, err := r.Accept("peer", o.Frame(0), now)
	if err != nil || got == nil || reply.Kind != KindCommit {
		t.Fatalf("first delivery: %v / %v / %+v", got, err, reply)
	}
	got2, reply2, err2 := r.Accept("peer", o.Frame(0), now)
	if !errors.Is(err2, ErrAlreadyDelivered) {
		t.Fatalf("a re-delivery reported %v", err2)
	}
	if got2 != nil {
		t.Fatal("the same message was delivered upward twice")
	}
	if reply2 == nil || reply2.Kind != KindCommit {
		t.Fatal("a re-delivery was met with silence — which is exactly what " +
			"made the sender retry in the first place")
	}
}

// A carrier that says it is full must not lose a frame: the sender waits and
// offers the same frame again.
func TestAFullCarrierDelaysAFrameRatherThanDroppingIt(t *testing.T) {
	key := testKey(t)
	lim := Limits{Window: 4, AckTimeout: 2 * time.Second,
		SACKDelay: 10 * time.Millisecond, SendFloor: time.Millisecond, FrameGap: time.Millisecond}
	air, peerAir := newAir(200, 0, 7)
	air.full = 3 // every third offer is refused

	sender, _ := NewSession(air, key, Options{Limits: lim})
	receiver, _ := NewSession(peerAir, key, Options{Limits: lim})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	arrived := make(chan []byte, 4)
	drive(ctx, receiver, peerAir, arrived)
	drive(ctx, sender, air, nil)

	msg := bytes.Repeat([]byte("paced. "), 100)
	if err := sender.Send(ctx, RadioAddress("peer"), msg); err != nil {
		t.Fatalf("a busy carrier lost the message: %v", err)
	}
	select {
	case got := <-arrived:
		if !bytes.Equal(got, msg) {
			t.Fatal("the message arrived changed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was delivered")
	}
	if sender.Stats().Refused == 0 {
		t.Fatal("the carrier never refused, so the pacing path was not exercised")
	}
}

// A peer that stops answering ends the transfer, and says so. "Handed to the
// transport" being mistaken for delivery is the failure this whole project
// keeps guarding against.
func TestAPeerThatStopsAnsweringIsReportedAsSuch(t *testing.T) {
	key := testKey(t)
	lim := Limits{Window: 4, MaxRounds: 2, AckTimeout: 50 * time.Millisecond,
		SendFloor: time.Millisecond, FrameGap: time.Millisecond}
	air, _ := newAir(200, 0, 3) // nobody is listening on the other side

	sender, _ := NewSession(air, key, Options{Limits: lim})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err := sender.Send(ctx, RadioAddress("peer"), bytes.Repeat([]byte("?"), 600))
	if !errors.Is(err, ErrGaveUp) {
		t.Fatalf("a silent peer produced %v, want ErrGaveUp", err)
	}
	if st := sender.Stats(); st.Completed != 0 || st.GaveUp != 1 {
		t.Fatalf("stats claim something completed: %+v", st)
	}
}

// A message too large to finish is refused BEFORE any airtime is spent.
// Starting a transfer that cannot complete is the worst of both.
func TestAnImpossibleMessageIsRefusedBeforeItCostsAirtime(t *testing.T) {
	key := testKey(t)
	lim := Limits{MaxFragmentsPerTransfer: 4}
	_, err := NewOutbound(bytes.Repeat([]byte("~"), 4000), 200, lim, key)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a message needing far more than four fragments produced %v", err)
	}
}

// Every fragment must fit the carrier. A header whose size depends on the
// values inside it is exactly the kind of thing that is right until a message
// crosses a size boundary.
func TestNoFragmentEverExceedsTheCarrierMTU(t *testing.T) {
	key := testKey(t)
	for _, mtu := range []int{64, 65, 100, 200, 251, 255, 256, 512} {
		for _, size := range []int{1, 63, 64, 255, 256, 1000, 5000} {
			o, err := NewOutbound(bytes.Repeat([]byte("m"), size), mtu,
				Limits{MaxFragmentsPerTransfer: MaxFragments}, key)
			if err != nil {
				continue // refused, which is a legitimate answer
			}
			for i := range o.Count() {
				b, err := o.Frame(i).Encode(key)
				if err != nil {
					t.Fatal(err)
				}
				if len(b) > mtu {
					t.Fatalf("mtu %d, message %d bytes: fragment %d encoded to %d",
						mtu, size, i, len(b))
				}
			}
		}
	}
}
