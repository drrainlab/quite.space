package composition

import "testing"

func TestBundleImmutableID(t *testing.T) {
	b := &Bundle{
		Kind: BundleCore, Priority: 20, EstimatedBytes: 940_000,
		Entries: []BundleEntry{
			{AssetID: "bb", Variant: "preview", Required: true, EncryptionEpoch: 12},
			{AssetID: "aa", Variant: "original", Required: false, EncryptionEpoch: 12},
		},
	}
	id1 := b.ID()

	// Canonical ordering: reordering entries yields the same id.
	b2 := &Bundle{
		Kind: BundleCore, Priority: 20, EstimatedBytes: 940_000,
		Entries: []BundleEntry{
			{AssetID: "aa", Variant: "original", Required: false, EncryptionEpoch: 12},
			{AssetID: "bb", Variant: "preview", Required: true, EncryptionEpoch: 12},
		},
	}
	if b2.ID() != id1 {
		t.Fatal("entry order changed the bundle id (not canonical)")
	}

	// Content change → new id.
	b3 := &Bundle{Kind: BundleCore, Priority: 20, EstimatedBytes: 940_001, Entries: b.Entries}
	if b3.ID() == id1 {
		t.Fatal("content change did not change the bundle id")
	}

	// Round-trip.
	got, err := DecodeBundle(b.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if got.ID() != id1 || len(got.Entries) != 2 {
		t.Fatalf("bundle did not round-trip: %+v", got)
	}
}

func TestBundleIndexAuthenticatesViaSnapshot(t *testing.T) {
	tid, priv := testSpaceKey(t)
	b := &Bundle{Kind: BundleCore, Priority: 10, EstimatedBytes: 1000}
	bid := b.ID()

	idx := &BundleIndex{Bundles: []BundleRef{{BundleID: bid, Kind: BundleCore, Priority: 10, EstimatedBytes: 1000}}}
	snap, err := NewSnapshot(tid, KindBundleIndex, 1, 42, nil, idx.Encode())
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
		t.Fatalf("index snapshot verify: %v", err)
	}
	gi, err := DecodeBundleIndex(got.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(gi.Bundles) != 1 || gi.Bundles[0].BundleID != bid {
		t.Fatalf("index did not bind the bundle id: %+v", gi)
	}
}
