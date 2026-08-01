package terminals

import "testing"

func communityWithRate(n int) SpacePolicy {
	return SpacePolicy{
		Visibility: VisibilityPublic, Join: JoinOpen, Publish: PublishAll,
		MaxFramesPerAuthor: n,
	}
}

// The first integer-valued qp.* label in the tree, so its round trip is
// worth pinning on its own.
func TestContributionLimitRoundTrips(t *testing.T) {
	p := communityWithRate(16)
	if err := p.Validate(); err != nil {
		t.Fatalf("a valid limit was refused: %v", err)
	}
	got := ParsePolicy(p.Labels())
	if got.MaxFramesPerAuthor != 16 {
		t.Fatalf("limit did not survive the labels: %+v", got)
	}
	if !got.IsPublic() || got.Join != JoinOpen {
		t.Fatalf("the rest of the policy was disturbed: %+v", got)
	}

	// Unset means no label at all, so a space that never touched this keeps
	// a byte-identical manifest.
	for _, l := range communityWithRate(0).Labels() {
		if len(l) > 8 && l[:8] == "qp.rate=" {
			t.Fatalf("an unset limit emitted a label: %q", l)
		}
	}
}

// An old build has no case for qp.rate, and ParsePolicy's switch has no
// default-bad arm — so the label is skipped and the space stays readable.
func TestAnOlderBuildIgnoresTheLimit(t *testing.T) {
	labels := []string{"qp.visibility=public", "qp.join=open", "qp.rate=16"}
	// Simulated by dropping the label, which is exactly what a build without
	// the case does.
	old := ParsePolicy(labels[:2])
	if !old.IsPublic() || old.MaxFramesPerAuthor != 0 {
		t.Fatalf("an older build did not simply read a limitless community: %+v", old)
	}
}

func TestAMalformedLimitFailsClosed(t *testing.T) {
	base := []string{"qp.visibility=public", "qp.join=open"}
	for _, bad := range []string{
		"qp.rate=lots", // not a number at all
		"qp.rate=1",    // below the floor that keeps a
		"qp.rate=0",    // forged claim from silencing anyone
		"qp.rate=4096", // looser than the cap that applies
		"qp.rate=-8",   // nonsense with a sign
	} {
		got := ParsePolicy(append(append([]string{}, base...), bad))
		if got.IsPublic() {
			t.Fatalf("%q did not fail the policy closed", bad)
		}
	}
	// Two limits: ambiguous, like two visibilities.
	got := ParsePolicy(append(append([]string{}, base...), "qp.rate=8", "qp.rate=48"))
	if got.IsPublic() {
		t.Fatal("conflicting limits did not fail closed")
	}
}

func TestTheLimitIsForOpenCommunitiesOnly(t *testing.T) {
	priv := SpacePolicy{MaxFramesPerAuthor: 16}
	if err := priv.Validate(); err == nil {
		t.Fatal("a private space accepted a contribution limit")
	}

	curated := SpacePolicy{
		Visibility: VisibilityPublic, Publish: PublishCurated,
		Writers:            []WriterBinding{{}},
		MaxFramesPerAuthor: 16,
	}
	if err := curated.Validate(); err == nil {
		t.Fatal("a broadcast space accepted a contribution limit — it already " +
			"admits only attested writers")
	}
}

func TestTheLimitIsBoundedOnBothSides(t *testing.T) {
	if err := communityWithRate(MinFramesPerAuthor - 1).Validate(); err == nil {
		t.Fatal("a limit below the floor was accepted — tight enough that a " +
			"forged device claim could exhaust somebody's share")
	}
	if err := communityWithRate(MaxFramesPerAuthor + 1).Validate(); err == nil {
		t.Fatal("a limit above the defence cap was accepted, which would read " +
			"as louder than the ceiling that actually applies")
	}
	for _, n := range []int{MinFramesPerAuthor, 24, MaxFramesPerAuthor} {
		if err := communityWithRate(n).Validate(); err != nil {
			t.Fatalf("limit %d refused: %v", n, err)
		}
	}
}
