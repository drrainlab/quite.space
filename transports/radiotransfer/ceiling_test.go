// The ceiling is DERIVED, and the arithmetic is checked against the boards.
package radiotransfer

import (
	"testing"
	"time"
)

// What twenty seconds of RU long-fast air actually buys.
//
// The numbers on the right are measured, on the two Heltec v3 the project
// runs, from node/radiocarry_measure_test.go. A ceiling that admitted the
// image or refused the message would be wrong in a way a person feels: the
// first jams a channel for six minutes, the second breaks a conversation.
func TestTheCeilingAdmitsAConversationAndRefusesAPhoto(t *testing.T) {
	// 500-byte frames at ~3.9 s each — the RU long-fast profile, measured.
	const mtu = 500
	perFrame := 3900 * time.Millisecond

	key, err := DeriveTransferKey(make([]byte, MinSeedLen), KDFVersion)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := newModelPair(mtu, perFrame, key, false)
	ep, err := Wrap(a, key, EndpointOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()

	c := ep.Capabilities()
	if c.MaxEventBytes <= 0 {
		t.Fatal("a carrier that can price its own air declared no ceiling")
	}
	if !c.BlobsRefused {
		t.Fatal("the radio agreed to serve asset blobs")
	}
	t.Logf("ceiling on this profile: %d bytes (%v per frame, %v budget)",
		c.MaxEventBytes, perFrame, EventAirtimeBudget)

	for _, s := range []struct {
		what string
		size int
		want bool
	}{
		{"a short message", 340, true},
		{"a reaction", 387, true},
		{"a long message", 857, true},
		{"an image, 2 KiB preview", 2388, false},
		{"an image, 40 KiB preview", 41300, false},
	} {
		if got := c.CarriesEvent(s.size); got != s.want {
			t.Errorf("%s (%d B): carried=%v, want %v — ceiling is %d",
				s.what, s.size, got, s.want, c.MaxEventBytes)
		}
	}

	// And the ceiling must stay BELOW what the carrier can physically move,
	// or it is not a policy at all — it is a restatement of the MTU.
	if c.MaxEventBytes >= c.MaxPayload {
		t.Fatalf("the ceiling (%d) is not below what the carrier can move (%d), "+
			"so it forbids nothing", c.MaxEventBytes, c.MaxPayload)
	}
}

// A carrier that cannot price its own air gets NO ceiling.
//
// Refusing on a guess would be worse than carrying: a wrong refusal is a
// message that never goes, with no way for anyone to find out why.
func TestACarrierThatCannotPriceItsAirIsNotGivenACeiling(t *testing.T) {
	key, err := DeriveTransferKey(make([]byte, MinSeedLen), KDFVersion)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := newAir(200, 0, 1) // lossless fake, no AirtimeModel
	ep, err := Wrap(a, key, EndpointOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	if got := ep.Capabilities().MaxEventBytes; got != 0 {
		t.Fatalf("a carrier with no airtime model was given a ceiling of %d", got)
	}
}
