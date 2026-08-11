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

// A space's presence vocabulary arrives inside somebody else's manifest,
// and manifest.Validate bounds the label COUNT, not what is in a label.
// Character.Validate holds the honesty rule but runs only on WRITE, so
// until admissiblePresence existed the read path accepted anything.
func TestReadingAPresenceVocabularyDropsWhatItMayNotShow(t *testing.T) {
	labels := []string{"a space", "qp.archetype=campfire",
		// "verified" and "system" impersonate protocol facts; "always
		// online" hides one inside a phrase, which is why the rule
		// matches per word token rather than on the whole string.
		"qp.presence=around,verified,listening,system,always online,busy"}
	_, c := terminals.ParseCharacter(labels)

	for _, bad := range []string{"verified", "system", "always online"} {
		for _, got := range c.Presence {
			if got == bad {
				t.Errorf("read a presence state that impersonates a protocol fact: %q", bad)
			}
		}
	}
	want := []string{"around", "listening", "busy"}
	if len(c.Presence) != len(want) {
		t.Fatalf("kept %v, want the honest ones %v", c.Presence, want)
	}
	for i := range want {
		if c.Presence[i] != want[i] {
			t.Errorf("kept %v, want %v", c.Presence, want)
			break
		}
	}
	// What survives the read must also pass the write rule — otherwise the
	// two halves disagree and one of them is decoration.
	if err := c.Validate(); err != nil {
		t.Errorf("a character read off the wire does not validate: %v", err)
	}
}

// Dropping states, never the space: refusing a whole manifest over one bad
// word would make content disappear, which is the wrong failure direction
// on a read path.
func TestASpaceOfNothingButReservedWordsSimplyHasNoVocabulary(t *testing.T) {
	_, c := terminals.ParseCharacter([]string{"a space", "qp.presence=online,offline,admin"})
	if len(c.Presence) != 0 {
		t.Errorf("kept %v, want nothing", c.Presence)
	}
	if c.Archetype == "" {
		t.Error("the rest of the character was lost with the vocabulary")
	}
}

// A hostile manifest must not be able to hand every reader an unbounded
// menu to build.
func TestAnOversizedVocabularyIsCutToTheBound(t *testing.T) {
	many := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		many = append(many, "state_"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	_, c := terminals.ParseCharacter([]string{"s", "qp.presence=" + strings.Join(many, ",")})
	if len(c.Presence) > 12 {
		t.Errorf("kept %d states, bound is %d", len(c.Presence), 12)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("the cut vocabulary does not validate: %v", err)
	}
}
