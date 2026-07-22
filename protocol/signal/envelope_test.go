package signal

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// deterministic keys for tests
func testKeys(t *testing.T, seed byte) (ed25519.PrivateKey, id.DeviceID) {
	t.Helper()
	seedBytes := bytes.Repeat([]byte{seed}, ed25519.SeedSize)
	priv := ed25519.NewKeyFromSeed(seedBytes)
	var dev id.DeviceID
	copy(dev[:], priv.Public().(ed25519.PublicKey))
	return priv, dev
}

func sampleEnvelope(t *testing.T) (*Envelope, ed25519.PrivateKey) {
	t.Helper()
	priv, dev := testKeys(t, 7)
	var term id.TerminalID
	var prin id.PrincipalID
	term[0], prin[0] = 0xAA, 0xBB
	return &Envelope{
		Terminal:        term,
		Principal:       prin,
		Device:          dev,
		Sequence:        1,
		Schema:          "message.text.v1",
		CreatedAt:       1750000000,
		LogicalClock:    42,
		ProducedBy:      AuthorshipHuman,
		PayloadEncoding: PayloadCBOR,
		Payload:         []byte{0xa1, 0x01, 0x61, 0x68}, // {1: "h"}
		Priority:        PriorityMessage,
	}, priv
}

func TestSignEncodeDecodeVerify(t *testing.T) {
	e, priv := sampleEnvelope(t)
	frame, err := e.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if got.Schema != e.Schema || got.Sequence != 1 || got.LogicalClock != 42 ||
		got.ProducedBy != AuthorshipHuman || !bytes.Equal(got.Payload, e.Payload) {
		t.Fatalf("decode mismatch: %+v", got)
	}
	if err := VerifyFrame(frame, got); err != nil {
		t.Fatal(err)
	}
}

func TestDeterministicEncoding(t *testing.T) {
	e1, priv := sampleEnvelope(t)
	e2, _ := sampleEnvelope(t)
	f1, err := e1.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := e2.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f1, f2) {
		t.Fatal("same logical envelope produced different frames")
	}
	if id.EventIDOf(f1) != id.EventIDOf(f2) {
		t.Fatal("event ids differ")
	}
}

func TestTamperDetected(t *testing.T) {
	e, priv := sampleEnvelope(t)
	frame, err := e.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	for i := range frame {
		mut := bytes.Clone(frame)
		mut[i] ^= 0x01
		got, err := Decode(mut)
		if err != nil {
			continue // structural rejection is fine
		}
		if err := VerifyFrame(mut, got); err == nil {
			t.Fatalf("bit flip at %d passed verification", i)
		}
	}
}

func TestWrongKeyRejected(t *testing.T) {
	e, priv := sampleEnvelope(t)
	frame, err := e.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(frame)
	if err != nil {
		t.Fatal(err)
	}
	otherPriv, otherDev := testKeys(t, 9)
	_ = otherPriv
	got.Device = otherDev
	if err := VerifyFrame(frame, got); err == nil {
		t.Fatal("verification passed with wrong device key")
	}
}

func TestUnknownFieldsPreservedForVerification(t *testing.T) {
	// Simulate a future node adding key 99: signature covers it; our node
	// must verify without understanding it (ADR-003/ADR-009).
	e, priv := sampleEnvelope(t)
	body, err := e.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	// Rebuild the body map with an extra unknown entry before signing.
	withExtra := injectEntry(t, body, 99, codec.AppendText(nil, "future"))
	sig := ed25519.Sign(priv, withExtra)
	e.Signature = sig
	// Full frame = body-with-extra + signature entry appended in order:
	// key 18 (signature) sorts before 99, so splice it into position.
	frame := injectEntry(t, withExtra, keySignature, codec.AppendBytes(nil, sig))

	got, err := Decode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyFrame(frame, got); err != nil {
		t.Fatalf("unknown field broke verification: %v", err)
	}
}

// injectEntry inserts key+value into an encoded canonical map, keeping order.
func injectEntry(t *testing.T, mapBytes []byte, key uint64, value []byte) []byte {
	t.Helper()
	d := codec.NewDecoder(mapBytes)
	m, err := d.ReadMapHeader()
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		k   uint64
		raw []byte
	}
	var entries []entry
	for {
		keyStart := d.Pos()
		k, ok, err := m.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		keyBytes := mapBytes[keyStart:d.Pos()]
		val, err := d.ReadRawItem()
		if err != nil {
			t.Fatal(err)
		}
		raw := append(bytes.Clone(keyBytes), val...)
		entries = append(entries, entry{k, raw})
	}
	newRaw := append(codec.AppendUint(nil, key), value...)
	inserted := false
	var out []entry
	for _, en := range entries {
		if !inserted && key < en.k {
			out = append(out, entry{key, newRaw})
			inserted = true
		}
		out = append(out, en)
	}
	if !inserted {
		out = append(out, entry{key, newRaw})
	}
	buf := codec.AppendMap(nil, len(out))
	for _, en := range out {
		buf = append(buf, en.raw...)
	}
	return buf
}

func TestMalformedRejected(t *testing.T) {
	e, priv := sampleEnvelope(t)
	frame, err := e.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	// Truncations must never decode.
	for cut := 0; cut < len(frame); cut++ {
		if _, err := Decode(frame[:cut]); err == nil {
			t.Fatalf("accepted truncated frame at %d", cut)
		}
	}
	// Sequence 0 must be rejected at construction.
	bad, _ := sampleEnvelope(t)
	bad.Sequence = 0
	if _, err := bad.Sign(priv); err == nil {
		t.Fatal("accepted sequence 0")
	}
	// Sequence 2 without previous must be rejected.
	bad2, _ := sampleEnvelope(t)
	bad2.Sequence = 2
	if _, err := bad2.Sign(priv); err == nil {
		t.Fatal("accepted sequence 2 without previous")
	}
}
