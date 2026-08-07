// The signed directory declaration, end to end through the node (CAT-0b).
//
// terminals/policy_kind_test.go proves the label round-trips. What this file
// proves is the part a person can observe: an owner can declare a purpose
// and take it back, a stranger learns it from the projection alone, and a
// private space cannot carry one at all.
package node

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
)

func TestAnOwnerCanDeclareAPurposeAndTakeItBack(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	tid, err := rt.CreateSpaceWithOptions("quite.space", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
			Kind:       terminals.SpaceKindDirectory,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := policyOf(t, rt, tid).Kind; got != terminals.SpaceKindDirectory {
		t.Fatalf("the declaration did not survive creation: %q", got)
	}

	// Taking it back is an ordinary revision.
	ordinary := terminals.SpaceKindOrdinary
	if err := rt.RevisePolicy(tid, PolicyDelta{Kind: &ordinary}); err != nil {
		t.Fatal(err)
	}
	if got := policyOf(t, rt, tid).Kind; got != terminals.SpaceKindOrdinary {
		t.Fatalf("the declaration could not be withdrawn: %q", got)
	}
}

// A purpose belongs to the space, not to one of its access modes. The rate
// limit next to it in RevisePolicy IS cleared on a mode flip, which is
// exactly the reflex this guards against.
func TestFlippingThePublishModeKeepsThePurpose(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	tid, err := rt.CreateSpaceWithOptions("a directory", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
			Kind:       terminals.SpaceKindDirectory,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	all := "all"
	if err := rt.RevisePolicy(tid, PolicyDelta{Publish: &all}); err != nil {
		t.Fatal(err)
	}
	pol := policyOf(t, rt, tid)
	if pol.Kind != terminals.SpaceKindDirectory {
		t.Fatal("opening a directory to contributors stopped it being a directory")
	}
	if pol.Publish != terminals.PublishAll {
		t.Fatalf("the flip itself did not happen: %q", pol.Publish)
	}
}

func TestAPrivateSpaceCannotBeCreatedAsADirectory(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	if _, err := rt.CreateSpaceWithOptions("mine", CreateOptions{
		Policy: terminals.SpacePolicy{Kind: terminals.SpaceKindDirectory},
	}); err == nil {
		t.Fatal("a private space was created with a declared purpose")
	}
}

func TestAPurposeThisBuildDoesNotKnowIsRefusedWithItsOwnSentence(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	tid, err := rt.CreateSpaceWithOptions("a space", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	gallery := "gallery"
	err = rt.RevisePolicy(tid, PolicyDelta{Kind: &gallery})
	if err == nil {
		t.Fatal("an unknown purpose was signed")
	}
	if !strings.Contains(err.Error(), "purpose") {
		t.Fatalf("the refusal does not name the field: %v", err)
	}
}

// THE ONE THAT MATTERS: a stranger with only the link learns what the space
// says it is, through the projection, with no replica and no membership.
func TestAStrangerLearnsTheDeclaredPurpose(t *testing.T) {
	aliceDir, bobDir := t.TempDir(), t.TempDir()
	alice := openRuntime(t, aliceDir, "alice")
	defer alice.Close()
	bob := openRuntime(t, bobDir, "bob")
	defer bob.Close()
	srv, _ := setUpRelay(t, alice)
	defer srv.Close()

	tid, err := alice.CreateSpaceWithOptions("quite.space", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
			Kind:       terminals.SpaceKindDirectory,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := alice.publishPublicProjection(alice.GetSettings().Relay, tid); err != nil {
		t.Fatal(err)
	}
	link, err := alice.ComposePublicLink(tid, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.OpenPublicLink(link); err != nil {
		t.Fatal(err)
	}
	if got := policyOf(t, bob, tid).Kind; got != terminals.SpaceKindDirectory {
		t.Fatalf("the declaration did not reach a stranger: %q", got)
	}
}

func policyOf(t *testing.T, rt *Runtime, tid id.TerminalID) terminals.SpacePolicy {
	t.Helper()
	var pol terminals.SpacePolicy
	if err := rt.withSpace(tid, func(st *spaceState) error {
		pol = st.space.Policy()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return pol
}
