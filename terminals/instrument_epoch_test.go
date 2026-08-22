package terminals_test

// The Instrument Plane's founding gate (QI-0, ADR-025), as a test matrix:
//
//	                conversation epoch    instrument epoch
//	member                 ✓                     ✓
//	instrument             ✗ (cannot)            ✓
//	relay                  ✗ ciphertext          ✗ ciphertext
//
// Plus the two lifecycle halves the owner's review pinned: a DETACHED
// instrument stops reading after the turn, and the lineage survives a
// restart via export/restore.

import (
	"crypto/sha256"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/crypto"
	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/kernel/reducers"
	"github.com/drrainlab/quiet_places/protocol/capability"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/human"
)

func instrumentTemplate(label string) manifest.Manifest {
	return manifest.Manifest{
		Kind:           manifest.KindSensor,
		DeclaredLabels: []string{label},
		IOMode:         manifest.IOSourceOnly,
		Capabilities:   []string{capability.SignalPublish},
		Publishes:      []string{schemas.ObservationValue},
		AgencyMode:     manifest.AgencyDeterministic,
		RetentionSecs:  3600,
		AnnounceTTL:    300,
	}
}

// instrumentStand: an owner with a private space, an instrument device in
// the INSTRUMENT wrap list only, replicas for the instrument and for a
// keyless relay view.
func instrumentStand(t *testing.T) (owner *terminals.Participant, home *terminals.Space,
	instr *terminals.Participant, instrSpace *terminals.Space, relayView *terminals.Space) {
	t.Helper()
	var err error
	owner, err = human.New("owner")
	if err != nil {
		t.Fatal(err)
	}
	home, err = terminals.NewSpace("greenhouse room", owner.Principal)
	if err != nil {
		t.Fatal(err)
	}
	home.EnablePrivate(owner.Device)
	home.AddMember(owner.Device.ID, owner.Device.X25519Pub)

	instrDev, err := identity.NewDevice(identity.NewRand())
	if err != nil {
		t.Fatal(err)
	}
	home.AddInstrument(instrDev.ID, instrDev.X25519Pub)

	if _, err := owner.RotateEpoch(home); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.RotateInstrumentEpoch(home); err != nil {
		t.Fatal(err)
	}

	instr, _, err = terminals.NewParticipantFrom(owner.Principal, instrDev, nil,
		instrumentTemplate("greenhouse"))
	if err != nil {
		t.Fatal(err)
	}
	instrSpace = terminals.Replica(home.ID)
	instrSpace.EnablePrivate(instrDev)
	pipe(t, home, instrSpace)

	relayView = terminals.Replica(home.ID)
	pipe(t, home, relayView)
	return
}

func reading(t *testing.T, magnitude uint64, at uint64) []byte {
	t.Helper()
	b, err := (&schemas.ValueObservation{
		Channel: "temperature", HasNumber: true, Magnitude: magnitude, Decimals: 1,
		ObservedAt: at, StaleAfter: 600, Simulated: true,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTheInstrumentPlaneAccessMatrix(t *testing.T) {
	owner, home, instr, instrSpace, relayView := instrumentStand(t)

	// A human word, sealed to the conversation.
	if _, err := human.Say(owner, home, "только для людей", human.SayOptions{}, 10); err != nil {
		t.Fatal(err)
	}

	// A reading, sealed to the instrument plane, emitted through the
	// instrument's OWN replica.
	if _, err := instr.Emit(instrSpace, schemas.ObservationValue, reading(t, 214, 100),
		signal.AuthorshipSensor, 11); err != nil {
		t.Fatal(err)
	}

	// MEMBER reads both planes.
	pipe(t, instrSpace, home)
	if home.Undecryptable != 0 {
		t.Fatalf("the member could not read everything: undecryptable=%d", home.Undecryptable)
	}

	// INSTRUMENT cannot read the conversation.
	before := instrSpace.Undecryptable
	pipe(t, home, instrSpace)
	if instrSpace.Undecryptable != before+1 {
		t.Fatalf("the instrument's view of the conversation: undecryptable %d -> %d "+
			"(want exactly one more — the human message)", before, instrSpace.Undecryptable)
	}

	// RELAY reads neither plane: both new frames count as undecryptable.
	relayBefore := relayView.Undecryptable
	pipe(t, home, relayView)
	pipe(t, instrSpace, relayView)
	if relayView.Undecryptable != relayBefore+2 {
		t.Fatalf("the keyless view should gain exactly 2 undecryptable, got %d -> %d",
			relayBefore, relayView.Undecryptable)
	}
}

func TestADetachedInstrumentStopsReadingAfterTheTurn(t *testing.T) {
	owner, home, instr, instrSpace, _ := instrumentStand(t)

	// Detach and TURN THE KEY — removal without rotation changes nothing.
	home.RemoveInstrument(instr.Device.ID)
	if _, err := owner.RotateInstrumentEpoch(home); err != nil {
		t.Fatal(err)
	}
	pipe(t, home, instrSpace)

	if _, err := owner.Emit(home, schemas.ObservationValue, reading(t, 999, 200),
		signal.AuthorshipHuman, 20); err != nil {
		t.Fatal(err)
	}
	before := instrSpace.Undecryptable
	pipe(t, home, instrSpace)
	if instrSpace.Undecryptable <= before {
		t.Fatal("a detached instrument still reads observations after the rotation")
	}
}

func TestInstrumentEpochsSurviveARestart(t *testing.T) {
	_, home, _, _, _ := instrumentStand(t)
	keys := home.ExportInstrumentEpochs()
	if len(keys) == 0 {
		t.Fatal("nothing exported — the ring is empty")
	}
	fresh := terminals.Replica(home.ID)
	fresh.RestoreInstrumentEpochs(keys)
	if fresh.CurrentInstrumentEpoch() != home.CurrentInstrumentEpoch() {
		t.Fatal("the restored ring lost its head")
	}
	if !fresh.KnowsInstrumentEpoch(1) {
		t.Fatal("the restored ring lost epoch 1")
	}
}

func TestInstrumentDeclGrammar(t *testing.T) {
	decls := []terminals.ChannelDecl{
		{Channel: "temperature", Kind: "number", Unit: "°C", Label: "Температура: улица"},
		{Channel: "co2", Kind: "number", Unit: "ppm"},
		{Channel: "door", Kind: "boolean", Label: "开关: 温室"},
		{Channel: "mode", Kind: "enum", Unit: "%"},
	}
	labels, err := terminals.InstrumentLabels(decls)
	if err != nil {
		t.Fatal(err)
	}
	back := terminals.ParseInstruments(labels)
	if len(back) != len(decls) {
		t.Fatalf("roundtrip lost channels: %d != %d", len(back), len(decls))
	}
	for i := range decls {
		if back[i] != decls[i] {
			t.Fatalf("channel %d diverged: %+v != %+v", i, back[i], decls[i])
		}
	}
	// The budget refuses with its own name, not a manifest-wide mystery.
	many := make([]terminals.ChannelDecl, terminals.MaxInstrumentChannels+1)
	for i := range many {
		many[i] = terminals.ChannelDecl{
			Channel: "ch" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)),
			Kind:    "number",
		}
	}
	if _, err := terminals.InstrumentLabels(many); err == nil {
		t.Fatal("over-budget declaration was accepted")
	}
	// Tolerant read: garbage entries are skipped, never fatal.
	got := terminals.ParseInstruments([]string{"qp.instr=", "qp.instr=BAD:number", "qp.instr=ok:number"})
	if len(got) != 1 || got[0].Channel != "ok" {
		t.Fatalf("tolerant parse failed: %+v", got)
	}
}

// DETACHMENT IS A BOUNDARY OF AUTHORITY, NOT ONLY OF SECRECY (QI-M,
// owner's amendment 4). Members keep old epoch keys for delayed delivery,
// so a detached device that still holds epoch 1 can produce a perfectly
// valid, perfectly sealed observation. The receiver must refuse it anyway:
// the device is no longer addressed by the current epoch, and THAT is the
// revocation. Counted apart from Undecryptable — this is not "we lack a
// key", it is "you are not an instrument here any more".
func TestADetachedInstrumentIsRefusedEvenWithTheOldKey(t *testing.T) {
	owner, home, instr, instrSpace, _ := instrumentStand(t)

	// Before: a reading through the instrument's own replica is accepted
	// and lands in the member's reducer.
	if _, err := instr.Emit(instrSpace, schemas.ObservationValue, reading(t, 214, 100),
		signal.AuthorshipSensor, 11); err != nil {
		t.Fatal(err)
	}
	pipe(t, instrSpace, home)
	if home.UnauthorizedInstrument != 0 {
		t.Fatalf("an attached instrument was refused: %d", home.UnauthorizedInstrument)
	}
	if v, ok := home.State.ValueObservations()[reducers.ValueObsKey{
		Instrument: instr.TerminalID, Channel: "temperature"}]; !ok || v.Value.Magnitude != 214 {
		t.Fatalf("the attached reading never reduced: %+v ok=%v", v, ok)
	}
	oldKeys := instrSpace.ExportInstrumentEpochs()
	if len(oldKeys) != 1 {
		t.Fatalf("the instrument should hold exactly epoch 1, has %d", len(oldKeys))
	}

	// Detach and turn the key. The instrument learns epoch 2 exists but
	// is not addressed — and keeps epoch 1, as any replica would.
	home.RemoveInstrument(instr.Device.ID)
	if _, err := owner.RotateInstrumentEpoch(home); err != nil {
		t.Fatal(err)
	}
	pipe(t, home, instrSpace)

	// The detached device forges ahead with the key it still has: a valid
	// chain continuation, a valid signature, a valid seal under epoch 1.
	plane := sha256.Sum256(append([]byte("qp.instrument-plane.v1"), home.ID[:]...))
	var planeID id.TerminalID
	copy(planeID[:], plane[:])
	sealed, err := crypto.SealPayload(oldKeys[0], planeID, schemas.ObservationValue, reading(t, 999, 300))
	if err != nil {
		t.Fatal(err)
	}
	seq, tip, ok := home.Log.ChainTip(instr.Device.ID)
	if !ok {
		t.Fatal("the member has no chain for the instrument")
	}
	src := instr.TerminalID
	env := &signal.Envelope{
		Terminal: home.ID, Principal: instr.Principal, Device: instr.Device.ID,
		Sequence: seq + 1, Previous: &tip,
		Schema: schemas.ObservationValue, CreatedAt: 300, LogicalClock: 1000,
		ProducedBy: signal.AuthorshipSensor, SourceTerminal: &src,
		PayloadEncoding: signal.PayloadInstrumentSealed, Payload: sealed,
		Priority: signal.PriorityTelemetry, ExpiresAt: 900, MaxForwards: 1,
	}
	frame, err := env.Sign(instr.Device.SignKey())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := home.Absorb(frame); err != nil {
		t.Fatalf("the log itself should accept a well-formed frame: %v", err)
	}
	if home.UnauthorizedInstrument != 1 {
		t.Fatalf("the detached device's reading was not refused as unauthorized: %d", home.UnauthorizedInstrument)
	}
	if v := home.State.ValueObservations()[reducers.ValueObsKey{
		Instrument: instr.TerminalID, Channel: "temperature"}]; v.Value.Magnitude != 214 {
		t.Fatalf("the forged reading reached the reducer: %+v", v)
	}
}
