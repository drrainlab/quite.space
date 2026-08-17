// PA-0.1 acceptance: access policy rides the signed space manifest, parses
// canonically, fails closed on ambiguity, and — for curated spaces — keeps
// unauthorized frames out of the canonical log entirely (I2) while the
// low-level write gate refuses reader emits (I3). Private spaces are
// byte-for-byte unchanged.
package terminals_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/human"
)

func broadcastPolicy(writers ...terminals.WriterBinding) terminals.SpacePolicy {
	return terminals.SpacePolicy{
		Visibility: terminals.VisibilityPublic,
		Publish:    terminals.PublishCurated,
		Writers:    writers,
	}
}

func bindingOf(p *terminals.Participant) terminals.WriterBinding {
	return terminals.WriterBinding{Principal: p.Principal, Device: p.Device.ID}
}

// Policy labels roundtrip through the signed manifest and coexist with the
// character labels; an absent policy parses as private.
func TestPolicyLabelsRoundtrip(t *testing.T) {
	alice, err := human.New("alice")
	if err != nil {
		t.Fatal(err)
	}
	pol := broadcastPolicy(bindingOf(alice))
	s, err := terminals.NewSpaceWithPolicy("Open Field", id.PrincipalID{1},
		terminals.DefaultCharacter("campfire"), pol)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Policy()
	if got.Visibility != terminals.VisibilityPublic || got.Publish != terminals.PublishCurated {
		t.Fatalf("policy did not roundtrip: %+v", got)
	}
	if len(got.Writers) != 1 || got.Writers[0] != bindingOf(alice) {
		t.Fatalf("writers did not roundtrip: %+v", got.Writers)
	}
	// Character survives alongside policy.
	title, c := s.Character()
	if title != "Open Field" || c.Archetype != "campfire" {
		t.Fatalf("character lost next to policy: %q %+v", title, c)
	}
	// Absent labels → private zero value.
	if p := terminals.ParsePolicy([]string{"Just a title"}); p.Effective() != terminals.VisibilityPrivate {
		t.Fatalf("absent labels must read private, got %+v", p)
	}
}

// Canonical encoding: writers sorted + deduplicated; conflicting duplicate
// scalar labels fail CLOSED to private (a tampered policy must never widen
// access).
func TestPolicyCanonicalAndFailClosed(t *testing.T) {
	a := terminals.WriterBinding{Principal: id.PrincipalID{2}, Device: id.DeviceID{9}}
	b := terminals.WriterBinding{Principal: id.PrincipalID{1}, Device: id.DeviceID{3}}
	pol := broadcastPolicy(a, b, a) // unsorted, duplicated
	labels := pol.Labels()
	joined := strings.Join(labels, "\n")
	if strings.Count(joined, "writer=") != 2 {
		t.Fatalf("duplicate writer not deduplicated: %v", labels)
	}
	rt := terminals.ParsePolicy(labels)
	if len(rt.Writers) != 2 || rt.Writers[0] != b || rt.Writers[1] != a {
		t.Fatalf("writers not canonically sorted: %+v", rt.Writers)
	}
	// Conflicting duplicate visibility labels → fail closed.
	amb := terminals.ParsePolicy([]string{
		"qp.visibility=public", "qp.visibility=unlisted", "qp.join=open",
	})
	if amb.Effective() != terminals.VisibilityPrivate {
		t.Fatalf("ambiguous policy must fail closed to private, got %+v", amb)
	}
	// Malformed writer binding → fail closed.
	mal := terminals.ParsePolicy([]string{"qp.visibility=public", "qp.publish=curated", "qp.writer=nothex"})
	if mal.Effective() != terminals.VisibilityPrivate {
		t.Fatalf("malformed writer must fail closed, got %+v", mal)
	}
}

// The v1 combination envelope is enforced.
func TestPolicyValidateCombinations(t *testing.T) {
	w := terminals.WriterBinding{Principal: id.PrincipalID{1}, Device: id.DeviceID{1}}
	cases := []struct {
		name string
		p    terminals.SpacePolicy
		ok   bool
	}{
		{"private zero", terminals.SpacePolicy{}, true},
		{"private with join", terminals.SpacePolicy{Join: "open"}, false},
		{"broadcast", broadcastPolicy(w), true},
		{"broadcast without writers", terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic, Publish: terminals.PublishCurated}, false},
		{"broadcast with join", terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic, Publish: terminals.PublishCurated,
			Join: terminals.JoinOpen, Writers: []terminals.WriterBinding{w}}, false},
		{"open community", terminals.SpacePolicy{
			Visibility: terminals.VisibilityUnlisted, Join: terminals.JoinOpen}, true},
		{"public all without join", terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic}, false},
		{"community with writers", terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic, Join: terminals.JoinOpen,
			Writers: []terminals.WriterBinding{w}}, false},
		{"unknown visibility", terminals.SpacePolicy{Visibility: "sorta"}, false},
	}
	for _, c := range cases {
		err := c.p.Validate()
		if c.ok && err != nil {
			t.Fatalf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("%s: invalid combination accepted", c.name)
		}
	}
}

// I2: in a curated space an unauthorized-but-validly-signed frame never
// enters the canonical log; PolicyStats records it; the curator and owner
// both materialize normally.
func TestCuratedAdmissionKeepsSpamOutOfLog(t *testing.T) {
	owner, err := human.New("owner")
	if err != nil {
		t.Fatal(err)
	}
	curator, err := human.New("curator")
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := human.New("stranger")
	if err != nil {
		t.Fatal(err)
	}
	s, err := terminals.NewSpaceWithPolicy("Broadcast", owner.Principal,
		terminals.DefaultCharacter("radio_room"),
		broadcastPolicy(bindingOf(owner), bindingOf(curator)))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := human.Say(owner, s, "from the owner", human.SayOptions{}, 100); err != nil {
		t.Fatalf("owner refused: %v", err)
	}
	if _, err := human.Say(curator, s, "from the curator", human.SayOptions{}, 101); err != nil {
		t.Fatalf("curator refused: %v", err)
	}
	lenBefore := s.Log.Len()

	// The stranger emits into its OWN replica of the space (valid signature,
	// contiguous chain) and the frames arrive via sync/bundle → Absorb.
	foreign := terminals.Replica(s.ID)
	a, err := human.Say(stranger, foreign, "unauthorized spam", human.SayOptions{}, 102)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Absorb(a.Frame); !errors.Is(err, terminals.ErrNotAuthorized) {
		t.Fatalf("unauthorized frame not refused by admission: %v", err)
	}
	if s.Log.Len() != lenBefore {
		t.Fatalf("canonical log grew on unauthorized frame: %d → %d", lenBefore, s.Log.Len())
	}
	if s.PolicyStats.IgnoredTotal != 1 || len(s.PolicyStats.IgnoredRecent) != 1 {
		t.Fatalf("policy stats not recorded: %+v", s.PolicyStats)
	}
	if s.PolicyStats.IgnoredRecent[0].Device != stranger.Device.ID {
		t.Fatalf("rejection records wrong signer: %+v", s.PolicyStats.IgnoredRecent[0])
	}
	// Authorized messages materialized; spam did not.
	msgs := s.State.Messages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 materialized messages, got %d", len(msgs))
	}
	for _, m := range msgs {
		if m.Text == "unauthorized spam" {
			t.Fatal("spam materialized")
		}
	}
}

// I3: a reader replica refuses local emits at the single low-level gate.
func TestReadOnlyReplicaRefusesEmit(t *testing.T) {
	reader, err := human.New("reader")
	if err != nil {
		t.Fatal(err)
	}
	s := terminals.Replica(id.TerminalID{7})
	s.ReadOnly = true
	if _, err := human.Say(reader, s, "hello?", human.SayOptions{}, 1); !errors.Is(err, terminals.ErrReadOnlyReplica) {
		t.Fatalf("reader emit not refused: %v", err)
	}
	if s.Log.Len() != 0 {
		t.Fatal("reader emit reached the log")
	}
}

// Private manifests are byte-identical with and without the policy path
// (zero policy emits no labels) — no behavior change for existing spaces.
func TestZeroPolicyKeepsPrivateManifestShape(t *testing.T) {
	s, err := terminals.NewSpace("Quiet", id.PrincipalID{4})
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Decode(s.ManifestFrame)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range m.DeclaredLabels {
		if strings.HasPrefix(l, "qp.visibility") || strings.HasPrefix(l, "qp.writer") ||
			strings.HasPrefix(l, "qp.join") || strings.HasPrefix(l, "qp.publish") {
			t.Fatalf("private manifest carries policy label %q", l)
		}
	}
	if s.Policy().Effective() != terminals.VisibilityPrivate {
		t.Fatalf("private space policy = %+v", s.Policy())
	}
}
