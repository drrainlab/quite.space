// What must not break quietly about attaching a radio from the interface.
//
// The live gate covers what hardware can prove: two boards attached from the
// browser find each other, and a restart brings the radio back on its own.
// These cover what hardware CANNOT prove, because the failure would be
// silence — a node confidently alone on a segment of one.
package node

import (
	"bytes"
	"errors"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
)

// The one that matters most, and the reason the derivation was moved.
//
// The CLI hashed the phrase inline. Adding an attach path in the interface was
// about to hash it a second time, and two derivations of one secret fail in
// the worst possible way: both sides believe they typed the same words, every
// frame fails its MAC, and NOTHING anywhere says why. There is no error to
// read — a segment of one looks exactly like a segment where nobody is home.
func TestTheSamePhraseAlwaysDerivesTheSameSegment(t *testing.T) {
	const phrase = "one segment from the invitation"

	a, err := radiotransfer.SeedFromPhrase(phrase)
	if err != nil {
		t.Fatal(err)
	}
	b, err := radiotransfer.SeedFromPhrase(phrase)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("the same phrase derived two different seeds")
	}
	// And the keys they produce must be interchangeable, which is the claim
	// the seed exists to make.
	ka, err := radiotransfer.DeriveTransferKey(a, radiotransfer.KDFVersion)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := radiotransfer.DeriveTransferKey(b, radiotransfer.KDFVersion)
	if err != nil {
		t.Fatal(err)
	}
	// Interchangeable is the actual claim, so assert the actual claim: a frame
	// tagged by one radio verifies on the other. Comparing key bytes would
	// test a weaker thing through a private field.
	body := []byte("a frame on the segment")
	if !kb.Verify(body, ka.Tag(body)) {
		t.Fatal("one phrase, two frame-authentication keys — the radios would " +
			"hear each other and verify nothing, with no error anywhere")
	}
	// A different phrase is a different segment. Stated as a test because the
	// day this stops being true, the symptom is strangers on your air.
	other, err := radiotransfer.SeedFromPhrase(phrase + " but not really")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, other) {
		t.Fatal("two different phrases collapsed onto one segment")
	}
}

// A phrase too short to be a secret is refused, and refused on the PHRASE.
//
// Checking after hashing would check nothing at all: the digest is 32 bytes
// whatever went in, so a one-letter phrase would sail through and derive a key
// that looks exactly as strong as a real one.
func TestAShortPhraseIsRefusedBeforeItBecomesASeed(t *testing.T) {
	if _, err := radiotransfer.SeedFromPhrase("short"); err == nil {
		t.Fatal("a five-character phrase was accepted as a segment secret")
	} else if !errors.Is(err, radiotransfer.ErrSeedTooShort) {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
}

// Attaching stores the SEED, never the words.
//
// Nothing needs the phrase again — the key derives from the seed — and a
// phrase is the one form of this secret somebody is likely to have reused
// somewhere that matters more than a radio.
func TestTheStoredAttachmentKeepsNoPhrase(t *testing.T) {
	const phrase = "one segment from the invitation"
	seed, err := radiotransfer.SeedFromPhrase(phrase)
	if err != nil {
		t.Fatal(err)
	}
	rec := storage.RadioRecord{
		Carrier: carrierRNode, Device: "/dev/null", Seed: seed,
	}
	if !rec.Attached() {
		t.Fatal("a complete record did not read as attached")
	}
	if bytes.Contains(rec.Seed, []byte(phrase)) {
		t.Fatal("the phrase itself is in the stored record")
	}
	// And a record missing any part of itself is NOT an attachment. A
	// half-record would send Open at a device with no key, every start.
	for _, partial := range []storage.RadioRecord{
		{Carrier: carrierRNode, Device: "/dev/null"},
		{Carrier: carrierRNode, Seed: seed},
		{Device: "/dev/null", Seed: seed},
	} {
		if partial.Attached() {
			t.Fatalf("an incomplete record read as attached: %+v", partial)
		}
	}
}

// Detaching a node that has no radio says so, rather than reporting success.
func TestDetachingNothingIsRefusedRatherThanReportedAsDone(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	if err := rt.DetachRadio(); err == nil {
		t.Fatal("detaching a node with no radio reported success")
	}
}

// A remembered radio that is not there must not stop a person opening their
// own data — and must not be forgotten either.
//
// These boards enumerate under a different serial path after a reset, and a
// radio that is simply unplugged is an ordinary Tuesday. Both would otherwise
// arrive as a failure to open a keystore, which is the worst possible framing
// of "the cable is out".
func TestARememberedRadioThatIsNotThereStillLetsTheNodeOpen(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")

	seed, err := radiotransfer.SeedFromPhrase("one segment from the invitation")
	if err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.ks.Radio = storage.RadioRecord{
		Carrier: carrierRNode,
		// A path that cannot be a serial radio, so the attach fails the way an
		// unplugged board does.
		Device: "/dev/definitely-not-a-radio",
		Seed:   seed,
	}
	rt.mu.Unlock()
	if err := rt.saveKeystore(); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	// The headline: Open SUCCEEDS.
	again := openRuntime(t, dir, "alice")
	defer again.Close()

	st := again.RadioState()
	if st.Connected {
		t.Fatal("a device that cannot exist reported itself connected")
	}
	if st.Carrier != carrierRNode {
		t.Fatalf("the remembered radio was not named: carrier %q", st.Carrier)
	}
	if st.Err == "" {
		t.Fatal("the radio did not come back and the status gives no reason — " +
			"unplugged and never-configured are indistinguishable")
	}
	// And it is still remembered, so plugging the cable back in is enough.
	again.mu.Lock()
	still := again.ks.Radio.Attached()
	again.mu.Unlock()
	if !still {
		t.Fatal("one failed attach erased the attachment")
	}
}

// With nothing attached, no carrier is named.
//
// The status used to answer "meshtastic" on a node with no radio at all — a
// driver that is not present, in a field that reads as a fact, on the screen
// whose whole job is reporting facts.
func TestNoRadioNamesNoCarrier(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	if st := rt.RadioState(); st.Carrier != "" {
		t.Fatalf("nothing is attached, yet the status named %q", st.Carrier)
	}
}
