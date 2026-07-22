package manifest

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/capability"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func testTerminalKey(t *testing.T, seed byte) (ed25519.PrivateKey, id.TerminalID) {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	var term id.TerminalID
	copy(term[:], priv.Public().(ed25519.PublicKey))
	return priv, term
}

func sensorManifest(t *testing.T) (*Manifest, ed25519.PrivateKey) {
	t.Helper()
	priv, term := testTerminalKey(t, 3)
	var controller id.PrincipalID
	controller[0] = 0xCC
	return &Manifest{
		Terminal:       term,
		Controller:     controller,
		Kind:           KindSensor,
		DeclaredLabels: []string{"environment", "greenhouse", "temperature"},
		IOMode:         IOSourceOnly,
		Capabilities:   []string{capability.SignalPublish, capability.PresencePublish},
		Publishes:      []string{"observation.temperature.v1"},
		AgencyMode:     AgencyDeterministic,
		RetentionSecs:  3600,
		AnnounceTTL:    300,
		Revision:       1,
	}, priv
}

func TestManifestSignDecodeVerify(t *testing.T) {
	m, priv := sensorManifest(t)
	frame, err := m.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindSensor || got.IOMode != IOSourceOnly || got.Revision != 1 {
		t.Fatalf("decode mismatch: %+v", got)
	}
	if err := VerifyFrame(frame, got); err != nil {
		t.Fatal(err)
	}
	// Deterministic re-encode.
	frame2, err := m.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frame, frame2) {
		t.Fatal("manifest encoding not deterministic")
	}
}

func TestSourceOnlyCannotDeclareCommandSurface(t *testing.T) {
	m, priv := sensorManifest(t)
	m.Capabilities = append(m.Capabilities, capability.CommandReceive)
	if _, err := m.Sign(priv); err == nil {
		t.Fatal("source_only with command.receive was accepted")
	}
	m2, priv2 := sensorManifest(t)
	m2.Commands = []string{"actuator.switch.v1"}
	if _, err := m2.Sign(priv2); err == nil {
		t.Fatal("source_only with command schemas was accepted")
	}
}

func TestCommandsRequireCapability(t *testing.T) {
	priv, term := testTerminalKey(t, 4)
	var controller id.PrincipalID
	m := &Manifest{
		Terminal:   term,
		Controller: controller,
		Kind:       KindActuator,
		IOMode:     IODuplex,
		Commands:   []string{"actuator.switch.v1"},
		Revision:   1,
	}
	if _, err := m.Sign(priv); err == nil {
		t.Fatal("command schemas without command.receive were accepted")
	}
	m.Capabilities = []string{capability.CommandReceive, capability.CommandExecute, capability.SignalReceive}
	if _, err := m.Sign(priv); err != nil {
		t.Fatal(err)
	}
}

func TestA4Rejected(t *testing.T) {
	m, priv := sensorManifest(t)
	m.Autonomy = AutonomyPhysicalControl
	if _, err := m.Sign(priv); err == nil {
		t.Fatal("autonomy A4 was accepted (must be rejected in v0)")
	}
}

func TestAIAgentRequiresAIPresent(t *testing.T) {
	m, priv := sensorManifest(t)
	m.AgencyMode = AgencyAIAgent
	m.AIPresent = false
	if _, err := m.Sign(priv); err == nil {
		t.Fatal("ai_agent without ai_present was accepted")
	}
}

func TestRevisionChain(t *testing.T) {
	m, priv := sensorManifest(t)
	frame1, err := m.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	h := Hash(frame1)
	m2, _ := sensorManifest(t)
	m2.Revision = 2
	m2.Previous = &h
	m2.DeclaredLabels = append(m2.DeclaredLabels, "calibrated")
	frame2, err := m2.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(frame2)
	if err != nil {
		t.Fatal(err)
	}
	if got.Previous == nil || *got.Previous != h {
		t.Fatal("revision chain broken")
	}
	// Capability diff across revisions.
	added, removed := capability.NewSet(got.Capabilities...).Diff(capability.NewSet(m.Capabilities...))
	if len(added) != 0 || len(removed) != 0 {
		t.Fatalf("unexpected capability diff: +%v -%v", added, removed)
	}
}
