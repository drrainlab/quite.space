// The segment travelling inside an invitation, and the three refusals.
package node

import (
	"bytes"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/quicklink"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
	"github.com/drrainlab/quiet_places/transports/rnode"
)

func segFor(seed []byte) quicklink.RadioSegment {
	return quicklink.RadioSegment{
		KDFVersion: uint64(radiotransfer.KDFVersion),
		Carrier:    carrierRNode, Profile: rnode.ProfileLongFastRU, Seed: seed,
	}
}

func seedOf(t *testing.T, phrase string) []byte {
	t.Helper()
	s, err := radiotransfer.SeedFromPhrase(phrase)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// THE PAYOFF, stated as a test: a device that was given a segment can bring a
// radio up without being asked for anything.
//
// The person invited never had the words. Requiring them to go and get them
// would put a conversation with the inviter in the middle of the path — at the
// exact moment the reason for having a radio is that no such conversation is
// possible.
func TestAGivenSegmentMeansNobodyIsAskedForAPhrase(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()

	if rt.KnownSegment() {
		t.Fatal("a fresh node claimed to know a segment")
	}
	// Attaching with no phrase and no segment must REFUSE, not derive from "".
	if err := rt.AttachRNode("/dev/null", ""); err == nil {
		t.Fatal("attached with no phrase and no known segment")
	} else if !strings.Contains(err.Error(), "knows no radio segment") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}

	want := seedOf(t, "one segment from the invitation")
	if err := rt.AdoptSegment(segFor(want)); err != nil {
		t.Fatal(err)
	}
	if !rt.KnownSegment() {
		t.Fatal("the segment arrived and was not kept")
	}
	rt.mu.Lock()
	got := append([]byte(nil), rt.ks.Radio.Seed...)
	dev := rt.ks.Radio.Device
	rt.mu.Unlock()
	if !bytes.Equal(got, want) {
		t.Fatal("the stored seed is not the one that arrived")
	}
	// And no device is claimed. A segment is known long before a board is
	// plugged in — often on a machine with no radio at all — and that gap is
	// exactly the value.
	if dev != "" {
		t.Fatalf("adopting a segment claimed a device: %q", dev)
	}
	// It also survives a restart, or the invitation would have to be re-opened
	// at the worst possible time.
	rt.mu.Lock()
	rec := rt.ks.Radio
	rt.mu.Unlock()
	if len(rec.Seed) == 0 {
		t.Fatal("the segment is not in the keystore")
	}
}

// Adopting the same segment twice is quiet. A person may open the same link
// again, or two links from the same inviter; neither is an event.
func TestAdoptingTheSameSegmentTwiceIsNotAnError(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()

	seg := segFor(seedOf(t, "one segment from the invitation"))
	if err := rt.AdoptSegment(seg); err != nil {
		t.Fatal(err)
	}
	if err := rt.AdoptSegment(seg); err != nil {
		t.Fatalf("the same segment a second time was refused: %v", err)
	}
}

// A SECOND, DIFFERENT segment is refused rather than adopted.
//
// The alternative is the quietest possible disaster: opening somebody's link
// re-points a radio that was working at air the people you already share it
// with are not on, and the only symptom is that nothing arrives.
func TestASecondDifferentSegmentIsRefusedRatherThanSwallowingTheFirst(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()

	first := seedOf(t, "one segment from the invitation")
	if err := rt.AdoptSegment(segFor(first)); err != nil {
		t.Fatal(err)
	}
	err := rt.AdoptSegment(segFor(seedOf(t, "an entirely different segment")))
	if err == nil {
		t.Fatal("a second segment silently replaced the first")
	}
	if !strings.Contains(err.Error(), "already on a different radio segment") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
	rt.mu.Lock()
	still := append([]byte(nil), rt.ks.Radio.Seed...)
	rt.mu.Unlock()
	if !bytes.Equal(still, first) {
		t.Fatal("the refusal still moved the seed")
	}
}

// A segment this build cannot speak is refused with a reason, never adopted
// half-way. Each of these lands as a radio that hears nobody if it slips past.
func TestASegmentThisBuildCannotSpeakIsRefusedWithAReason(t *testing.T) {
	seed := seedOf(t, "one segment from the invitation")
	for name, seg := range map[string]quicklink.RadioSegment{
		"another carrier": {KDFVersion: uint64(radiotransfer.KDFVersion),
			Carrier: "meshtastic", Profile: rnode.ProfileLongFastRU, Seed: seed},
		"a profile this build never heard of": {KDFVersion: uint64(radiotransfer.KDFVersion),
			Carrier: carrierRNode, Profile: "some-future-air", Seed: seed},
		"another key derivation": {KDFVersion: uint64(radiotransfer.KDFVersion) + 1,
			Carrier: carrierRNode, Profile: rnode.ProfileLongFastRU, Seed: seed},
	} {
		rt := openRuntime(t, t.TempDir(), "bob")
		if err := rt.AdoptSegment(seg); err == nil {
			t.Errorf("%s: adopted", name)
		}
		if rt.KnownSegment() {
			t.Errorf("%s: refused, and kept it anyway", name)
		}
		rt.Close()
	}
}

// An invitation from a node with no radio carries no segment — which is
// exactly today's invitation, and must stay ordinary rather than becoming a
// special case anybody has to think about.
func TestANodeWithNoRadioCarriesNoSegment(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	if seg := rt.SegmentDescriptor(); seg.Present() {
		t.Fatalf("a node with no radio offered a segment: %+v", seg)
	}
}

// And a node that HAS one offers a descriptor that survives its own
// validation — the check the receiving side will run.
func TestTheOfferedDescriptorIsOneAPeerWouldAccept(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()

	seed := seedOf(t, "one segment from the invitation")
	if err := alice.AdoptSegment(segFor(seed)); err != nil {
		t.Fatal(err)
	}
	seg := alice.SegmentDescriptor()
	if !seg.Present() {
		t.Fatal("a node holding a segment offered none")
	}
	if err := seg.Validate(); err != nil {
		t.Fatalf("the descriptor we hand out fails the check the other side runs: %v", err)
	}
	// The receiving side, for real.
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	if err := bob.AdoptSegment(seg); err != nil {
		t.Fatalf("a peer refused the descriptor we mint: %v", err)
	}
	bob.mu.Lock()
	got := append([]byte(nil), bob.ks.Radio.Seed...)
	bob.mu.Unlock()
	if !bytes.Equal(got, seed) {
		t.Fatal("the seed did not survive the trip between two nodes")
	}
}
