package crypto

import (
	"bytes"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func devicePair(t *testing.T) *identity.Device {
	t.Helper()
	d, err := identity.NewDevice(identity.NewRand())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestWrapUnwrapRoundTrip(t *testing.T) {
	term := id.TerminalID{0xE0}
	member := devicePair(t)
	outsider := devicePair(t)

	key, err := NewEpochKey(1)
	if err != nil {
		t.Fatal(err)
	}
	wraps, err := WrapEpoch(term, key, map[id.DeviceID][32]byte{member.ID: member.X25519Pub})
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnwrapEpoch(term, 1, wraps, member.ID, member.X25519Priv())
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != key.Key {
		t.Fatal("unwrapped key differs")
	}
	// A non-addressed device fails closed.
	if _, err := UnwrapEpoch(term, 1, wraps, outsider.ID, outsider.X25519Priv()); err == nil {
		t.Fatal("outsider unwrapped the epoch")
	}
	// The right device id with the wrong scalar fails too.
	if _, err := UnwrapEpoch(term, 1, wraps, member.ID, outsider.X25519Priv()); err == nil {
		t.Fatal("wrong scalar unwrapped the epoch")
	}
	// A wrap replayed into another terminal's context fails (info binding).
	otherTerm := id.TerminalID{0xE1}
	if _, err := UnwrapEpoch(otherTerm, 1, wraps, member.ID, member.X25519Priv()); err == nil {
		t.Fatal("wrap replayed across terminals")
	}
}

func TestSealOpenPayload(t *testing.T) {
	term := id.TerminalID{0xE2}
	key, _ := NewEpochKey(3)
	plaintext := []byte("the quiet places stay quiet")

	sealed, err := SealPayload(key, term, "message.text.v1", plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, plaintext) {
		t.Fatal("plaintext visible in sealed payload")
	}
	n, err := SealedEpoch(sealed)
	if err != nil || n != 3 {
		t.Fatalf("sealed epoch: %d %v", n, err)
	}
	got, err := OpenPayload(key, term, "message.text.v1", sealed)
	if err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("open: %q %v", got, err)
	}

	// AAD binding: wrong schema, wrong terminal, wrong epoch all fail.
	if _, err := OpenPayload(key, term, "card.created.v1", sealed); err == nil {
		t.Fatal("schema swap accepted")
	}
	if _, err := OpenPayload(key, id.TerminalID{0xFF}, "message.text.v1", sealed); err == nil {
		t.Fatal("terminal swap accepted")
	}
	otherKey, _ := NewEpochKey(3)
	if _, err := OpenPayload(otherKey, term, "message.text.v1", sealed); err == nil {
		t.Fatal("wrong key accepted")
	}
	// Tampering any byte breaks the AEAD.
	mut := bytes.Clone(sealed)
	mut[len(mut)-1] ^= 1
	if _, err := OpenPayload(key, term, "message.text.v1", mut); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}
