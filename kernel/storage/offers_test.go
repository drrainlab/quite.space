package storage

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func TestOfferBookRoundTripsAndSkipsAppendedFields(t *testing.T) {
	var sp id.TerminalID
	var dv id.DeviceID
	sp[0], dv[0] = 1, 2
	in := []OfferRecord{{Space: sp, Device: dv, Endpoint: "203.0.113.9:7411", Cursor: 42, Guess: true, Legacy: false, At: 1000, FullAt: 900}}
	out, err := DecodeOfferBook(AppendOfferBook(nil, in))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0] != in[0] {
		t.Fatalf("round trip: %+v", out)
	}
	// A record from a later build with one more field: the field is skipped.
	buf := codec.AppendArray(nil, 2)
	buf = codec.AppendUint(buf, OfferBookVersion)
	buf = codec.AppendArray(buf, 1)
	buf = codec.AppendArray(buf, offerRecFields+1)
	buf = codec.AppendBytes(buf, sp[:])
	buf = codec.AppendBytes(buf, dv[:])
	buf = codec.AppendText(buf, "203.0.113.9:7411")
	buf = codec.AppendUint(buf, 7)
	buf = codec.AppendBool(buf, false)
	buf = codec.AppendBool(buf, true)
	buf = codec.AppendUint(buf, 5)
	buf = codec.AppendUint(buf, 4)
	buf = codec.AppendText(buf, "a field this build does not know")
	out, err = DecodeOfferBook(buf)
	if err != nil || len(out) != 1 || out[0].Cursor != 7 || !out[0].Legacy {
		t.Fatalf("appended field not skipped: %v %+v", err, out)
	}
	// An unknown version is refused, not misread.
	bad := codec.AppendArray(nil, 2)
	bad = codec.AppendUint(bad, OfferBookVersion+1)
	bad = codec.AppendArray(bad, 0)
	if _, err := DecodeOfferBook(bad); err != ErrOfferBookVersion {
		t.Fatalf("unknown version: %v", err)
	}
	if _, err := DecodeOfferBook([]byte("garbage")); err == nil {
		t.Fatal("garbage decoded")
	}
}
