// PA-0.4A/B acceptance: the signed public projection is independently
// installable (I6), its metadata+contents are bound by the space signature
// (I7), and installing the bounded selection materializes exactly the
// publisher's projected state (I9) — including for a completely fresh
// reader with zero prior knowledge.
package terminals_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/projection"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/human"
)

func openCommunityPolicy() terminals.SpacePolicy {
	return terminals.SpacePolicy{
		Visibility: terminals.VisibilityUnlisted,
		Join:       terminals.JoinOpen,
	}
}

func buildPublicSpace(t *testing.T, n int) (*terminals.Space, *terminals.Participant) {
	t.Helper()
	owner, err := human.New("owner")
	if err != nil {
		t.Fatal(err)
	}
	s, err := terminals.NewSpaceWithPolicy("Fieldnotes", owner.Principal.ID,
		terminals.DefaultCharacter("forest"), openCommunityPolicy())
	if err != nil {
		t.Fatal(err)
	}
	base := uint64(time.Now().Unix()) - uint64(n)
	for i := 0; i < n; i++ {
		if _, err := human.Say(owner, s, fmt.Sprintf("note %d", i), human.SayOptions{}, base+uint64(i)); err != nil {
			t.Fatal(err)
		}
	}
	return s, owner
}

// I6 + I9, untruncated: a fresh reader installs the projection and lands on
// the publisher's exact message state.
func TestProjectionFreshReaderMaterializes(t *testing.T) {
	s, owner := buildPublicSpace(t, 20)
	wire, digest, err := s.BuildPublicProjection(1, owner.Device.ID,
		uint64(time.Now().Unix()), terminals.DefaultProjectionLimits())
	if err != nil {
		t.Fatal(err)
	}
	env, err := projection.Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if env.Truncated {
		t.Fatal("20 notes must not truncate under default limits")
	}
	reader := terminals.Replica(s.ID)
	reader.ReadOnly = true
	applied, err := reader.InstallPublicProjection(env)
	if err != nil {
		t.Fatal(err)
	}
	if applied == 0 {
		t.Fatal("nothing applied")
	}
	om, rm := s.State.Messages(), reader.State.Messages()
	if len(om) != len(rm) {
		t.Fatalf("reader materialized %d of %d messages", len(rm), len(om))
	}
	for i := range om {
		if om[i].Text != rm[i].Text || om[i].ID != rm[i].ID {
			t.Fatalf("message %d diverged: %q vs %q", i, om[i].Text, rm[i].Text)
		}
	}
	// Title/character arrived with the verified manifest.
	title, c := reader.Character()
	if title != "Fieldnotes" || c.Archetype != "forest" {
		t.Fatalf("manifest not installed: %q %q", title, c.Archetype)
	}
	// Reinstall is idempotent.
	if again, _ := reader.InstallPublicProjection(env); again != 0 {
		t.Fatalf("reinstall applied %d duplicates", again)
	}
	_ = digest
}

// The I6 truncation test: an author chain far beyond MaxFrames — the fresh
// reader must land on the NEWEST state with zero unresolved predecessor
// dependencies (the projection store is gap-tolerant by design).
func TestTruncatedProjectionInstallsClean(t *testing.T) {
	s, owner := buildPublicSpace(t, 60)
	lim := terminals.PublicProjectionLimits{MaxFrames: 30, MaxBytes: 6 << 20, MaxAge: 0}
	wire, _, err := s.BuildPublicProjection(3, owner.Device.ID,
		uint64(time.Now().Unix()), lim)
	if err != nil {
		t.Fatal(err)
	}
	env, err := projection.Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !env.Truncated {
		t.Fatal("60 frames under MaxFrames=30 must truncate")
	}
	if len(env.Frames) > 30 {
		t.Fatalf("frame bound violated: %d", len(env.Frames))
	}
	if env.OldestTime == 0 {
		t.Fatal("truncated projection must state its oldest retained time")
	}
	if len(env.CutPoints) == 0 {
		t.Fatal("truncation must record chain cut points")
	}
	reader := terminals.Replica(s.ID)
	if _, err := reader.InstallPublicProjection(env); err != nil {
		t.Fatal(err)
	}
	rm := reader.State.Messages()
	if len(rm) == 0 {
		t.Fatal("reader materialized nothing from a truncated projection")
	}
	// The NEWEST message survives; every materialized message is a suffix
	// of the owner's feed (deterministic display order).
	om := s.State.Messages()
	if rm[len(rm)-1].Text != om[len(om)-1].Text {
		t.Fatalf("newest message lost: %q vs %q",
			rm[len(rm)-1].Text, om[len(om)-1].Text)
	}
	offset := len(om) - len(rm)
	for i := range rm {
		if rm[i].Text != om[offset+i].Text {
			t.Fatalf("retained window is not the newest suffix at %d", i)
		}
	}
}

// I7: the signature binds seq + truncation metadata + contents — any
// mutation fails verification; a forged/huge seq cannot be minted without
// the space key.
func TestProjectionSignatureBindsEverything(t *testing.T) {
	s, owner := buildPublicSpace(t, 5)
	wire, _, err := s.BuildPublicProjection(7, owner.Device.ID,
		uint64(time.Now().Unix()), terminals.DefaultProjectionLimits())
	if err != nil {
		t.Fatal(err)
	}
	base, err := projection.Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := projection.Verify(base); err != nil {
		t.Fatal(err)
	}
	mutate := func(name string, f func(e *projection.Envelope)) {
		e, err := projection.Decode(wire)
		if err != nil {
			t.Fatal(err)
		}
		f(e)
		if err := projection.Verify(e); err == nil {
			t.Fatalf("%s: mutation passed verification", name)
		}
	}
	mutate("forged seq", func(e *projection.Envelope) { e.Seq = 1 << 60 })
	mutate("hidden truncation", func(e *projection.Envelope) { e.Truncated = !e.Truncated })
	mutate("dropped frame", func(e *projection.Envelope) { e.Frames = e.Frames[1:] })
	mutate("swapped publisher", func(e *projection.Envelope) { e.PublisherDevice = id.DeviceID{9} })
	mutate("older time lie", func(e *projection.Envelope) { e.OldestTime = 1 })
}

// ContentDigest is stable for identical content and changes with it —
// the publisher's seq-bump rule depends on this.
func TestProjectionContentDigestSemantics(t *testing.T) {
	s, owner := buildPublicSpace(t, 3)
	now := uint64(time.Now().Unix())
	lim := terminals.DefaultProjectionLimits()
	_, d1, err := s.BuildPublicProjection(1, owner.Device.ID, now, lim)
	if err != nil {
		t.Fatal(err)
	}
	// Same content, different seq/time → SAME digest (heartbeat rule).
	_, d2, err := s.BuildPublicProjection(2, owner.Device.ID, now+600, lim)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatal("identical content must digest identically across heartbeats")
	}
	if _, err := human.Say(owner, s, "new note", human.SayOptions{}, now+601); err != nil {
		t.Fatal(err)
	}
	_, d3, err := s.BuildPublicProjection(3, owner.Device.ID, now+602, lim)
	if err != nil {
		t.Fatal(err)
	}
	if d3 == d1 {
		t.Fatal("changed content must change the digest")
	}
}

// Non-public spaces refuse to build projections; foreign envelopes and
// wrong-space installs are refused.
func TestProjectionRefusals(t *testing.T) {
	priv, err := terminals.NewSpace("Sanctum", id.PrincipalID{1})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := priv.BuildPublicProjection(1, id.DeviceID{1}, 100,
		terminals.DefaultProjectionLimits()); err == nil {
		t.Fatal("private space built a public projection")
	}
	s, owner := buildPublicSpace(t, 2)
	wire, _, err := s.BuildPublicProjection(1, owner.Device.ID, 100,
		terminals.DefaultProjectionLimits())
	if err != nil {
		t.Fatal(err)
	}
	env, _ := projection.Decode(wire)
	other := terminals.Replica(id.TerminalID{42})
	if _, err := other.InstallPublicProjection(env); err == nil {
		t.Fatal("projection installed into the wrong space")
	}
}

// PH-0: the wire ceiling is not negotiable. A caller may ask for a larger
// soft budget — the DEFAULT was 6 MiB for months — but the envelope travels
// as one CBOR byte string the relay reads under a 1 MiB item cap, so an
// oversize projection used to be handed over and silently dropped, leaving
// the publisher with a timeout and no explanation. It must be a sentence.
func TestOversizeProjectionIsNamedNotSilent(t *testing.T) {
	s, owner := buildPublicSpace(t, 4)
	base := uint64(time.Now().Unix())
	body := strings.Repeat("x", (16<<10)-16) // just under MaxTextLen
	for i := range 80 {
		if _, err := human.Say(owner, s, fmt.Sprintf("%d %s", i, body),
			human.SayOptions{}, base+uint64(i)); err != nil {
			t.Fatal(err)
		}
	}
	lim := terminals.DefaultProjectionLimits()
	lim.MaxBytes = 100 << 20 // a caller who trusts a soft limit too far
	_, _, err := s.BuildPublicProjection(1, owner.Device.ID, base, lim)
	if err == nil {
		t.Fatal("an oversize projection was built without complaint")
	}
	if !errors.Is(err, terminals.ErrProjectionTooLarge) {
		t.Fatalf("wrong error, a publisher cannot act on this: %v", err)
	}
	// And with the real defaults the same space publishes fine: aging is
	// what keeps it under the ceiling.
	if _, _, err := s.BuildPublicProjection(1, owner.Device.ID, base,
		terminals.DefaultProjectionLimits()); err != nil {
		t.Fatalf("default limits must still produce a publishable projection: %v", err)
	}
}

// PH-2: the ingress address a space advertises is INSIDE the signature and
// the content digest. An uncommitted hint would be a squatter's invitation:
// swap it in transit and a space's contributions go to a mailbox its owner
// never drains, with nothing to detect the swap.
func TestIngressHintsAreSignedAndDigested(t *testing.T) {
	s, owner := buildPublicSpace(t, 3)
	lim := terminals.DefaultProjectionLimits()
	root, ok := s.IngressRoot()
	if !ok {
		t.Fatal("controller replica must have an ingress root")
	}
	lim.IngressHints = [][]byte{[]byte("aaaaaaaaaaaaaaaa"), []byte("bbbbbbbbbbbbbbbb")}
	now := uint64(time.Now().Unix())
	wire, digest, err := s.BuildPublicProjection(1, owner.Device.ID, now, lim)
	if err != nil {
		t.Fatal(err)
	}
	env, err := projection.Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if len(env.IngressHints) != 2 {
		t.Fatalf("hints did not survive the wire: %d", len(env.IngressHints))
	}
	if projection.Verify(env) != nil {
		t.Fatal("a freshly signed envelope failed verification")
	}
	// Tamper: the signature must reject it.
	env.IngressHints[0] = []byte("cccccccccccccccc")
	if projection.Verify(env) == nil {
		t.Fatal("a redirected ingress address passed verification")
	}
	// And a different address is different CONTENT, so the publisher bumps
	// Seq rather than silently serving two meanings of one number.
	lim2 := terminals.DefaultProjectionLimits()
	lim2.IngressHints = [][]byte{[]byte("cccccccccccccccc")}
	_, digest2, err := s.BuildPublicProjection(1, owner.Device.ID, now, lim2)
	if err != nil {
		t.Fatal(err)
	}
	if digest == digest2 {
		t.Fatal("the content digest ignores the ingress address")
	}
	// The root is the owner's alone: it comes from the space key, which a
	// reader replica does not have.
	reader := terminals.Replica(s.ID)
	if _, ok := reader.IngressRoot(); ok {
		t.Fatal("a replica without the space key derived an ingress root")
	}
	_ = root
}

// A v1 envelope must be refused as a VERSION problem, not as a broken
// signature — the difference between "upgrade me" and "you are under attack".
//
// BUILT, NOT HUNTED FOR. The first version of this test searched the encoded
// envelope for the byte pair {0x03, 0x02} — "key 3, value 2" — and rewrote
// the 2 to a 1. Those two bytes also occur, by chance, inside a signature or
// a digest, and when the search found one of those instead it corrupted a
// random field: the decoder then said "map keys not strictly ascending" and
// the test reported a version skew "reported as something else". A real
// failure, in the test, roughly once in a hundred runs — invisible for
// months and impossible to reproduce on demand.
//
// So the old envelope is encoded deliberately, with the same codec a v1
// publisher would have used: the ONE field this test is about is set to 1,
// and nothing else is left to chance.
func TestOldFormatIsRefusedAsAVersionNotASignature(t *testing.T) {
	var spaceID id.TerminalID
	for i := range spaceID {
		spaceID[i] = byte(i)
	}
	// magic + a map holding what Decode needs to reach the version check: the
	// space, the format, and a signature (so an unsigned envelope is not what
	// gets refused instead).
	buf := []byte("QPP1")
	buf = codec.AppendMap(buf, 3)
	buf = codec.AppendUint(buf, 1) // keySpace
	buf = codec.AppendBytes(buf, spaceID[:])
	buf = codec.AppendUint(buf, 3)  // keyFormat
	buf = codec.AppendUint(buf, 1)  // …and 1 is the old one
	buf = codec.AppendUint(buf, 12) // keySig
	buf = codec.AppendBytes(buf, make([]byte, 64))

	_, err := projection.Decode(buf)
	if err == nil {
		t.Fatal("a v1 envelope decoded under v2")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Fatalf("a version skew reported as something else: %v", err)
	}
}
