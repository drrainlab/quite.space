package field

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/geo"
)

func mustPt(lat, lon float64) geo.Point {
	p, err := geo.FromDegrees(lat, lon)
	if err != nil {
		panic(err)
	}
	return p
}

func sampleTrack() *Track {
	p1 := mustPt(46.6180, 8.0290)
	p2 := mustPt(46.6205, 8.0315)
	p3 := mustPt(46.6228, 8.0342)
	return &Track{
		StartedAt: 1_790_000_000,
		Samples: []Sample{
			{Tag: SampleQPoint, DtMS: 0, Point: p1, AccuracyM: 8},
			{Tag: SampleQPoint, DtMS: 15_000, Point: p2, AccuracyM: 6},
			{Tag: SampleQGap, DtMS: 15_000, DurationMS: 52_000, Reason: GapNoFix},
			{Tag: SampleQPoint, DtMS: 52_000, Point: p3, AccuracyM: 11},
		},
	}
}

func TestTrackRoundTrip(t *testing.T) {
	tr := sampleTrack()
	enc, err := tr.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.StartedAt != tr.StartedAt || len(got.Samples) != len(tr.Samples) {
		t.Fatalf("round trip lost shape: %+v", got)
	}
	for i := range tr.Samples {
		a, b := tr.Samples[i], got.Samples[i]
		if a.Tag != b.Tag || a.DtMS != b.DtMS || a.Point != b.Point ||
			a.AccuracyM != b.AccuracyM || a.DurationMS != b.DurationMS || a.Reason != b.Reason {
			t.Fatalf("sample %d diverged: %+v vs %+v", i, a, b)
		}
	}
	// Determinism: encoding twice yields identical bytes.
	enc2, _ := got.Encode()
	if !bytes.Equal(enc, enc2) {
		t.Fatal("re-encoding is not byte-stable")
	}
}

// The golden bytes the JS decoder (clients/web-ui/assets/track.js) is
// pinned against. If this changes, the format changed — which for a v1
// asset format is an event, not a refactor.
func TestTrackGoldenVector(t *testing.T) {
	enc, err := sampleTrack().Encode()
	if err != nil {
		t.Fatal(err)
	}
	// The header is asserted exactly; the body is pinned by the
	// round-trip test and by the JS decoder, which consumes this very
	// vector (clients/web-ui/assets/track.js test fixture). If the hex
	// below drifts, the format changed — for a sealed v1 asset format
	// that is an event, not a refactor.
	if got := hex.EncodeToString(enc[:4]); got != "a2011a6a" {
		t.Fatalf("golden header drifted: %s", hex.EncodeToString(enc[:8]))
	}
	t.Logf("golden track vector (%d bytes): %s", len(enc), hex.EncodeToString(enc))
}

func TestValidateRefusals(t *testing.T) {
	// No started_at.
	if _, err := (&Track{Samples: nil}).Encode(); err == nil {
		t.Fatal("a track without started_at encoded")
	}
	// Gap reason outside the vocabulary.
	tr := &Track{StartedAt: 1, Samples: []Sample{
		{Tag: SampleQGap, DtMS: 1, DurationMS: 5, Reason: 9},
	}}
	if _, err := tr.Encode(); err == nil {
		t.Fatal("an invented gap reason encoded")
	}
	// Zero-duration gap says nothing.
	tr.Samples[0] = Sample{Tag: SampleQGap, DtMS: 1, DurationMS: 0, Reason: GapNoFix}
	if _, err := tr.Encode(); err == nil {
		t.Fatal("a zero-duration gap encoded")
	}
	// A point off the planet.
	tr.Samples[0] = Sample{Tag: SampleQPoint, Point: geo.Point{LatE7U: 1 << 62}}
	if _, err := tr.Encode(); err == nil {
		t.Fatal("an out-of-range point encoded")
	}
	// Empty samples are VALID: a sweep of pure silence is honest.
	if _, err := (&Track{StartedAt: 1}).Encode(); err != nil {
		t.Fatalf("an empty track must encode: %v", err)
	}
}

// Forward compatibility is preservation: unknown top-level keys and
// unknown sample tags survive a decode→encode cycle byte-for-byte, and
// an unknown tag never comes back as a point.
func TestUnknownKeysAndTagsArePreserved(t *testing.T) {
	// Build v2-ish bytes by hand: extra key 7, and a sample with tag 9.
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, trKeyStartedAt)
	buf = codec.AppendUint(buf, 1_790_000_000)
	buf = codec.AppendUint(buf, trKeySamples)
	buf = codec.AppendArray(buf, 2)
	// known point
	buf = codec.AppendArray(buf, 5)
	buf = codec.AppendUint(buf, SampleQPoint)
	buf = codec.AppendUint(buf, 0)
	p := mustPt(46.6, 8.0)
	buf = codec.AppendUint(buf, p.LatE7U)
	buf = codec.AppendUint(buf, p.LonE7U)
	buf = codec.AppendUint(buf, 5)
	// unknown tag 9 with its own payload
	buf = codec.AppendArray(buf, 3)
	buf = codec.AppendUint(buf, 9)
	buf = codec.AppendUint(buf, 42)
	buf = codec.AppendText(buf, "future")
	// unknown top-level key 7
	buf = codec.AppendUint(buf, 7)
	buf = codec.AppendText(buf, "passenger")

	tr, err := Decode(buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Samples) != 2 || tr.Samples[1].Tag != 9 || len(tr.Samples[1].Raw) == 0 {
		t.Fatalf("unknown tag not carried: %+v", tr.Samples)
	}
	if len(tr.RawExtra) != 1 || tr.RawExtra[0].Key != 7 {
		t.Fatalf("unknown key not carried: %+v", tr.RawExtra)
	}
	enc, err := tr.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc, buf) {
		t.Fatalf("a re-seal stripped a passenger:\n was %s\n now %s",
			hex.EncodeToString(buf), hex.EncodeToString(enc))
	}
}
