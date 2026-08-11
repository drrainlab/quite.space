package projection

import (
	"bytes"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// A projection envelope arrives from a relay, a mirror, or a stranger's
// link. These pin what Decode refuses before it trusts anything.

func TestAnOversizedEnvelopeIsRefusedRatherThanParsed(t *testing.T) {
	// The bound belongs to the decoder, not to whoever calls it. Today's
	// callers happen to cap their input (LAN packets at 1 MiB, relay items
	// at 768 KiB), so this is unreachable through them — which is exactly
	// why it is worth having: "safe because of who happens to call it"
	// stops being true silently.
	big := append([]byte(magic), bytes.Repeat([]byte{0x42}, MaxEnvelopeBytes)...)
	_, err := Decode(big)
	if err == nil {
		t.Fatal("an envelope over the ceiling was accepted")
	}
	if !strings.Contains(err.Error(), "ceiling") {
		t.Errorf("refused with %q — the reason should name the bound", err)
	}
	// And the refusal must not have moved: an envelope at the ceiling is
	// still looked at (it fails later, on its contents, which is correct).
	atLimit := make([]byte, MaxEnvelopeBytes)
	copy(atLimit, magic)
	if _, err := Decode(atLimit); err != nil && strings.Contains(err.Error(), "ceiling") {
		t.Error("an envelope exactly at the ceiling was refused as oversized")
	}
}

func TestNotAProjectionIsSaidPlainly(t *testing.T) {
	if _, err := Decode([]byte("nope")); err == nil {
		t.Fatal("arbitrary bytes decoded as an envelope")
	}
}

// CutPoint ids are fixed 32-byte arrays, and copy() neither complains nor
// panics on a wrong length — it truncates or zero-pads. So a malformed cut
// point used to be accepted as a DIFFERENT device, quietly. Verify would
// still reject the tampered envelope (it re-encodes the fixed 32 bytes),
// but every other id in this decoder is length-checked where it is read,
// and being wrong two layers earlier is not the same as being refused.
func TestACutPointWithATruncatedIdIsRefused(t *testing.T) {
	body := func(dev, tip []byte) []byte {
		buf := []byte(magic)
		buf = codec.AppendMap(buf, 2)
		buf = codec.AppendUint(buf, keyFormat)
		buf = codec.AppendUint(buf, FormatVersion)
		buf = codec.AppendUint(buf, keyCuts)
		buf = codec.AppendArray(buf, 1)
		buf = codec.AppendArray(buf, 3)
		buf = codec.AppendBytes(buf, dev)
		buf = codec.AppendUint(buf, 7)
		buf = codec.AppendBytes(buf, tip)
		return buf
	}
	full := bytes.Repeat([]byte{0xAB}, id.Size)

	// The REASON is the assertion, not merely that something failed. This
	// fixture is unsigned, so Decode refuses it either way — a test that
	// only checked err != nil would pass with the length checks removed,
	// which is exactly what it did on the first attempt.
	refusedForItsCutPoint := func(dev, tip []byte, what string) {
		t.Helper()
		_, err := Decode(body(dev, tip))
		if err == nil {
			t.Errorf("%s was accepted", what)
			return
		}
		if !strings.Contains(err.Error(), "cut-point") {
			t.Errorf("%s was refused for the wrong reason (%v) — it reached the "+
				"signature check, so the id itself was taken as valid", what, err)
		}
	}
	refusedForItsCutPoint(full[:8], full, "a short cut-point device")
	refusedForItsCutPoint(full, full[:8], "a short cut-point tip")
	refusedForItsCutPoint(append(append([]byte(nil), full...), 0xFF), full,
		"an over-long cut-point device")
	// The honest one must not be refused FOR ITS CUT POINT — the fixture
	// carries no signature, so Decode still stops on that, and stopping
	// there is correct. Naming the difference is the whole assertion: a
	// check that went too far would report the cut point instead.
	if _, err := Decode(body(full, full)); err == nil {
		t.Fatal("an unsigned envelope decoded — the signature check is gone")
	} else if strings.Contains(err.Error(), "cut-point") {
		t.Errorf("a well-formed cut point was refused: %v", err)
	}
}
