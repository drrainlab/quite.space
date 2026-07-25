// M0.8 acceptance: every headless terminal has a correct manifest and is
// structurally unable to perform operations it does not declare.
package terminals_test

import (
	"errors"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/agent"
	"github.com/drrainlab/quiet_places/terminals/archive"
	"github.com/drrainlab/quiet_places/terminals/bot"
	"github.com/drrainlab/quiet_places/terminals/human"
	"github.com/drrainlab/quiet_places/terminals/sensor"
	"github.com/drrainlab/quiet_places/transports/relay"
)

func newSpace(t *testing.T) *terminals.Space {
	t.Helper()
	s, err := terminals.NewSpace("Forest Session", id.PrincipalID{1})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestManifestsVerify(t *testing.T) {
	alice, err := human.New("alice")
	if err != nil {
		t.Fatal(err)
	}
	echo, err := bot.NewEcho()
	if err != nil {
		t.Fatal(err)
	}
	temp, err := sensor.NewTemperature("studio climate")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := agent.NewSummarizer()
	if err != nil {
		t.Fatal(err)
	}
	logger, err := archive.NewLogger()
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]*terminals.Participant{
		"human": alice, "bot": echo, "sensor": temp,
		"agent": sum, "logger": logger.P,
	} {
		m, err := manifest.Decode(p.ManifestFrame)
		if err != nil {
			t.Fatalf("%s manifest: %v", name, err)
		}
		if err := manifest.VerifyFrame(p.ManifestFrame, m); err != nil {
			t.Fatalf("%s manifest signature: %v", name, err)
		}
	}
}

func TestSensorCannotBeMessagedOrPublishAsHuman(t *testing.T) {
	s := newSpace(t)
	temp, err := sensor.NewTemperature("studio climate")
	if err != nil {
		t.Fatal(err)
	}
	// Publishing observations: declared, works.
	if _, err := sensor.Publish(temp, s, 2360, false, true, 1000); err != nil {
		t.Fatal(err)
	}
	// Receiving anything: undeclared.
	if temp.CanReceive() {
		t.Fatal("source-only sensor claims receive capability")
	}
	if err := temp.RequireReceive(); !errors.Is(err, terminals.ErrUndeclaredOperation) {
		t.Fatalf("wrong error: %v", err)
	}
	// Signing as human: agency forbids it.
	payload, _ := (&schemas.TextMessage{Text: "i am a person"}).Encode()
	_, err = temp.Emit(s, schemas.MessageText, payload, signal.AuthorshipHuman, 1001)
	if !errors.Is(err, terminals.ErrAuthorshipForbidden) {
		t.Fatalf("sensor signed as human: %v", err)
	}
	// The observation is honest: simulated flag survived.
	o, ok := s.State.LatestObservation()
	if !ok || !o.Value.Simulated {
		t.Fatal("simulated observation not marked simulated")
	}
}

func TestLoggerCannotPublish(t *testing.T) {
	s := newSpace(t)
	logger, err := archive.NewLogger()
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := (&schemas.TextMessage{Text: "logger speaks"}).Encode()
	_, err = logger.P.Emit(s, schemas.MessageText, payload, signal.AuthorshipDeterministicBot, 1)
	if !errors.Is(err, terminals.ErrUndeclaredOperation) {
		t.Fatalf("sink-only logger published: %v", err)
	}
}

func TestAgentCannotSignAsHuman(t *testing.T) {
	s := newSpace(t)
	sum, err := agent.NewSummarizer()
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := (&schemas.TextMessage{Text: "definitely a human"}).Encode()
	_, err = sum.Emit(s, schemas.MessageText, payload, signal.AuthorshipHuman, 1)
	if !errors.Is(err, terminals.ErrAuthorshipForbidden) {
		t.Fatalf("AI agent signed as human: %v", err)
	}
	// Its real output is marked ai_agent and human_approved=false.
	a, err := agent.Summarize(sum, s, 2)
	if err != nil {
		t.Fatal(err)
	}
	if a.Env.ProducedBy != signal.AuthorshipAIAgent || a.Env.HumanApproved {
		t.Fatal("agent output not marked honestly")
	}
}

func TestEchoBotFlow(t *testing.T) {
	s := newSpace(t)
	alice, err := human.New("alice")
	if err != nil {
		t.Fatal(err)
	}
	echo, err := bot.NewEcho()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := human.Say(alice, s, "hello bot", human.SayOptions{}, 100); err != nil {
		t.Fatal(err)
	}
	answered := map[id.EventID]bool{}
	n, err := bot.React(echo, s, answered, 101)
	if err != nil || n != 1 {
		t.Fatalf("react: %v %d", err, n)
	}
	// Deterministic: reacting again does nothing new.
	n, err = bot.React(echo, s, answered, 102)
	if err != nil || n != 0 {
		t.Fatalf("second react: %v %d", err, n)
	}
	msgs := s.State.Messages()
	if len(msgs) != 2 || msgs[1].ProducedBy != signal.AuthorshipDeterministicBot {
		t.Fatalf("echo state wrong: %+v", msgs)
	}
}

func TestPresenceHonestyEndToEnd(t *testing.T) {
	s := newSpace(t)
	alice, err := human.New("alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := human.SetPresence(alice, s, "listening", 1000, 300); err != nil {
		t.Fatal(err)
	}
	p := s.Trust.Presence(alice.TerminalID, 1100)
	if !p.Current || p.State != "listening" {
		t.Fatalf("current presence wrong: %+v", p)
	}
	p = s.Trust.Presence(alice.TerminalID, 2000)
	if p.Current {
		t.Fatal("expired presence still current")
	}
	if p.AgeSeconds != 1000 {
		t.Fatalf("age wrong: %d", p.AgeSeconds)
	}
}

func TestBlindRelayStoreAndForward(t *testing.T) {
	store := relay.NewStore(16, 1<<16)
	item := relay.Item{DestinationHint: "hint-rotating-abc", ExpiresAt: 2000,
		Ciphertext: []byte{0xde, 0xad, 0xbe, 0xef}}
	if !store.Put(item) {
		t.Fatal("relay refused item")
	}
	// Quota: the 17th DISTINCT item for one hint is refused. (Identical
	// bytes are idempotent by design since PA-0 — they never consume a
	// second slot, so the flood must vary.)
	for i := 0; i < 16; i++ {
		store.Put(relay.Item{DestinationHint: "flood", ExpiresAt: 2000, Ciphertext: []byte{1, byte(i)}})
	}
	if store.Put(relay.Item{DestinationHint: "flood", ExpiresAt: 2000, Ciphertext: []byte{2, 0}}) {
		t.Fatal("quota not enforced")
	}
	// Collect before expiry: delivered once, then gone.
	got := store.Collect("hint-rotating-abc", 1500)
	if len(got) != 1 || got[0][0] != 0xde {
		t.Fatalf("collect wrong: %v", got)
	}
	if len(store.Collect("hint-rotating-abc", 1500)) != 0 {
		t.Fatal("item delivered twice")
	}
	// Expiry is unconditional: only the "late" item is past TTL at t=200.
	store.Put(relay.Item{DestinationHint: "late", ExpiresAt: 100, Ciphertext: []byte{2}})
	if n := store.Expire(200); n != 1 {
		t.Fatalf("expected 1 expired item, got %d", n)
	}
	if len(store.Collect("late", 200)) != 0 {
		t.Fatal("expired item delivered")
	}
}
