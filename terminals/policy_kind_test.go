package terminals

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
)

// A space may declare what it is FOR: qp.kind=directory says "I am meant to
// be a place through which other spaces are found" (CAT-0b).
//
// The declaration confers nothing. No admission, rate, relay, sync or fetch
// decision reads it — that inertness is what makes it a semantic layer
// rather than a second protocol. What it replaces is a free-text `catalog`
// tag on somebody's publication, which said only that an author typed a
// word.

func publicDirectory() SpacePolicy {
	return SpacePolicy{
		Visibility: VisibilityPublic,
		Join:       JoinOpen,
		Publish:    PublishAll,
		Kind:       SpaceKindDirectory,
	}
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

func TestADirectoryDeclarationRoundTrips(t *testing.T) {
	p := publicDirectory()
	if err := p.Validate(); err != nil {
		t.Fatalf("a public directory is a legal policy: %v", err)
	}
	labels := p.Labels()
	if !hasLabel(labels, "qp.kind=directory") {
		t.Fatalf("no kind label emitted: %v", labels)
	}
	back := ParsePolicy(labels)
	if back.Kind != SpaceKindDirectory {
		t.Fatalf("kind did not survive the round trip: %q", back.Kind)
	}
	// The rest of the policy is undisturbed.
	if back.Visibility != p.Visibility || back.Join != p.Join || back.Publish != p.Publish {
		t.Fatalf("the kind label disturbed the rest of the policy: %+v", back)
	}
}

// An ordinary space emits nothing at all, so every manifest written before
// this existed stays byte-identical.
func TestAnOrdinarySpaceCarriesNoKindLabel(t *testing.T) {
	open := SpacePolicy{Visibility: VisibilityPublic, Join: JoinOpen, Publish: PublishAll}
	for _, l := range open.Labels() {
		if strings.HasPrefix(l, "qp.kind=") {
			t.Fatalf("an ordinary space emitted %q", l)
		}
	}
	if ParsePolicy(open.Labels()).Kind != SpaceKindOrdinary {
		t.Fatal("an absent label did not parse as an ordinary space")
	}
}

// THE LOAD-BEARING TEST OF THIS GATE.
//
// A value this build has never heard of must be IGNORED, never `bad`. Every
// other scalar fails the whole policy closed on garbage, which is right when
// a misread number changes who may write — but a kind changes nothing, and
// `bad` collapses the policy to private, which makes the space unreadable
// (MaterializePublicProjection refuses a non-public policy). Failing closed
// here would make every directory published by a future build invisible to
// this one.
func TestAnUnknownKindIsIgnoredRatherThanFailingClosed(t *testing.T) {
	labels := []string{"qp.visibility=public", "qp.join=open", "qp.kind=gallery"}
	p := ParsePolicy(labels)
	if !p.IsPublic() {
		t.Fatal("an unknown kind made the space unreadable — it must be ignored, not fatal")
	}
	if p.Kind != SpaceKindOrdinary {
		t.Fatalf("an unknown kind was kept as %q", p.Kind)
	}
}

// An older build has no case for the key at all: it skips it and reads an
// ordinary public space. The same story qp.rate already tells.
func TestAnOlderBuildReadsADirectoryAsAnOrdinarySpace(t *testing.T) {
	labels := publicDirectory().Labels()
	var without []string
	for _, l := range labels {
		if !strings.HasPrefix(l, "qp.kind=") {
			without = append(without, l)
		}
	}
	p := ParsePolicy(without)
	if !p.IsPublic() || p.Join != JoinOpen {
		t.Fatalf("an older build could not read the space at all: %+v", p)
	}
}

// A duplicated scalar key is ambiguous, and ambiguity fails closed — the
// same rule as every other scalar. Unforgeable in practice: labels are
// covered by the manifest signature, and Labels() emits at most one.
func TestConflictingKindsAreAmbiguousLikeAnyOtherScalar(t *testing.T) {
	p := ParsePolicy([]string{
		"qp.visibility=public", "qp.join=open",
		"qp.kind=directory", "qp.kind=directory",
	})
	if p.IsPublic() {
		t.Fatal("two kind labels were not treated as ambiguous")
	}
}

func TestAPrivateSpaceIsNotADirectory(t *testing.T) {
	p := SpacePolicy{Kind: SpaceKindDirectory}
	if err := p.Validate(); err == nil {
		t.Fatal("a private space was allowed to declare a purpose")
	}
}

func TestAnUnknownKindIsNeverSigned(t *testing.T) {
	p := SpacePolicy{Visibility: VisibilityPublic, Join: JoinOpen, Publish: PublishAll, Kind: "gallery"}
	if err := p.Validate(); err == nil {
		t.Fatal("an unknown kind was accepted on the way in")
	}
}

// A directory is an ordinary space with a purpose: every access shape stays
// legal. Constraining them here would encode a curation opinion into a
// semantic label.
func TestADirectoryFitsEveryPublicShape(t *testing.T) {
	owner := WriterBinding{Principal: id.PrincipalID{1}, Device: id.DeviceID{2}}
	for _, p := range []SpacePolicy{
		{Visibility: VisibilityPublic, Join: JoinOpen, Publish: PublishAll, Kind: SpaceKindDirectory},
		{Visibility: VisibilityUnlisted, Join: JoinOpen, Publish: PublishAll, Kind: SpaceKindDirectory},
		{Visibility: VisibilityPublic, Join: JoinNone, Publish: PublishCurated,
			Writers: []WriterBinding{owner}, Kind: SpaceKindDirectory},
		{Visibility: VisibilityUnlisted, Join: JoinNone, Publish: PublishCurated,
			Writers: []WriterBinding{owner}, Kind: SpaceKindDirectory},
	} {
		if err := p.Validate(); err != nil {
			t.Fatalf("%s/%s directory refused: %v", p.Visibility, p.Publish, err)
		}
	}
}

// Purpose must not be the one creation-immutable axis. A space that grew
// into a hub can say so; one that stopped being a hub can stop.
func TestADirectoryDeclarationCanBeTakenBack(t *testing.T) {
	ordinary := SpacePolicy{Visibility: VisibilityPublic, Join: JoinOpen, Publish: PublishAll}
	dir := publicDirectory()
	if err := ValidateRevision(ordinary, dir); err != nil {
		t.Fatalf("a space could not become a directory: %v", err)
	}
	if err := ValidateRevision(dir, ordinary); err != nil {
		t.Fatalf("a directory could not stop being one: %v", err)
	}
}

// One more label against manifest.MaxLabels: a curated directory at the
// writer ceiling must still sign. The cliff pre-exists; this makes it one
// writer shallower, and it should be met by a test rather than at signing
// time.
func TestTheLabelBudgetSurvivesADirectoryWithWriters(t *testing.T) {
	c := DefaultCharacter("studio")
	c.Rituals = []string{"listening_session", "one_photo_a_day", "question_of_the_week"}
	c.Presence = []string{"mixing", "reading"}

	p := publicDirectory()
	p.Join, p.Publish = JoinNone, PublishCurated
	for i := range 13 {
		p.Writers = append(p.Writers, WriterBinding{
			Principal: id.PrincipalID{byte(i)}, Device: id.DeviceID{byte(i)},
		})
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("policy: %v", err)
	}
	labels := append(c.Labels("a directory"), p.Labels()...)
	if len(labels) > manifest.MaxLabels {
		t.Fatalf("a curated directory with %d writers needs %d labels, over the %d ceiling",
			len(p.Writers), len(labels), manifest.MaxLabels)
	}
}
