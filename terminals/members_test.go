package terminals_test

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/agent"
	"github.com/drrainlab/quiet_places/terminals/human"
)

// M1.3/M1.4: member cards travel through an encrypted space and render
// honestly on the other side.
func TestMemberCardsAcrossEncryptedSpace(t *testing.T) {
	alice, err := human.New("alice")
	if err != nil {
		t.Fatal(err)
	}
	spaceA, err := terminals.NewSpace("Lab", alice.Principal)
	if err != nil {
		t.Fatal(err)
	}
	spaceA.EnablePrivate(alice.Device)
	spaceA.AddMember(alice.Device.ID, alice.Device.X25519Pub)
	if _, err := alice.RotateEpoch(spaceA); err != nil {
		t.Fatal(err)
	}

	// Alice and an AI agent publish their manifests.
	if _, sent, err := alice.PublishManifest(spaceA); err != nil || !sent {
		t.Fatalf("alice publish: %v %v", err, sent)
	}
	// Re-publishing the same revision is a no-op (no event spam).
	if _, sent, err := alice.PublishManifest(spaceA); err != nil || sent {
		t.Fatalf("republish not idempotent: %v %v", err, sent)
	}
	summarizer, err := agent.NewSummarizer()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := summarizer.PublishManifest(spaceA); err != nil {
		t.Fatal(err)
	}
	if err := human.SetPresence(alice, spaceA, "listening", 1000, 300); err != nil {
		t.Fatal(err)
	}

	// Bob joins by invite and syncs; his replica must render the cards.
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

	cards := spaceB.MemberCards(1100)
	if len(cards) != 2 {
		t.Fatalf("expected 2 member cards, got %d", len(cards))
	}
	var humanCard, agentCard *terminals.MemberCard
	for i := range cards {
		switch cards[i].Kind {
		case "human":
			humanCard = &cards[i]
		case "agent":
			agentCard = &cards[i]
		}
	}
	if humanCard == nil || agentCard == nil {
		t.Fatalf("cards missing kinds: %+v", cards)
	}

	// Human card: presence current at t=1100, stale at t=2000.
	if !humanCard.Presence.Known || !humanCard.Presence.Current || humanCard.Presence.State != "listening" {
		t.Fatalf("human presence wrong: %+v", humanCard.Presence)
	}
	stale := spaceB.MemberCards(2000)
	for _, c := range stale {
		if c.Kind == "human" {
			if c.Presence.Current {
				t.Fatal("stale presence shown as current")
			}
			if c.Presence.AgeSeconds != 1000 {
				t.Fatalf("presence age wrong: %d", c.Presence.AgeSeconds)
			}
		}
	}

	// Agent card: AI is a hard fact of the manifest; model stays unknown.
	if !agentCard.AIPresent || agentCard.Agency != "ai_agent" {
		t.Fatal("agent card hides AI participation")
	}
	if agentCard.ModelDeclared {
		t.Fatal("model shown as declared when it is not")
	}
	if agentCard.Autonomy != manifest.AutonomyDelegated {
		t.Fatalf("autonomy wrong: %v", agentCard.Autonomy)
	}
	if agentCard.CanReceiveCommands {
		t.Fatal("agent without command surface shown as commandable")
	}
	// sys labels are locally derived, not self-assigned.
	foundSys := false
	for _, l := range agentCard.SysLabels {
		if l == "sys.agency.ai_agent" {
			foundSys = true
		}
	}
	if !foundSys {
		t.Fatalf("sys labels missing: %v", agentCard.SysLabels)
	}
}
