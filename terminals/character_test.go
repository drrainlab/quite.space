package terminals_test

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/human"
)

func TestCharacterRoundTripThroughManifest(t *testing.T) {
	alice, _ := human.New("alice")
	c := terminals.DefaultCharacter("studio")
	c.Mood = "night"
	c.Rituals = []string{"listening_session", "field_recording_of_the_week"}
	c.Presence = append(c.Presence, "sunday_capsule_prep")

	s, err := terminals.NewSpaceWithCharacter("Night Studio", alice.Principal.ID, c)
	if err != nil {
		t.Fatal(err)
	}
	title, got := s.Character()
	if title != "Night Studio" {
		t.Fatalf("title lost: %q", title)
	}
	if got.Archetype != "studio" || got.Mood != "night" || got.Central != "audio" ||
		got.Relic != "waveform" || len(got.Rituals) != 2 {
		t.Fatalf("character mangled: %+v", got)
	}
	// The character survives to invited replicas via the manifest frame.
	s.EnablePrivate(alice.Device)
	s.AddMember(alice.Device.ID, alice.Device.X25519Pub)
	if _, err := alice.RotateEpoch(s); err != nil {
		t.Fatal(err)
	}
	bob, _ := human.New("bob")
	invite, err := s.NewInvite(bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	replica, err := terminals.AcceptInvite(invite, bob.Device)
	if err != nil {
		t.Fatal(err)
	}
	rTitle, rc := replica.Character()
	if rTitle != "Night Studio" || rc.Archetype != "studio" || rc.Mood != "night" {
		t.Fatalf("replica sees different character: %q %+v", rTitle, rc)
	}
}

func TestPresenceCannotImpersonateSystem(t *testing.T) {
	for _, bad := range []string{"online", "Verified", "message delivered", "ADMIN", "trusted friend"} {
		if err := terminals.ValidatePresenceState(bad); err == nil {
			t.Errorf("presence state %q accepted — impersonates system properties", bad)
		}
	}
	for _, good := range []string{"mixing_a_track", "in_the_shop", "reading_not_replying", "сегодня без слов"} {
		if err := terminals.ValidatePresenceState(good); err != nil {
			t.Errorf("presence state %q rejected: %v", good, err)
		}
	}
	// A character carrying an impersonating custom status is refused whole.
	c := terminals.DefaultCharacter("campfire")
	c.Presence = append(c.Presence, "always online")
	if err := c.Validate(); err == nil {
		t.Fatal("character with impersonating presence accepted")
	}
}

func TestPrivateHistoryInvite(t *testing.T) {
	alice, _ := human.New("alice")
	c := terminals.DefaultCharacter("home")
	c.Memory = "private_history"
	s, err := terminals.NewSpaceWithCharacter("Family", alice.Principal.ID, c)
	if err != nil {
		t.Fatal(err)
	}
	s.EnablePrivate(alice.Device)
	s.AddMember(alice.Device.ID, alice.Device.X25519Pub)
	if _, err := alice.RotateEpoch(s); err != nil {
		t.Fatal(err)
	}
	if _, err := human.Say(alice, s, "before bob existed", human.SayOptions{}, 100); err != nil {
		t.Fatal(err)
	}
	// Rotate to epoch 2, then invite WITHOUT history.
	if _, err := alice.RotateEpoch(s); err != nil {
		t.Fatal(err)
	}
	if _, err := human.Say(alice, s, "the present day", human.SayOptions{}, 200); err != nil {
		t.Fatal(err)
	}
	bob, _ := human.New("bob")
	invite, err := s.NewInviteWithHistory(bob.Device.ID, bob.Device.X25519Pub, false)
	if err != nil {
		t.Fatal(err)
	}
	replica, err := terminals.AcceptInvite(invite, bob.Device)
	if err != nil {
		t.Fatal(err)
	}
	pipe(t, s, replica)
	msgs := replica.State.Messages()
	if len(msgs) != 1 || msgs[0].Text != "the present day" {
		t.Fatalf("newcomer sees the wrong slice of history: %+v", msgs)
	}
	for _, m := range msgs {
		if strings.Contains(m.Text, "before bob") {
			t.Fatal("private history leaked to newcomer")
		}
	}
	// The closed past is counted honestly, not hidden.
	if replica.Undecryptable != 1 {
		t.Fatalf("undecryptable count wrong: %d", replica.Undecryptable)
	}
}
