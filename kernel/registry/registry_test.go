package registry

import (
	"bytes"
	"crypto/ed25519"
	"slices"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/capability"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
)

func terminalKey(t *testing.T, seed byte) (ed25519.PrivateKey, id.TerminalID) {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	var tid id.TerminalID
	copy(tid[:], priv.Public().(ed25519.PublicKey))
	return priv, tid
}

func sensorFrame(t *testing.T, seed byte) ([]byte, ed25519.PrivateKey, id.TerminalID) {
	t.Helper()
	priv, tid := terminalKey(t, seed)
	m := &manifest.Manifest{
		Terminal:      tid,
		Controller:    id.PrincipalID{0xCC},
		Kind:          manifest.KindSensor,
		IOMode:        manifest.IOSourceOnly,
		Capabilities:  []string{capability.SignalPublish},
		Publishes:     []string{"observation.temperature.v1"},
		AgencyMode:    manifest.AgencyDeterministic,
		RetentionSecs: 3600,
		Revision:      1,
	}
	frame, err := m.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	return frame, priv, tid
}

func actuatorFrame(t *testing.T, seed byte) ([]byte, id.TerminalID) {
	t.Helper()
	priv, tid := terminalKey(t, seed)
	m := &manifest.Manifest{
		Terminal:   tid,
		Controller: id.PrincipalID{0xDD},
		Kind:       manifest.KindActuator,
		IOMode:     manifest.IODuplex,
		Capabilities: []string{capability.SignalReceive,
			capability.CommandReceive, capability.CommandExecute},
		Commands: []string{"actuator.switch.v1"},
		Revision: 1,
	}
	frame, err := m.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	return frame, tid
}

func TestSourceOnlySensorCannotReceiveCommands(t *testing.T) {
	r := New()
	frame, _, tid := sensorFrame(t, 3)
	if _, err := r.Upsert(frame); err != nil {
		t.Fatal(err)
	}
	// M0.3 acceptance: the sensor is structurally not a command receiver.
	if err := r.AuthorizeCommand(tid, "actuator.switch.v1"); err == nil {
		t.Fatal("source-only sensor authorized as command receiver")
	}
	if err := r.AuthorizeSend(tid, "message.text.v1"); err == nil {
		t.Fatal("source-only sensor authorized as message sink")
	}
}

func TestActuatorCommandAuthorization(t *testing.T) {
	r := New()
	frame, tid := actuatorFrame(t, 4)
	if _, err := r.Upsert(frame); err != nil {
		t.Fatal(err)
	}
	if err := r.AuthorizeCommand(tid, "actuator.switch.v1"); err != nil {
		t.Fatal(err)
	}
	// Undeclared command schema fails even with command.receive.
	if err := r.AuthorizeCommand(tid, "actuator.selfdestruct.v1"); err == nil {
		t.Fatal("undeclared command schema authorized")
	}
	// Unknown terminal fails closed.
	if err := r.AuthorizeCommand(id.TerminalID{0xFF}, "actuator.switch.v1"); err == nil {
		t.Fatal("unknown terminal authorized")
	}
}

func TestRevisionChain(t *testing.T) {
	r := New()
	frame1, priv, _ := sensorFrame(t, 5)
	t1, err := r.Upsert(frame1)
	if err != nil {
		t.Fatal(err)
	}
	h := t1.ManifestHash

	m2 := *t1.Manifest
	m2.Revision = 2
	m2.Previous = &h
	m2.DeclaredLabels = []string{"calibrated"}
	frame2, err := m2.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	t2, err := r.Upsert(frame2)
	if err != nil {
		t.Fatal(err)
	}
	if t2.Manifest.Revision != 2 {
		t.Fatal("revision not updated")
	}
	if _, ok := t2.Observed["observed.manifest_changed"]; !ok {
		t.Fatal("manifest change not observed")
	}

	// Re-ingesting revision 2 is idempotent.
	if _, err := r.Upsert(frame2); err != nil {
		t.Fatal(err)
	}
	// Stale revision rejected.
	if _, err := r.Upsert(frame1); err == nil {
		t.Fatal("stale revision accepted")
	}
	// Broken chain rejected.
	m3 := m2
	m3.Revision = 3
	bogus := id.Hash{0x99}
	m3.Previous = &bogus
	frame3, err := m3.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Upsert(frame3); err == nil {
		t.Fatal("broken revision chain accepted")
	}
}

func TestFirstManifestMustBeRevisionOne(t *testing.T) {
	r := New()
	priv, tid := terminalKey(t, 6)
	prev := id.Hash{1}
	m := &manifest.Manifest{
		Terminal:   tid,
		Controller: id.PrincipalID{0xEE},
		Kind:       manifest.KindBot,
		IOMode:     manifest.IODuplex,
		Capabilities: []string{capability.SignalPublish,
			capability.SignalReceive},
		Revision: 5,
		Previous: &prev,
	}
	frame, err := m.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Upsert(frame); err == nil {
		t.Fatal("accepted unknown terminal at revision 5")
	}
}

func TestSysLabelsDerived(t *testing.T) {
	r := New()
	frame, _, tid := sensorFrame(t, 7)
	if _, err := r.Upsert(frame); err != nil {
		t.Fatal(err)
	}
	term, _ := r.Get(tid)
	labels := term.SysLabels()
	for _, want := range []string{"sys.kind.sensor", "sys.io.source_only",
		"sys.agency.deterministic", "sys.storage.ephemeral"} {
		if !slices.Contains(labels, want) {
			t.Errorf("missing sys label %q in %v", want, labels)
		}
	}
}

func TestCapabilityDiff(t *testing.T) {
	r := New()
	frame1, priv, tid := sensorFrame(t, 8)
	t1, err := r.Upsert(frame1)
	if err != nil {
		t.Fatal(err)
	}
	h := t1.ManifestHash
	m2 := *t1.Manifest
	m2.Revision = 2
	m2.Previous = &h
	m2.IOMode = manifest.IODuplex
	m2.Capabilities = []string{capability.SignalPublish, capability.SignalReceive}
	frame2, err := m2.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	added, removed, err := r.CapabilityDiff(tid, frame2)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != capability.SignalReceive || len(removed) != 0 {
		t.Fatalf("diff wrong: +%v -%v", added, removed)
	}
}
