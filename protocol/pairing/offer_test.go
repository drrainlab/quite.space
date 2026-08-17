package pairing

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

func TestOfferRoundTrip(t *testing.T) {
	o, err := NewOffer(rand.Reader, "192.168.1.23:47201", 1755500000)
	if err != nil {
		t.Fatal(err)
	}
	enc := o.Encode()
	got, err := DecodeOffer(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version != o.Version || got.Addr != o.Addr || got.ExpiresAt != o.ExpiresAt {
		t.Fatalf("fields changed in flight:\n got %+v\nwant %+v", got, o)
	}
	if !bytes.Equal(got.Secret[:], o.Secret[:]) {
		t.Fatal("the secret changed in flight")
	}
}

// The offer is broadcast as SOUND, and sound is the budget: 24 payload bytes
// per 2.44-second block (audiopass.js AP.PAYLOAD). The plan's promise is a
// ~7-second loop — three blocks — for the ordinary LAN case, and this test
// is that promise measured rather than remembered.
func TestOfferFitsThreeSoundBlocksForATypicalAddress(t *testing.T) {
	o, err := NewOffer(rand.Reader, "192.168.100.200:65535", 1755500000)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(o.Encode()); n > 3*SoundBlockPayload {
		t.Fatalf("a typical offer is %d bytes — past three sound blocks (%d), "+
			"which breaks the seven-second loop", n, 3*SoundBlockPayload)
	}
}

// The hard ceiling exists because every byte is ~0.1s of tone: an offer that
// cannot fit MaxOfferBytes must be refused at MINT, where the address can
// still be fixed, not discovered by a listener who waited for a fourth block
// that never decodes.
func TestOfferRefusesAnOversizedAddressAtMint(t *testing.T) {
	long := "[fe80:0123:4567:89ab:cdef:0123:4567:89ab%enterprise-wifi-6e]:65535"
	if _, err := NewOffer(rand.Reader, long, 1755500000); err == nil {
		t.Fatal("an address that cannot fit the sound budget was accepted at mint")
	}
}

func TestOfferRejectsMalformed(t *testing.T) {
	base := func() *Offer {
		o, err := NewOffer(rand.Reader, "10.0.0.5:4400", 1755500000)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	cases := []struct {
		name  string
		mutil func(o *Offer)
	}{
		{"zero secret", func(o *Offer) { o.Secret = [32]byte{} }},
		{"empty addr", func(o *Offer) { o.Addr = "" }},
		{"no expiry", func(o *Offer) { o.ExpiresAt = 0 }},
		{"unknown version", func(o *Offer) { o.Version = 99 }},
	}
	for _, tc := range cases {
		o := base()
		tc.mutil(o)
		if _, err := DecodeOffer(o.Encode()); err == nil {
			t.Errorf("%s: decoded without complaint", tc.name)
		}
	}
	if _, err := DecodeOffer([]byte{0x01, 0x02}); err == nil {
		t.Error("garbage decoded without complaint")
	}
}

// The offer is a bearer capability with a 60-second life (threat model:
// a recording replayed later must be worthless on its own).
func TestOfferExpiry(t *testing.T) {
	o, err := NewOffer(rand.Reader, "10.0.0.5:4400", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if o.ExpiresAt != 1000+OfferTTLSeconds {
		t.Fatalf("ExpiresAt = %d, want mint+%d", o.ExpiresAt, OfferTTLSeconds)
	}
	if o.Expired(1000 + OfferTTLSeconds - 1) {
		t.Fatal("expired inside its window")
	}
	if !o.Expired(1000 + OfferTTLSeconds) {
		t.Fatal("alive past its window")
	}
}

// ADR-009: old decoders must skip unknown future fields, so the offer can
// grow (a fallback relay, a hint) without stranding shipped listeners.
func TestOfferDecoderSkipsUnknownFutureFields(t *testing.T) {
	o, err := NewOffer(rand.Reader, "10.0.0.5:4400", 1755500000)
	if err != nil {
		t.Fatal(err)
	}
	// Re-encode by hand with one extra trailing item, as a future version
	// would.
	var buf []byte
	buf = codec.AppendArray(buf, offerFields+1)
	buf = codec.AppendUint(buf, uint64(o.Version))
	buf = codec.AppendBytes(buf, o.Secret[:])
	buf = codec.AppendText(buf, o.Addr)
	buf = codec.AppendUint(buf, o.ExpiresAt)
	buf = codec.AppendText(buf, "a future field")
	got, err := DecodeOffer(buf)
	if err != nil {
		t.Fatalf("a decoder refused a future offer it could have read: %v", err)
	}
	if got.Addr != o.Addr || !bytes.Equal(got.Secret[:], o.Secret[:]) {
		t.Fatal("known fields were misread in the presence of a future one")
	}
}
