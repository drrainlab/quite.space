package quicklink

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

func aSegment() RadioSegment {
	return RadioSegment{
		KDFVersion: 1, Carrier: "rnode", Profile: "long-fast-ru",
		Seed: bytes.Repeat([]byte{0x5a}, SegmentSeedLen),
	}
}

func TestASegmentSurvivesTheSeal(t *testing.T) {
	tok, err := New()
	if err != nil {
		t.Fatal(err)
	}
	want := Payload{PassLink: "relay:7411\nPASS", From: "alice", Space: "line",
		Segment: aSegment()}
	sealed, err := Seal(tok, want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(tok, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Segment.Present() {
		t.Fatal("the segment did not survive the round trip")
	}
	if !bytes.Equal(got.Segment.Seed, want.Segment.Seed) ||
		got.Segment.Carrier != want.Segment.Carrier ||
		got.Segment.Profile != want.Segment.Profile ||
		got.Segment.KDFVersion != want.Segment.KDFVersion {
		t.Fatalf("segment changed: %+v → %+v", want.Segment, got.Segment)
	}
}

// THE ONE THAT DECIDES THE WIRE.
//
// SealVersion was deliberately not moved for this field, and the reason is a
// sentence a person would read: the outer version check is a HARD REFUSAL, and
// a refusal on a link somebody just typed reads as "you got the words wrong".
// So a build that has never heard of a segment must open one of these links
// and simply not see it.
//
// Simulated the only honest way — by decoding the inner map with a reader that
// knows nothing about key 7, which is exactly what an older binary is.
func TestABuildThatNeverHeardOfSegmentsStillOpensTheLink(t *testing.T) {
	tok, err := New()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := Seal(tok, Payload{
		PassLink: "relay:7411\nPASS", From: "alice", Space: "line",
		MaxUses: 1, Segment: aSegment(),
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Open(tok, sealed)
	if err != nil {
		t.Fatal(err)
	}
	// Everything the old build cares about is intact.
	if p.PassLink == "" || p.From != "alice" || p.Space != "line" || p.MaxUses != 1 {
		t.Fatalf("the fields an older build reads did not survive: %+v", p)
	}
	// And the old decoder's rule — skip what you do not know — is the rule
	// this map is actually encoded under. Pinned so nobody "tidies" the
	// default branch away.
	inner := codec.AppendMap(nil, 2)
	inner = codec.AppendUint(inner, innerKeyPassLink)
	inner = codec.AppendText(inner, "x")
	inner = codec.AppendUint(inner, innerKeySegment)
	inner = appendSegment(inner, aSegment())
	d := codec.NewDecoder(inner)
	m, err := d.ReadMapHeader()
	if err != nil {
		t.Fatal(err)
	}
	for {
		k, ok, err := m.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		if k == innerKeySegment {
			// The older build's branch, verbatim.
			if err := d.SkipItem(); err != nil {
				t.Fatalf("a decoder that does not know key 7 could not skip it: %v", err)
			}
			continue
		}
		if _, err := d.ReadText(); err != nil {
			t.Fatal(err)
		}
	}
}

// A descriptor this build cannot act on is REFUSED, not half-used.
//
// Every one of these produces the same symptom in the field if it slips
// through — a radio that hears nobody and is told nothing — so each is a
// refusal at the decoder rather than a surprise on the air.
func TestAnUnusableSegmentIsRefusedRatherThanHalfRead(t *testing.T) {
	for name, broken := range map[string]RadioSegment{
		"a seed of the wrong length": {KDFVersion: 1, Carrier: "rnode",
			Profile: "long-fast-ru", Seed: []byte{1, 2, 3}},
		"no carrier": {KDFVersion: 1, Profile: "long-fast-ru",
			Seed: bytes.Repeat([]byte{1}, SegmentSeedLen)},
		"no profile": {KDFVersion: 1, Carrier: "rnode",
			Seed: bytes.Repeat([]byte{1}, SegmentSeedLen)},
		"no kdf version": {Carrier: "rnode", Profile: "long-fast-ru",
			Seed: bytes.Repeat([]byte{1}, SegmentSeedLen)},
		"an absurd carrier name": {KDFVersion: 1, Carrier: strings.Repeat("x", MaxCarrierLen+1),
			Profile: "long-fast-ru", Seed: bytes.Repeat([]byte{1}, SegmentSeedLen)},
	} {
		if err := broken.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		} else if !errors.Is(err, ErrBadSegment) {
			t.Errorf("%s: refused for the wrong reason: %v", name, err)
		}
		// And it must not survive a decode either, or the bound is a
		// suggestion rather than a rule.
		enc := appendSegment(nil, broken)
		if _, err := readSegment(codec.NewDecoder(enc)); err == nil {
			t.Errorf("%s: decoded anyway", name)
		}
	}
}
