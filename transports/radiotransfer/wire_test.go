package radiotransfer

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

func testKey(t *testing.T) *TransferKey {
	t.Helper()
	k, err := DeriveTransferKey(bytes.Repeat([]byte{7}, 32), KDFVersion)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func dataFrame(t *testing.T) *Frame {
	t.Helper()
	id, err := NewTransferID()
	if err != nil {
		t.Fatal(err)
	}
	return &Frame{
		Kind: KindData, Transfer: id,
		Index: 2, Count: 5, Total: 500,
		Digest: MessageDigest([]byte("the whole message")),
		Chunk:  []byte("one hundred bytes of nothing in particular"),
	}
}

// Every kind must survive the wire unchanged. Nothing below this line is
// worth checking if the bytes do not mean the same thing on both sides.
func TestEveryFrameKindSurvivesTheWire(t *testing.T) {
	key := testKey(t)
	id, _ := NewTransferID()
	frames := []*Frame{
		dataFrame(t),
		{Kind: KindSACK, Transfer: id, Count: 5, Base: 0, Bitmap: []byte{0b00010111}},
		{Kind: KindCommit, Transfer: id, Digest: MessageDigest([]byte("x"))},
		{Kind: KindCancel, Transfer: id, Reason: 3},
		{Kind: KindRepair, Transfer: id, Reason: 1},
	}
	for _, f := range frames {
		b, err := f.Encode(key)
		if err != nil {
			t.Fatalf("%s: %v", f.Kind, err)
		}
		got, err := Decode(b, key)
		if err != nil {
			t.Fatalf("%s: %v", f.Kind, err)
		}
		if got.Kind != f.Kind || got.Transfer != f.Transfer {
			t.Fatalf("%s came back as %s / %s", f.Kind, got.Kind, got.Transfer.Short())
		}
		if f.Kind == KindData {
			if !bytes.Equal(got.Chunk, f.Chunk) || got.Index != f.Index ||
				got.Count != f.Count || got.Total != f.Total || got.Digest != f.Digest {
				t.Fatalf("DATA came back changed: %+v", got)
			}
		}
		if f.Kind == KindSACK && !bytes.Equal(got.Bitmap, f.Bitmap) {
			t.Fatalf("SACK bitmap came back as %08b", got.Bitmap)
		}
	}
}

// THE reason the MAC exists.
//
// A neighbour within radio range can transmit anything. Without a tag, a
// forged CANCEL stops any transfer on the segment at will, and a forged SACK
// makes a sender believe fragments arrived that never did. On Meshtastic the
// channel PSK currently masks this; on RNode nothing would, and the security
// of this protocol must not be a property of a carrier we intend to replace.
func TestAFrameFromOutsideTheSegmentIsRefused(t *testing.T) {
	ours := testKey(t)
	theirs, err := DeriveTransferKey(bytes.Repeat([]byte{9}, 32), KDFVersion)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := NewTransferID()
	forged, err := (&Frame{Kind: KindCancel, Transfer: id, Reason: 1}).Encode(theirs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(forged, ours); err == nil {
		t.Fatal("a CANCEL written by somebody outside the segment was accepted — " +
			"anyone in radio range could stop every transfer on the segment")
	}
}

// A tag that covers only what this build understands would leave a future
// field unprotected, and "we did not know about it" is not a reason to trust
// it. The MAC covers the whole map, unknown keys included.
func TestAnUnknownFieldIsSkippedButStillProtected(t *testing.T) {
	key := testKey(t)
	id, _ := NewTransferID()

	// A frame from a newer sender: the same COMMIT, plus a key this build has
	// never heard of.
	body := codec.AppendMap(nil, 5)
	body = codec.AppendUint(body, keyVersion)
	body = codec.AppendUint(body, ProtocolVersion)
	body = codec.AppendUint(body, keyKind)
	body = codec.AppendUint(body, uint64(KindCommit))
	body = codec.AppendUint(body, keyTransfer)
	body = codec.AppendBytes(body, id[:])
	body = codec.AppendUint(body, keyDigest)
	d := MessageDigest([]byte("x"))
	body = codec.AppendBytes(body, d[:])
	body = codec.AppendUint(body, 99) // from a version this build does not have
	body = codec.AppendUint(body, 12345)
	frame := append(body, key.Tag(body)...)

	got, err := Decode(frame, key)
	if err != nil {
		t.Fatalf("a frame with one unknown key was refused: %v", err)
	}
	if got.Kind != KindCommit || got.Digest != d {
		t.Fatalf("the known fields did not survive: %+v", got)
	}

	// And flipping a bit INSIDE the unknown field must still break the tag.
	frame[len(body)-1] ^= 1
	if _, err := Decode(frame, key); err == nil {
		t.Fatal("a field this build skips was left unauthenticated — 'we do not " +
			"read it' is not a reason to trust it")
	}
}

// Bounds are checked on DECODE, before anything is allocated, and after the
// MAC — so a stranger can neither make this allocate nor choose which error it
// spends time on.
func TestEveryBoundIsRefusedAtItsEdge(t *testing.T) {
	key := testKey(t)
	id, _ := NewTransferID()
	for _, tc := range []struct {
		name  string
		frame *Frame
	}{
		{"too many fragments", &Frame{Kind: KindData, Transfer: id,
			Count: MaxFragments + 1, Index: 0, Total: 10, Chunk: []byte("x")}},
		{"fragment past the end", &Frame{Kind: KindData, Transfer: id,
			Count: 3, Index: 3, Total: 10, Chunk: []byte("x")}},
		{"message too large", &Frame{Kind: KindData, Transfer: id,
			Count: 2, Index: 0, Total: MaxMessageBytes + 1, Chunk: []byte("x")}},
		{"a fragment bigger than its message", &Frame{Kind: KindData, Transfer: id,
			Count: 2, Index: 0, Total: 2, Chunk: []byte("much longer than two")}},
		{"SACK past the end", &Frame{Kind: KindSACK, Transfer: id,
			Count: 4, Base: 4, Bitmap: []byte{1}}},
	} {
		b, err := tc.frame.Encode(key)
		if err != nil {
			continue // refused at encode, which is also correct
		}
		if _, err := Decode(b, key); err == nil {
			t.Fatalf("%s was accepted", tc.name)
		}
	}
}

// A version mismatch is its OWN answer, never a malformed frame. The two call
// for different actions — one is "upgrade", the other is "something is wrong
// on the air" — and reporting the first as the second is how a version skew
// gets investigated as a radio fault.
func TestAnUnknownVersionSaysSoRatherThanBlamingTheFrame(t *testing.T) {
	key := testKey(t)
	f := dataFrame(t)
	f.Version = ProtocolVersion + 1
	b, err := f.Encode(key)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Decode(b, key)
	if !errors.Is(err, ErrVersion) {
		t.Fatalf("a newer protocol version reported %v, want ErrVersion", err)
	}
}

// The two checks answer different questions and neither substitutes for the
// other: a CRC catches a mangled fragment before its airtime is spent on
// assembly; the digest catches a message assembled from fragments that were
// each individually fine.
func TestACorruptFragmentIsCaughtBeforeAssembly(t *testing.T) {
	key := testKey(t)
	f := dataFrame(t)
	b, err := f.Encode(key)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt a chunk byte and re-tag, as though the carrier mangled the
	// payload under an otherwise authentic frame.
	body := b[:len(b)-MACLen]
	i := bytes.Index(body, f.Chunk)
	if i < 0 {
		t.Fatal("the chunk is not in the encoded frame")
	}
	body[i] ^= 0xFF
	if _, err := Decode(append(body, key.Tag(body)...), key); err == nil {
		t.Fatal("a fragment that failed its own checksum was accepted for assembly")
	}
}

// Not every frame on a shared channel is ours, and most are not. Refusing a
// stranger's traffic loudly would fill a log with the neighbourhood.
func TestSomebodyElsesTrafficIsNotAnError(t *testing.T) {
	key := testKey(t)
	for _, junk := range [][]byte{
		nil,
		[]byte("hello from another protocol entirely"),
		bytes.Repeat([]byte{0xFF}, 4),
	} {
		if _, err := Decode(junk, key); !errors.Is(err, ErrNotTransfer) {
			t.Fatalf("%q reported %v, want ErrNotTransfer", junk, err)
		}
	}
}

// A seed with too little entropy must not produce a key that looks exactly as
// strong as a real one.
func TestAWeakSeedIsRefusedRatherThanStretched(t *testing.T) {
	if _, err := DeriveTransferKey([]byte("short"), KDFVersion); !errors.Is(err, ErrSeedTooShort) {
		t.Fatalf("a five-byte seed produced %v", err)
	}
	// And a KDF version this build does not implement derives NOTHING: a key
	// nobody else in the segment holds is worse than an error, because every
	// frame then fails to verify for a reason that looks like interference.
	_, err := DeriveTransferKey(bytes.Repeat([]byte{1}, 32), KDFVersion+1)
	if err == nil || !strings.Contains(err.Error(), "must agree") {
		t.Fatalf("an unknown KDF version produced %v", err)
	}
}

// The invariant this repository already learned once, in frag.go, and then
// broke twice.
func TestATransferIdIsUnpredictableAndNeverZero(t *testing.T) {
	seen := map[TransferID]bool{}
	for range 1000 {
		id, err := NewTransferID()
		if err != nil {
			t.Fatal(err)
		}
		if id.IsZero() {
			t.Fatal("a zero transfer id was drawn — zero is what an " +
				"uninitialised struct looks like, and must not also name something")
		}
		if seen[id] {
			t.Fatal("the same transfer id twice in a thousand draws: this is a " +
				"counter wearing a random costume")
		}
		seen[id] = true
	}
}

// The restart case, which is where a counter gets it wrong: a counter
// restarts too, so its first id after a restart is one the receiver may still
// remember from before.
func TestASenderRestartCannotCollideWithWhatAReceiverRemembers(t *testing.T) {
	// A receiver's memory of transfers seen before the sender restarted.
	remembered := map[TransferID]bool{}
	for range 256 {
		id, _ := NewTransferID()
		remembered[id] = true
	}
	// The sender restarts. Its FIRST id must not be one of those.
	for range 256 {
		id, _ := NewTransferID()
		if remembered[id] {
			t.Fatal("a fresh sender drew an id the receiver still holds — the " +
				"transfer would be dropped as a duplicate of something else")
		}
	}
}
