package bridge

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// golden bytes for key 8, the field that used to be a lapsed flag and is
// now a kind. Byte-level so a refactor cannot quietly renumber it: key 8
// followed by the value, both as small unsigned ints in the deterministic
// subset.
func kindBytes(v uint64) []byte {
	b := codec.AppendUint(nil, 8)
	return codec.AppendUint(b, v)
}

// Kind replaced a boolean in place. 0 and 1 must still be on the wire
// exactly where and how the flag was, or every receipt written before this
// change becomes unreadable — including ones sitting in a gateway's pending
// queue across the upgrade.
func TestReceiptKindIsWireCompatibleWithTheOldFlag(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0xC1}, ed25519.SeedSize))
	base := func(k ReceiptKind) []byte {
		r := &CustodyReceipt{
			FrameIDs:   []id.EventID{{1}},
			StoreID:    "store",
			AcceptedAt: 1000,
			ExpiresAt:  2000,
			Instance:   "gw",
			Kind:       k,
		}
		return r.Sign(priv)
	}

	accepted := base(ReceiptAccepted)
	if !bytes.Contains(accepted, kindBytes(0)) {
		t.Fatal("accepted no longer encodes as key 8 = 0: receipts written " +
			"before the enum existed can no longer be read")
	}
	lapsed := base(ReceiptLapsed)
	if !bytes.Contains(lapsed, kindBytes(1)) {
		t.Fatal("lapsed no longer encodes as key 8 = 1")
	}
	expired := base(ReceiptExpired)
	if !bytes.Contains(expired, kindBytes(2)) {
		t.Fatal("expired is not the new value 2")
	}

	// Round-trip through the real decoder.
	for _, tc := range []struct {
		raw  []byte
		want ReceiptKind
	}{{accepted, ReceiptAccepted}, {lapsed, ReceiptLapsed}, {expired, ReceiptExpired}} {
		got, err := DecodeReceipt(tc.raw)
		if err != nil {
			t.Fatalf("kind %v: %v", tc.want, err)
		}
		if got.Kind != tc.want {
			t.Fatalf("kind round-trip: got %v want %v", got.Kind, tc.want)
		}
	}
}

// A kind this build does not know must NEVER read as accepted. Decoding an
// unrecognised value to zero is the classic way a future "revoked" or
// "transferred" receipt would be treated as a promise of custody by an old
// node — the one misreading that loses a message while looking healthy.
func TestUnknownReceiptKindDoesNotDecodeAsAccepted(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0xC2}, ed25519.SeedSize))
	r := &CustodyReceipt{
		FrameIDs:   []id.EventID{{2}},
		StoreID:    "store",
		AcceptedAt: 1000,
		ExpiresAt:  2000,
		Instance:   "gw",
		Kind:       ReceiptKind(200), // from some later version
	}
	raw := r.Sign(priv)
	got, err := DecodeReceipt(raw)
	if err != nil {
		t.Fatalf("an unknown kind must still decode and verify: %v", err)
	}
	if got.Kind == ReceiptAccepted {
		t.Fatal("an unrecognised kind decoded as accepted — a future receipt " +
			"type would be read as a promise of custody")
	}
	if !got.Kind.Lapsed() {
		t.Fatal("an unrecognised kind must not be treated as live custody: " +
			"anything that is not explicitly accepted has to fail closed")
	}
}

// The fields added in RB-1 are optional on the wire and absent-by-default,
// so a receipt that predates them still decodes — and is distinguishable
// from one that carries them, which is what lets a node refuse to release
// responsibility on a receipt that names no attempt.
func TestPreRB1ReceiptDecodesWithNoAttempt(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0xC3}, ed25519.SeedSize))
	old := &CustodyReceipt{
		FrameIDs:   []id.EventID{{3}},
		StoreID:    "store",
		AcceptedAt: 1000,
		ExpiresAt:  2000,
		Instance:   "gw",
		Kind:       ReceiptAccepted,
	}
	raw := old.Sign(priv)
	got, err := DecodeReceipt(raw)
	if err != nil {
		t.Fatalf("a receipt without the RB-1 fields must still verify: %v", err)
	}
	if len(got.Attempt) != 0 || got.Lease != "" || got.IngressLink != "" || got.LoopDomain != "" {
		t.Fatalf("absent fields decoded as present: %+v", got)
	}

	withFields := &CustodyReceipt{
		FrameIDs:    []id.EventID{{3}},
		StoreID:     "store",
		AcceptedAt:  1000,
		ExpiresAt:   2000,
		Instance:    "gw",
		Kind:        ReceiptAccepted,
		Attempt:     []byte("attempt-token-16"),
		Lease:       "lease-id",
		IngressLink: "mesh:test",
		LoopDomain:  "mesh-dom",
	}
	full, err := DecodeReceipt(withFields.Sign(priv))
	if err != nil {
		t.Fatal(err)
	}
	if string(full.Attempt) != "attempt-token-16" || full.Lease != "lease-id" ||
		full.IngressLink != "mesh:test" || full.LoopDomain != "mesh-dom" {
		t.Fatalf("the RB-1 fields did not round-trip: %+v", full)
	}
	// The signature covers them: flipping a byte in the attempt breaks it.
	tampered := withFields.Sign(priv)
	i := bytes.Index(tampered, []byte("attempt-token-16"))
	if i < 0 {
		t.Fatal("attempt token not found in the signed encoding")
	}
	tampered[i] ^= 0xFF
	if _, err := DecodeReceipt(tampered); err == nil {
		t.Fatal("the attempt token is outside the signature: a gateway could " +
			"be made to answer for an attempt it never saw")
	}
}
