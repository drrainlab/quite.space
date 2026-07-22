package composition

import "testing"

func TestFixturesValidateAndSign(t *testing.T) {
	tid, priv := testSpaceKey(t)
	cases := []struct {
		kind    string
		payload []byte
	}{
		{KindAppearance, SampleAppearance().Encode()},
		{KindComposition, SampleComposition().Encode()},
		{KindBundleIndex, SampleBundleIndex().Encode()},
	}
	for _, c := range cases {
		snap, err := NewSnapshot(tid, c.kind, 1, 1, nil, c.payload)
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		frame, err := snap.Sign(priv)
		if err != nil {
			t.Fatalf("%s sign: %v", c.kind, err)
		}
		if err := ValidateContract(frame); err != nil {
			t.Fatalf("%s fixture failed validation: %v", c.kind, err)
		}
	}
}

func TestValidateRejectsUnsafe(t *testing.T) {
	// Rotation past ±15°.
	c := SampleComposition()
	c.Objects[0].Transform.RotationDeci = 300
	if err := ValidateComposition(c); err == nil {
		t.Fatal("over-rotated object accepted")
	}

	// Non-allowlisted renderer (the "executable payload" proxy: no free-form
	// markup exists, only a known id or a rejection).
	c2 := SampleComposition()
	c2.Objects[0].Renderer = "<script>alert(1)</script>"
	if err := ValidateComposition(c2); err == nil {
		t.Fatal("non-allowlisted renderer accepted")
	}

	// Missing fallback metadata.
	c3 := SampleComposition()
	c3.Objects[0].FallbackTitle = ""
	if err := ValidateComposition(c3); err == nil {
		t.Fatal("object without fallback title accepted")
	}

	// Object out of the unit square.
	c4 := SampleComposition()
	c4.Objects[0].Transform.X = 900
	c4.Objects[0].Transform.W = 300
	if err := ValidateComposition(c4); err == nil {
		t.Fatal("object past the unit square accepted")
	}

	// Wrong coordinate system.
	c5 := SampleComposition()
	c5.CoordinateSystem = "pixels"
	if err := ValidateComposition(c5); err == nil {
		t.Fatal("unknown coordinate system accepted")
	}

	// Appearance: motion policy off the allowlist.
	a := SampleAppearance()
	a.MotionPolicy = "strobe"
	if err := ValidateAppearance(a); err == nil {
		t.Fatal("disallowed motion policy accepted")
	}

	// Bundle: unknown variant.
	b := &Bundle{Kind: BundleCore, Entries: []BundleEntry{{AssetID: "aa", Variant: "raw", EncryptionEpoch: 1}}}
	if err := ValidateBundle(b); err == nil {
		t.Fatal("unknown bundle variant accepted")
	}
}
