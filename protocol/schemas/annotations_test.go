// The universal media annotation's wire (SP-2): text on an asset,
// optionally at an instant. The two-meanings rule is a wire fact here:
// position 0 WITH the key is "the very start", absent is "the whole
// asset" — a decoder must not collapse them.
package schemas

import (
	"strings"
	"testing"
)

const (
	annHex64 = "aa11bb22cc33dd44ee55ff660011223344556677889900aabbccddeeff001122"
	annHex32 = "0123456789abcdef0123456789abcdef"
)

func sampleAnnotation() *AssetAnnotation {
	a := &AssetAnnotation{Text: "вокал здесь суховат", Asset: annHex64}
	copy(a.AnnotationID[:], []byte("0123456789abcdef"))
	return a
}

func TestAssetAnnotationRoundTrip(t *testing.T) {
	a := sampleAnnotation()
	a.SetPosition(102_000) // 01:42
	var oid [16]byte
	copy(oid[:], []byte("fedcba9876543210"))
	a.ObjectID = &oid
	enc, err := a.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeAssetAnnotation(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != a.Text || got.Asset != a.Asset || got.AnnotationID != a.AnnotationID ||
		!got.HasPosition() || got.PositionMs != 102_000 ||
		got.ObjectID == nil || *got.ObjectID != oid {
		t.Fatalf("mismatch: %+v", got)
	}
}

func TestAssetAnnotationTwoMeanings(t *testing.T) {
	// Whole-asset note: no position key on the wire.
	whole := sampleAnnotation()
	enc, err := whole.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeAssetAnnotation(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasPosition() {
		t.Fatal("whole-asset note grew a position")
	}
	// Point-in-time at 0 is a LEGAL instant, distinct from absent.
	start := sampleAnnotation()
	start.SetPosition(0)
	enc2, err := start.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got2, err := DecodeAssetAnnotation(enc2)
	if err != nil {
		t.Fatal(err)
	}
	if !got2.HasPosition() || got2.PositionMs != 0 {
		t.Fatal("position 0 collapsed into absent")
	}
	if string(enc) == string(enc2) {
		t.Fatal("the two meanings share one wire form")
	}
}

func TestAssetAnnotationRefusals(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*AssetAnnotation)
	}{
		{"empty text", func(a *AssetAnnotation) { a.Text = "" }},
		{"over-long text", func(a *AssetAnnotation) {
			a.Text = strings.Repeat("я", MaxAnnotationTextRunes+1)
		}},
		{"zero id", func(a *AssetAnnotation) { a.AnnotationID = [16]byte{} }},
		{"bad asset hex", func(a *AssetAnnotation) { a.Asset = "BEEF" }},
		{"odd asset width", func(a *AssetAnnotation) { a.Asset = annHex32[:20] }},
	}
	for _, c := range cases {
		a := sampleAnnotation()
		c.mut(a)
		if _, err := a.Encode(); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	// Decode-side bound on hostile bytes.
	buf, _ := sampleAnnotation().Encode()
	if _, err := DecodeAssetAnnotation(buf[:len(buf)-2]); err == nil {
		t.Fatal("truncated payload accepted")
	}
	if !Known(AssetAnnotated) {
		t.Fatal("asset.annotated.v1 not registered")
	}
	// Legacy 32-hex asset ids are accepted (the asset API accepts both
	// widths, the annotation must not orphan V1 assets).
	legacy := sampleAnnotation()
	legacy.Asset = annHex32
	if _, err := legacy.Encode(); err != nil {
		t.Fatalf("legacy 32-hex refused: %v", err)
	}
}
