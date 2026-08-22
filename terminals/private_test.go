// M1.A acceptance: private spaces are readable exactly by their members —
// non-members see honest undecryptable counts, invites carry keys, removal
// plus rotation cuts off future reads while past reads honestly remain.
package terminals_test

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/human"
)

// pipe copies all frames from one space log into another (transport-less
// sync for tests).
func pipe(t *testing.T, from, to *terminals.Space) {
	t.Helper()
	if err := from.Log.Replay(func(a eventlog.Applied) error {
		_, err := to.Absorb(a.Frame)
		if err != nil && err != eventlog.ErrChainForked {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateSpaceEndToEnd(t *testing.T) {
	// Alice creates a private space.
	alice, err := human.New("alice")
	if err != nil {
		t.Fatal(err)
	}
	spaceA, err := terminals.NewSpace("Forest Session", alice.Principal)
	if err != nil {
		t.Fatal(err)
	}
	spaceA.EnablePrivate(alice.Device)
	spaceA.AddMember(alice.Device.ID, alice.Device.X25519Pub)
	if _, err := alice.RotateEpoch(spaceA); err != nil {
		t.Fatal(err)
	}
	if _, err := human.Say(alice, spaceA, "secret plan: record at dawn", human.SayOptions{}, 100); err != nil {
		t.Fatal(err)
	}
	// Sanity: alice reads her own space.
	if len(spaceA.State.Messages()) != 1 {
		t.Fatal("author cannot read own space")
	}

	// An observer replica without keys syncs everything and reads nothing.
	observer := terminals.Replica(spaceA.ID)
	pipe(t, spaceA, observer)
	if observer.Log.Len() != spaceA.Log.Len() {
		t.Fatal("observer did not receive frames")
	}
	if got := len(observer.State.Messages()); got != 0 {
		t.Fatalf("observer read %d encrypted messages", got)
	}
	if observer.Undecryptable != 1 {
		t.Fatalf("observer should honestly count 1 undecryptable, got %d", observer.Undecryptable)
	}

	// Bob joins via a signed invite.
	bob, err := human.New("bob")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := spaceA.NewInvite(bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	spaceB, err := terminals.AcceptInvite(invite, bob.Device)
	if err != nil {
		t.Fatal(err)
	}
	pipe(t, spaceA, spaceB)
	msgs := spaceB.State.Messages()
	if len(msgs) != 1 || msgs[0].Text != "secret plan: record at dawn" {
		t.Fatalf("invited member cannot read history: %+v", msgs)
	}

	// Owner registers bob and rotates (ADR-005: every membership change).
	spaceA.AddMember(bob.Device.ID, bob.Device.X25519Pub)
	if _, err := alice.RotateEpoch(spaceA); err != nil {
		t.Fatal(err)
	}
	if _, err := human.Say(alice, spaceA, "bob, you should hear this", human.SayOptions{}, 200); err != nil {
		t.Fatal(err)
	}
	pipe(t, spaceA, spaceB)
	if len(spaceB.State.Messages()) != 2 {
		t.Fatal("member missed post-join message")
	}

	// Bob writes back; alice reads it.
	if _, err := human.Say(bob, spaceB, "on my way", human.SayOptions{}, 300); err != nil {
		t.Fatal(err)
	}
	pipe(t, spaceB, spaceA)
	if len(spaceA.State.Messages()) != 3 {
		t.Fatal("owner missed member message")
	}

	// Removal: bob is dropped, epoch rotates, future messages are dark to
	// him — but what he already decrypted honestly stays readable.
	spaceA.RemoveMember(bob.Device.ID)
	if _, err := alice.RotateEpoch(spaceA); err != nil {
		t.Fatal(err)
	}
	if _, err := human.Say(alice, spaceA, "post-removal message", human.SayOptions{}, 400); err != nil {
		t.Fatal(err)
	}
	pipe(t, spaceA, spaceB)
	msgsB := spaceB.State.Messages()
	if len(msgsB) != 3 {
		t.Fatalf("removed member should keep 3 old messages, has %d", len(msgsB))
	}
	for _, m := range msgsB {
		if strings.Contains(m.Text, "post-removal") {
			t.Fatal("removed member read a post-removal message")
		}
	}
	if spaceB.Undecryptable != 1 {
		t.Fatalf("removed member should count 1 undecryptable, got %d", spaceB.Undecryptable)
	}
	// The owner still reads everything.
	if len(spaceA.State.Messages()) != 4 {
		t.Fatal("owner state wrong after rotation")
	}
}

func TestInviteSecurity(t *testing.T) {
	alice, _ := human.New("alice")
	spaceA, err := terminals.NewSpace("Forest Session", alice.Principal)
	if err != nil {
		t.Fatal(err)
	}
	spaceA.EnablePrivate(alice.Device)
	spaceA.AddMember(alice.Device.ID, alice.Device.X25519Pub)
	if _, err := alice.RotateEpoch(spaceA); err != nil {
		t.Fatal(err)
	}

	bob, _ := human.New("bob")
	mallory, _ := human.New("mallory")

	invite, err := spaceA.NewInvite(bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	// A stolen invite is useless to another device.
	if _, err := terminals.AcceptInvite(invite, mallory.Device); err == nil {
		t.Fatal("invite accepted by a different device")
	}
	// A tampered invite fails signature verification.
	mut := append([]byte(nil), invite...)
	mut[10] ^= 1
	if _, err := terminals.AcceptInvite(mut, bob.Device); err == nil {
		t.Fatal("tampered invite accepted")
	}
	// Non-controller replicas cannot mint invites.
	replica := terminals.Replica(spaceA.ID)
	if _, err := replica.NewInvite(bob.Device.ID, bob.Device.X25519Pub); err == nil {
		t.Fatal("non-controller minted an invite")
	}
}

func TestNonMemberCannotWrite(t *testing.T) {
	alice, _ := human.New("alice")
	spaceA, err := terminals.NewSpace("Forest Session", alice.Principal)
	if err != nil {
		t.Fatal(err)
	}
	spaceA.EnablePrivate(alice.Device)
	spaceA.AddMember(alice.Device.ID, alice.Device.X25519Pub)
	if _, err := alice.RotateEpoch(spaceA); err != nil {
		t.Fatal(err)
	}

	// An outsider replica of the same private space has no epoch key: the
	// runtime refuses to emit (no key — no write).
	outsider, _ := human.New("outsider")
	replica := terminals.Replica(spaceA.ID)
	replica.EnablePrivate(outsider.Device)
	pipe(t, spaceA, replica)
	if _, err := human.Say(outsider, replica, "let me in", human.SayOptions{}, 100); err == nil {
		t.Fatal("non-member wrote into a private space")
	}
}

// The rule stated where it lives, so it holds for every caller and not just
// the pass path: admitting a device that is already carried changes nothing.
func TestAcceptingAnExistingMemberIsANoOp(t *testing.T) {
	alice, err := human.New("alice")
	if err != nil {
		t.Fatal(err)
	}
	space, err := terminals.NewSpace("Line", alice.Principal)
	if err != nil {
		t.Fatal(err)
	}
	space.EnablePrivate(alice.Device)
	space.AddMember(alice.Device.ID, alice.Device.X25519Pub)
	if _, err := alice.RotateEpoch(space); err != nil {
		t.Fatal(err)
	}

	bob, err := human.New("bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := space.AcceptIntoSpace(alice, bob.Device.ID,
		bob.Device.X25519Pub, "bob", alice.Principal, 100, nil); err != nil {
		t.Fatal(err)
	}
	events, epoch := space.Log.Len(), space.CurrentEpoch()

	// The same device again — a second link, a re-sent request, a double click.
	n, key, mf, err := space.AcceptIntoSpace(alice, bob.Device.ID,
		bob.Device.X25519Pub, "bob", alice.Principal, 200, nil)
	if err != nil {
		t.Fatalf("re-admitting an existing member failed: %v", err)
	}
	if space.Log.Len() != events {
		t.Fatalf("re-admission wrote %d event(s) about somebody already here",
			space.Log.Len()-events)
	}
	if space.CurrentEpoch() != epoch {
		t.Fatalf("re-admission rotated the epoch %d → %d, re-keying the space "+
			"for everyone", epoch, space.CurrentEpoch())
	}
	// It must still answer usefully: a member who lost their copy converges.
	if n != epoch || key == ([32]byte{}) || len(mf) == 0 {
		t.Fatalf("re-admission gave nothing to converge with: epoch %d, key set %v, manifest %d bytes",
			n, key != [32]byte{}, len(mf))
	}
	if !space.HasMember(bob.Device.ID) {
		t.Fatal("bob stopped being a member")
	}
}
