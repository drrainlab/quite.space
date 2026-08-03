package radiotransfer

import (
	"bytes"
	"crypto/sha256"
	"testing"
	"time"
)

// deafPair builds two endpoints where alice's frames reach bob but nothing
// comes back, so a transfer alice starts can never complete. That is what a
// radio with nobody listening actually looks like from the sender's side, and
// it is the case every deadline in this package exists to bound.
func deafPair(t *testing.T, lim Limits) (alice, bob *Endpoint, heard chan []byte) {
	t.Helper()
	seed := sha256.Sum256([]byte("one segment, one deaf direction"))
	key, err := DeriveTransferKey(seed[:], KDFVersion)
	if err != nil {
		t.Fatal(err)
	}
	aAir, bAir := newAir(200, 0, 7)
	bAir.loss = 1 // bob hears alice; alice never hears bob

	heard = make(chan []byte, 8)
	alice, err = Wrap(aAir, key, EndpointOptions{Options: Options{Limits: lim}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	bob, err = Wrap(bAir, key, EndpointOptions{
		Options: Options{Limits: lim},
		OnControl: func(_ RadioAddress, msg []byte) {
			select {
			case heard <- append([]byte(nil), msg...):
			default:
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	return alice, bob, heard
}

// The reason bob received nothing on the night this wave was written.
//
// sendLoop is serial and preferred control at SELECTION time only — but by the
// time a control message is queued, the loop is already inside a sync transfer
// and will not look at the control queue again until that transfer finishes or
// gives up. With the shipped defaults that is MaxRounds(6) x AckTimeout(45s) =
// 270 seconds. An invitation queued behind one background summary waits four
// and a half minutes before it is so much as STARTED, which is exactly the
// "still going, not failed, not delivered" the counters kept reporting.
func TestAnInviteDoesNotWaitBehindASyncTransfer(t *testing.T) {
	lim := Limits{Window: 8, MaxRounds: 6,
		AckTimeout: 2 * time.Second, SACKDelay: 10 * time.Millisecond,
		SendFloor: 5 * time.Millisecond, FrameGap: 10 * time.Millisecond}
	alice, _, heard := deafPair(t, lim)

	// A background summary, several frames long, that can never complete.
	if err := alice.Send(bytes.Repeat([]byte("sync"), 150)); err != nil {
		t.Fatal(err)
	}
	// Let it get onto the air, so this tests preemption rather than a lucky
	// ordering of the two queues.
	time.Sleep(300 * time.Millisecond)

	invite := []byte("come into the space in the car park")
	if err := alice.SendControl(invite); err != nil {
		t.Fatal(err)
	}

	// Generous against the medium, brutal against the defect: the whole
	// unpreempted give-up budget here is 6 x 2s = 12s, so anything under that
	// proves the sync transfer stepped aside.
	select {
	case got := <-heard:
		if !bytes.Equal(got, invite) {
			t.Fatalf("bob heard %q, want the invitation", got)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("the invitation never reached bob while a sync transfer was on " +
			"the air: control still queues behind the whole give-up budget")
	}
	if alice.Yielded() == 0 {
		t.Fatal("the invitation arrived but no sync transfer was recorded as " +
			"yielding — the counter and the behaviour disagree")
	}
}

// A one-frame question must not cost as much as the answer it is avoiding.
//
// A PROBE exists to discover in seconds that nobody is listening, so that a
// six-frame invitation is never committed blind. Inheriting the sync give-up
// budget would make the cheap question cost 270 seconds — more than a
// quicklink lives — and the whole point of asking first would be gone.
func TestAProbeDoesNotInheritTheSyncGiveUpBudget(t *testing.T) {
	lim := Limits{Window: 8, MaxRounds: 6,
		AckTimeout: 2 * time.Second, SACKDelay: 10 * time.Millisecond,
		SendFloor: 5 * time.Millisecond, FrameGap: 10 * time.Millisecond}
	alice, _, _ := deafPair(t, lim)

	start := time.Now()
	if err := alice.SendControlWithin([]byte("are you listening?"),
		700*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, failed := alice.Queued(); failed > 0 {
			if took := time.Since(start); took > 3*time.Second {
				t.Fatalf("the probe took %s to give up on a deaf peer; its own "+
					"deadline was 700ms", took.Round(time.Millisecond))
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a probe with a 700ms deadline had not given up after five " +
		"seconds: it inherited the sync budget, which is 12s here and 270s " +
		"with the shipped defaults")
}
