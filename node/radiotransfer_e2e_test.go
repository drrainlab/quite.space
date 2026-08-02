package node

import (
	"context"
	"crypto/sha256"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
)

// airLink is a two-node radio segment: small frames, one at a time, lost at a
// stated rate. It is the carrier the boards on the bench turned out to be —
// 96-99% per frame, losses scattered rather than in bursts — with the rate
// turned up so a repair layer is proved rather than merely exercised.
type airLink struct {
	mu      sync.Mutex
	mtu     int
	loss    float64
	rng     *rand.Rand
	out     chan []byte
	in      chan []byte
	dropped int
}

func newSegment(mtu int, loss float64, seed uint64) (*airLink, *airLink) {
	a2b, b2a := make(chan []byte, 512), make(chan []byte, 512)
	return &airLink{mtu: mtu, loss: loss, rng: rand.New(rand.NewPCG(seed, 1)),
			out: a2b, in: b2a},
		&airLink{mtu: mtu, loss: loss, rng: rand.New(rand.NewPCG(seed, 2)),
			out: b2a, in: a2b}
}

func (a *airLink) MTU() int { return a.mtu }

func (a *airLink) Send(ctx context.Context, _ radiotransfer.RadioAddress, frame []byte) error {
	a.mu.Lock()
	drop := a.rng.Float64() < a.loss
	if drop {
		a.dropped++
	}
	a.mu.Unlock()
	if drop {
		return nil
	}
	select {
	case a.out <- append([]byte(nil), frame...):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return radiotransfer.ErrCarrierFull
	}
}

func (a *airLink) Receive(ctx context.Context) (radiotransfer.RadioAddress, []byte, error) {
	select {
	case b := <-a.in:
		return radiotransfer.RadioAddress("segment"), b, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

func (a *airLink) Credit() transports.Credit {
	return transports.Credit{Packets: 8, Known: true, RetryAfter: 5 * time.Millisecond}
}

func (a *airLink) lost() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dropped
}

// THE end-to-end question, and it is not a protocol question.
//
// Two people, one radio segment, no LAN and no relay. Alice writes; the
// message is cut into fragments by the transfer layer, some of them are lost
// on the air, the missing ones are asked for again, and Bob READS WHAT ALICE
// WROTE. That is the sentence nine days of work could not produce, and it is
// what this test exists to hold.
func TestTwoPeopleTalkOverALossyRadioSegment(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	// The segment seed: every radio in a segment derives the same
	// frame-authentication key from it. A peer holding a different one is not
	// a peer, which is the next test down.
	seed := sha256.Sum256([]byte("a shared segment, agreed out of band"))
	key, err := radiotransfer.DeriveTransferKey(seed[:], radiotransfer.KDFVersion)
	if err != nil {
		t.Fatal(err)
	}

	// A fifth of every frame lost, both ways — worse than the boards measured.
	aAir, bAir := newSegment(200, 0.2, 99)
	lim := radiotransfer.Limits{Window: 4, MaxRounds: 30,
		AckTimeout: 300 * time.Millisecond, SACKDelay: 10 * time.Millisecond,
		SendFloor: 5 * time.Millisecond}

	aEP, err := radiotransfer.Wrap(aAir, key, radiotransfer.EndpointOptions{
		Options: radiotransfer.Options{Limits: lim}})
	if err != nil {
		t.Fatal(err)
	}
	defer aEP.Close()
	bEP, err := radiotransfer.Wrap(bAir, key, radiotransfer.EndpointOptions{
		Options: radiotransfer.Options{Limits: lim}})
	if err != nil {
		t.Fatal(err)
	}
	defer bEP.Close()

	// A space they both belong to, joined the ordinary way, then carried by
	// the radio and nothing else.
	tid := shareASpace(t, alice, bob, "over the air")

	alice.adoptLink(endpointLink{aEP}, 50*time.Millisecond, time.Hour, "radio")
	bob.adoptLink(endpointLink{bEP}, 50*time.Millisecond, time.Hour, "radio")

	const said = "the antenna is on the windowsill and the kettle is on"
	if _, err := alice.Say(tid, said, SayOptions{}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if heard(t, bob, tid, said) {
			st := aEP.Stats()
			t.Logf("delivered over a segment losing %d frames outbound and %d "+
				"inbound; %d transfers attempted, %d completed, %d frames offered",
				aAir.lost(), bAir.lost(), st.Attempted, st.Completed, st.FramesOut)
			if aAir.lost()+bAir.lost() == 0 {
				t.Fatal("nothing was lost on the air, so this proved nothing about " +
					"repair")
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	st := aEP.Stats()
	waiting, dropped, failed := aEP.Queued()
	t.Fatalf("bob never read what alice wrote. transfers: %d attempted, %d "+
		"completed, %d gave up; queue %d waiting, %d refused, %d failed",
		st.Attempted, st.Completed, st.GaveUp, waiting, dropped, failed)
}

// A radio on the same channel with a DIFFERENT segment seed is not a peer.
//
// This is what the frame key buys: without it, anyone within radio range
// could forge a CANCEL and stop every transfer on the segment at will. On
// Meshtastic the channel PSK masks that today; on RNode nothing would.
func TestARadioWithAnotherSeedIsNotAPeer(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	stranger := openRuntime(t, t.TempDir(), "stranger")
	defer stranger.Close()

	ours := sha256.Sum256([]byte("our segment, and only ours"))
	theirs := sha256.Sum256([]byte("somebody else's segment entirely"))
	ourKey, _ := radiotransfer.DeriveTransferKey(ours[:], radiotransfer.KDFVersion)
	theirKey, _ := radiotransfer.DeriveTransferKey(theirs[:], radiotransfer.KDFVersion)

	aAir, bAir := newSegment(200, 0, 5) // a perfect carrier: only the key differs
	lim := radiotransfer.Limits{Window: 4, MaxRounds: 2,
		AckTimeout: 100 * time.Millisecond, SACKDelay: 5 * time.Millisecond,
		SendFloor: 5 * time.Millisecond}

	aEP, _ := radiotransfer.Wrap(aAir, ourKey, radiotransfer.EndpointOptions{
		Options: radiotransfer.Options{Limits: lim}})
	defer aEP.Close()
	bEP, _ := radiotransfer.Wrap(bAir, theirKey, radiotransfer.EndpointOptions{
		Options: radiotransfer.Options{Limits: lim}})
	defer bEP.Close()

	tid := shareASpace(t, alice, stranger, "not for strangers")
	alice.adoptLink(endpointLink{aEP}, 50*time.Millisecond, time.Hour, "radio")
	stranger.adoptLink(endpointLink{bEP}, 50*time.Millisecond, time.Hour, "radio")

	const said = "this must not cross"
	if _, err := alice.Say(tid, said, SayOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	if heard(t, stranger, tid, said) {
		t.Fatal("a radio holding a different segment seed reassembled the " +
			"message: the frame key is not doing its job, and anyone in range " +
			"could forge control frames")
	}
}

// shareASpace makes a space both runtimes belong to, the ordinary way.
func shareASpace(t *testing.T, host, guest *Runtime, title string) id.TerminalID {
	t.Helper()
	tid, err := host.CreateSpace(title)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := host.MintInvite(tid, guest.Device.ID, guest.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guest.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	return tid
}

// heard reports whether a runtime can project the given text.
func heard(t *testing.T, rt *Runtime, tid id.TerminalID, want string) bool {
	t.Helper()
	for _, got := range bobEntries(t, rt, tid) {
		if got == want {
			return true
		}
	}
	return false
}

// endpointLink adapts a bare Endpoint to the runtime's link interface. The
// radio here never goes away, so liveness is a constant — which is what makes
// this a test of the transfer layer rather than of reconnection.
type endpointLink struct{ *radiotransfer.Endpoint }

func (endpointLink) Closed() (bool, error) { return false, nil }
