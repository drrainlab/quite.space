package composition

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
)

func testSpaceKey(t *testing.T) (id.TerminalID, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var tid id.TerminalID
	copy(tid[:], pub)
	return tid, priv
}

func sampleComposition() *Composition {
	return &Composition{
		CoordinateSystem: CoordinateSystem,
		Zones: []Zone{
			{ID: "shelf", Kind: "shelf", Renderer: "shelf.compact.v1", FallbackRenderer: "ordered-list.v1"},
			{ID: "wall", Kind: "wall", Renderer: "wall.freeform-lite.v1", FallbackRenderer: "card-grid.v1"},
		},
		Objects: []Object{{
			ID: "object:music-1", SemanticKind: "audio", ZoneID: "shelf",
			Renderer: "music.card.v1", FallbackRenderer: "generic.audio.v1",
			FallbackTitle: "Night Drive", FallbackAuthor: "Alice", FallbackDetail: "3:42",
			SourceAssetID: "abcd", Transform: Transform{X: 100, Y: 200, W: 280, H: 140, RotationDeci: -40, Z: 2},
		}},
	}
}

func TestSnapshotSignVerifyRoundTrip(t *testing.T) {
	tid, priv := testSpaceKey(t)
	payload := sampleComposition().Encode()

	snap, err := NewSnapshot(tid, KindComposition, 1, 187, nil, payload)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := snap.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}

	got, err := DecodeSnapshot(frame)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.DocumentKind != KindComposition || got.Revision != 1 || got.ProjectedThrough != 187 {
		t.Fatalf("header did not round-trip: %+v", got)
	}
	comp, err := DecodeComposition(got.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(comp.Objects) != 1 || comp.Objects[0].FallbackTitle != "Night Drive" ||
		comp.Objects[0].Transform.RotationDeci != -40 {
		t.Fatalf("payload did not round-trip: %+v", comp)
	}
}

func TestSnapshotTamperRejected(t *testing.T) {
	tid, priv := testSpaceKey(t)
	payload := sampleComposition().Encode()
	snap, _ := NewSnapshot(tid, KindComposition, 1, 10, nil, payload)
	frame, err := snap.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte inside the payload region: signature must fail.
	tampered := append([]byte(nil), frame...)
	tampered[len(tampered)-40] ^= 0xFF
	got, err := DecodeSnapshot(tampered)
	if err == nil {
		if verr := got.Verify(); verr == nil {
			t.Fatal("tampered snapshot verified")
		}
	}

	// A payload swapped under the same signature also fails (projection hash).
	other := (&Composition{CoordinateSystem: CoordinateSystem}).Encode()
	got2, _ := DecodeSnapshot(frame)
	got2.Payload = other
	if err := got2.Verify(); err == nil {
		t.Fatal("payload swap not caught")
	}
}

func TestSnapshotChainInvariants(t *testing.T) {
	tid, priv := testSpaceKey(t)

	p1 := (&Appearance{MotionPolicy: "quiet", Density: "calm"}).Encode()
	s1, _ := NewSnapshot(tid, KindAppearance, 1, 10, nil, p1)
	f1, err := s1.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	h1 := Hash(f1)

	// Valid next revision links and does not move the clock backward.
	p2 := (&Appearance{MotionPolicy: "still", Density: "calm"}).Encode()
	s2, _ := NewSnapshot(tid, KindAppearance, 2, 20, &h1, p2)
	if _, err := s2.Sign(priv); err != nil {
		t.Fatal(err)
	}
	if err := s2.VerifyChainStep(s1, f1); err != nil {
		t.Fatalf("valid chain step rejected: %v", err)
	}

	// Wrong previous hash breaks the link.
	bad := h1
	bad[0] ^= 0xFF
	s2b, _ := NewSnapshot(tid, KindAppearance, 2, 20, &bad, p2)
	s2b.Sign(priv)
	if err := s2b.VerifyChainStep(s1, f1); err == nil {
		t.Fatal("broken previous hash accepted")
	}

	// Clock going backward is rejected.
	s2c, _ := NewSnapshot(tid, KindAppearance, 2, 5, &h1, p2)
	s2c.Sign(priv)
	if err := s2c.VerifyChainStep(s1, f1); err == nil {
		t.Fatal("clock regression accepted")
	}

	// Changing the document kind mid-chain is rejected.
	s2d, _ := NewSnapshot(tid, KindComposition, 2, 20, &h1, (&Composition{CoordinateSystem: CoordinateSystem}).Encode())
	s2d.Sign(priv)
	if err := s2d.VerifyChainStep(s1, f1); err == nil {
		t.Fatal("document kind change accepted")
	}
}

func TestSnapshotForeignSignerRejected(t *testing.T) {
	tid, _ := testSpaceKey(t)
	_, otherPriv := testSpaceKey(t)
	payload := (&Appearance{Density: "calm"}).Encode()
	snap, _ := NewSnapshot(tid, KindAppearance, 1, 1, nil, payload)
	// Signing with a key that is not the space's must not even sign.
	if _, err := snap.Sign(otherPriv); err == nil {
		t.Fatal("foreign key signed a space snapshot")
	}
}
