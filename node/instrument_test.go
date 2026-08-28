package node

// QI-1's gate: the reference greenhouse is attached by the owner, its
// readings cross a real relay SEALED TO THE INSTRUMENT PLANE, and every
// member's reducer holds the exact deterministic values — the clock is
// driven by hand, so there is nothing to sleep on and nothing to flake.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/kernel/reducers"
	"github.com/drrainlab/quiet_places/protocol/enrollment"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/instrument"
)

func TestTheGreenhouseReachesEveryMember(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	setPersonalRelay(t, owner, addr)
	owner.applyRelaySync("", 0)
	tid, err := owner.CreateSpace("cabin")
	if err != nil {
		t.Fatal(err)
	}

	guest := openRuntime(t, t.TempDir(), "guest")
	defer guest.Close()
	setPersonalRelay(t, guest, addr)
	guest.applyRelaySync("", 0)
	pass, err := owner.MintPass(tid, 1, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := guest.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, guest, req, JoinReady)

	// The greenhouse, deterministic: seed 1, ticks driven by hand.
	iid, err := owner.AttachSimulatedInstrument(tid, "Greenhouse", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	owner.mu.Lock()
	ir := owner.instruments[iid]
	owner.mu.Unlock()
	if ir == nil {
		t.Fatal("the attached instrument is not in the runtime")
	}
	// Stop the live ticker — this test owns the clock.
	close(ir.stop)

	at := uint64(time.Now().Unix())
	if err := owner.emitSimTick(ir, 1, at); err != nil {
		t.Fatal(err)
	}

	// The reading's frame is sealed to the INSTRUMENT plane on the wire.
	var sawSealed bool
	_ = owner.withSpace(tid, func(st *spaceState) error {
		return st.space.Log.Replay(func(a eventlog.Applied) error {
			if a.Env.Schema == schemas.ObservationValue {
				if a.Env.PayloadEncoding != signal.PayloadInstrumentSealed {
					t.Fatalf("a reading rode encoding %d, not the instrument seal",
						a.Env.PayloadEncoding)
				}
				sawSealed = true
			}
			return nil
		})
	})
	if !sawSealed {
		t.Fatal("no observation frame found in the owner's log")
	}

	// Across the relay: owner pushes, guest pulls, the guest's reducer
	// holds the exact deterministic values.
	owner.relaySyncOnce(addr)
	if _, err := guest.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	tempDeci, humidDeci, door, light := simValues(1, 1)
	if err := guest.withSpace(tid, func(st *spaceState) error {
		vos := st.space.State.ValueObservations()
		get := func(ch string) (schemas.ValueObservation, bool) {
			for k, v := range vos {
				if k.Instrument == iid && k.Channel == ch {
					return v.Value, true
				}
			}
			return schemas.ValueObservation{}, false
		}
		temp, ok := get("temperature")
		if !ok {
			t.Fatal("the guest never received the temperature")
		}
		if int64(temp.Magnitude) != tempDeci || temp.Decimals != 1 || !temp.Simulated {
			t.Fatalf("temperature diverged from the deterministic driver: %+v want %d", temp, tempDeci)
		}
		hum, _ := get("humidity")
		if int64(hum.Magnitude) != humidDeci {
			t.Fatalf("humidity diverged: %+v want %d", hum, humidDeci)
		}
		d, _ := get("door")
		if d.BoolValue != door {
			t.Fatalf("door diverged: %v want %v", d.BoolValue, door)
		}
		l, _ := get("light")
		if l.Magnitude != light {
			t.Fatalf("light diverged: %d want %d", l.Magnitude, light)
		}
		if st.space.Undecryptable != 0 {
			t.Fatalf("a member could not read the plane: undecryptable=%d", st.space.Undecryptable)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestInstrumentsSurviveARuntimeRestart(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "keeper")
	tid, err := rt.CreateSpace("cabin")
	if err != nil {
		t.Fatal(err)
	}
	iid, err := rt.AttachSimulatedInstrument(tid, "Greenhouse", 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	rt.Close()

	rt2 := openRuntime(t, dir, "keeper")
	defer rt2.Close()
	rt2.mu.Lock()
	ir := rt2.instruments[iid]
	rt2.mu.Unlock()
	if ir == nil {
		t.Fatal("the instrument did not survive the restart")
	}
	close(ir.stop) // this test owns the clock too
	if !ir.rec.Simulated || ir.rec.SimSeed != 7 {
		t.Fatalf("the record lost its driver identity: %+v", ir.rec)
	}
	// And it still speaks: the instrument epoch came back with it.
	if err := rt2.emitSimTick(ir, 2, uint64(time.Now().Unix())); err != nil {
		t.Fatalf("a restarted greenhouse cannot publish: %v", err)
	}
}

// ---- external instruments (QI-M0, ADR-026) ----

// externalDevice is what a firmware is: keys minted where they stay, a
// manifest signed by its own terminal key, a replica of the space that
// holds NO conversation key, and an enrollment proving all three belong
// together. The test plays the device by hand.
type externalDevice struct {
	dev      *identity.Device
	part     *terminals.Participant
	termSeed []byte
	replica  *terminals.Space
	enroll   []byte
}

func newExternalDevice(t *testing.T, prin id.PrincipalID, space id.TerminalID, label string) *externalDevice {
	t.Helper()
	dev, err := identity.NewDevice(identity.NewRand())
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := instrument.Template("sensor", label, greenhouseChannels())
	if err != nil {
		t.Fatal(err)
	}
	part, termSeed, err := terminals.NewParticipantFrom(prin, dev, nil, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	e := &enrollment.Enrollment{Device: dev.ID, X25519Pub: dev.X25519Pub, Terminal: part.TerminalID,
		ManifestFrame: part.ManifestFrame, ManifestHash: sha256.Sum256(part.ManifestFrame), Label: label}
	e.Nonce[0] = 1
	if err := e.Sign(dev.SignKey(), ed25519.NewKeyFromSeed(termSeed)); err != nil {
		t.Fatal(err)
	}
	enroll, err := e.Encode()
	if err != nil {
		t.Fatal(err)
	}
	replica := terminals.Replica(space)
	replica.EnablePrivate(dev)
	return &externalDevice{dev: dev, part: part, termSeed: termSeed, replica: replica, enroll: enroll}
}

// provision absorbs the freight: the device learns the current instrument
// epoch and nothing else.
func (d *externalDevice) provision(t *testing.T, prov []byte) {
	t.Helper()
	p, err := enrollment.DecodeProvision(prov)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range p.EpochFrames {
		if err := d.replica.AbsorbInstrumentEpochFrame(f, p.Principal); err != nil {
			t.Fatalf("device cannot absorb the epoch frame: %v", err)
		}
	}
	if !d.replica.KnowsInstrumentEpoch(d.replica.CurrentInstrumentEpoch()) {
		t.Fatal("the provision did not give the device the current instrument key")
	}
}

// reading emits one temperature through the device's own replica and
// returns the frame — what would cross the wire.
func (d *externalDevice) reading(t *testing.T, deci uint64, at uint64) []byte {
	t.Helper()
	payload, err := (&schemas.ValueObservation{Channel: "temperature", HasNumber: true,
		Magnitude: deci, Decimals: 1, ObservedAt: at, StaleAfter: 60}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	a, err := d.part.Emit(d.replica, schemas.ObservationValue, payload, signal.AuthorshipSensor, at)
	if err != nil {
		t.Fatal(err)
	}
	return a.Frame
}

func guestTemperature(t *testing.T, rt *Runtime, tid, iid id.TerminalID) (uint64, bool) {
	t.Helper()
	var mag uint64
	var ok bool
	_ = rt.withSpace(tid, func(st *spaceState) error {
		v, found := st.space.State.ValueObservations()[reducers.ValueObsKey{Instrument: iid, Channel: "temperature"}]
		mag, ok = v.Value.Magnitude, found
		return nil
	})
	return mag, ok
}

func TestAnExternalInstrumentEnrollsWithItsOwnKeys(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	ownerDir := t.TempDir()
	owner := openRuntime(t, ownerDir, "owner")
	setPersonalRelay(t, owner, addr)
	owner.applyRelaySync("", 0)
	owner.DevIngest = true
	tid, err := owner.CreateSpace("cabin")
	if err != nil {
		t.Fatal(err)
	}
	guest := openRuntime(t, t.TempDir(), "guest")
	defer guest.Close()
	setPersonalRelay(t, guest, addr)
	guest.applyRelaySync("", 0)
	pass, err := owner.MintPass(tid, 1, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := guest.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, guest, req, JoinReady)

	// The device enrolls; the authority never sees a private key.
	ext := newExternalDevice(t, owner.PrincipalID, tid, "Heltec")
	prov, iid, err := owner.AttachInstrumentByEnrollment(tid, ext.enroll, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if iid != ext.part.TerminalID {
		t.Fatal("instrument id is not the device's terminal")
	}
	ext.provision(t, prov)
	if ext.replica.KnowsEpoch(ext.replica.CurrentEpoch()) && ext.replica.CurrentEpoch() != 0 {
		t.Fatal("the device learned a CONVERSATION epoch key")
	}

	// Its reading, handed in at the dev door, reaches the owner's panel
	// and — across the relay — the guest's.
	now := uint64(time.Now().Unix())
	frame := ext.reading(t, 217, now)
	if n, err := owner.IngestInstrumentFrames(tid, iid, [][]byte{frame}); err != nil || n != 1 {
		t.Fatalf("ingest: n=%d err=%v", n, err)
	}
	if mag, ok := guestTemperature(t, owner, tid, iid); !ok || mag != 217 {
		t.Fatalf("owner reducer: %d %v", mag, ok)
	}
	owner.relaySyncOnce(addr)
	if _, err := guest.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	if mag, ok := guestTemperature(t, guest, tid, iid); !ok || mag != 217 {
		t.Fatalf("guest reducer: %d %v", mag, ok)
	}
	// The guest also has the device's MANIFEST — carried by the owner.
	if err := guest.withSpace(tid, func(st *spaceState) error {
		if _, ok := st.space.Registry.Get(iid); !ok {
			return errors.New("guest never received the instrument's manifest")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The door is bound to this instrument: a frame from another certified
	// device is refused even though the log would admit it.
	if _, err := owner.IngestInstrumentFrames(tid, iid, [][]byte{frame[:0]}); err == nil {
		t.Fatal("garbage accepted at the dev door")
	}
	other := newExternalDevice(t, owner.PrincipalID, tid, "Other")
	provOther, _, err := owner.AttachInstrumentByEnrollment(tid, other.enroll, 1000)
	if err != nil {
		t.Fatal(err)
	}
	other.provision(t, provOther)
	// That attach ROTATED the plane: the first device must learn the new
	// epoch before it can speak again — exactly what a bearer will carry.
	epochs, err := owner.ExternalInstrumentEpochFrames(tid)
	if err != nil || len(epochs) != 1 {
		t.Fatalf("epoch frames: %v %d", err, len(epochs))
	}
	if err := ext.replica.AbsorbInstrumentEpochFrame(epochs[0], owner.PrincipalID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.IngestInstrumentFrames(tid, iid, [][]byte{other.reading(t, 1, now+1)}); err == nil {
		t.Fatal("a frame from a different instrument passed through this instrument's door")
	}

	// IDEMPOTENT: the same enrollment again returns a provision and changes
	// nothing — no new certificate, no new epoch, no second record.
	epochBefore := 0
	_ = owner.withSpace(tid, func(st *spaceState) error { epochBefore = int(st.space.CurrentInstrumentEpoch()); return nil })
	certsBefore, recsBefore := len(owner.ks.Certs), len(owner.ks.Instruments)
	prov2, iid2, err := owner.AttachInstrumentByEnrollment(tid, ext.enroll, 2000)
	if err != nil || iid2 != iid {
		t.Fatalf("re-enroll: %v", err)
	}
	p2, _ := enrollment.DecodeProvision(prov2)
	p1, _ := enrollment.DecodeProvision(prov)
	if !bytes.Equal(p1.CertFrame, p2.CertFrame) {
		t.Fatal("re-enrolling minted a second certificate")
	}
	_ = owner.withSpace(tid, func(st *spaceState) error {
		if int(st.space.CurrentInstrumentEpoch()) != epochBefore {
			t.Fatal("re-enrolling rotated the instrument epoch")
		}
		return nil
	})
	if len(owner.ks.Certs) != certsBefore || len(owner.ks.Instruments) != recsBefore {
		t.Fatal("re-enrolling grew the keystore")
	}
	// CONFLICT: the same device claiming a different terminal.
	forged := newExternalDevice(t, owner.PrincipalID, tid, "Forged")
	fe, _ := enrollment.Decode(forged.enroll)
	fe.Device, fe.X25519Pub = ext.dev.ID, ext.dev.X25519Pub
	if err := fe.Sign(ext.dev.SignKey(), ed25519.NewKeyFromSeed(forged.termSeed)); err != nil {
		t.Fatal(err)
	}
	fb, err := fe.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := owner.AttachInstrumentByEnrollment(tid, fb, 3000); !errors.Is(err, ErrEnrollmentConflict) {
		t.Fatalf("a rebinding of a known device was not a conflict: %v", err)
	}

	// RESTART: the external record survives, and detach still turns the key
	// for a device this node never held keys for.
	owner.Close()
	owner2 := openRuntime(t, ownerDir, "owner")
	defer owner2.Close()
	owner2.DevIngest = true
	found := false
	for _, rec := range owner2.ks.Instruments {
		if rec.External && rec.TerminalPub == iid {
			found = true
		}
	}
	if !found {
		t.Fatal("the external instrument did not survive the restart")
	}
	if err := owner2.DetachInstrument(tid, iid); err != nil {
		t.Fatal(err)
	}
	// After the turn the device is unauthorized: a fresh, valid reading
	// under its (now old) key is refused, and counted as such.
	late := ext.reading(t, 300, now+10)
	if _, err := owner2.IngestInstrumentFrames(tid, iid, [][]byte{late}); err == nil {
		t.Fatal("a detached instrument still has a door")
	}
	_ = owner2.withSpace(tid, func(st *spaceState) error {
		if _, err := st.space.Absorb(late); err != nil {
			t.Fatalf("the log should still admit a well-formed frame: %v", err)
		}
		if st.space.UnauthorizedInstrument != 1 {
			t.Fatalf("the detached device's reading was not refused as unauthorized: %d", st.space.UnauthorizedInstrument)
		}
		return nil
	})
}

// PUBLIC TELEMETRY: a broadcast space is plaintext, so it has no epoch
// membership and no instrument plane — attach must not try to turn a key
// that does not exist. What the sensor DOES get is an attested writer
// binding (the same law any curated writer lives under), signed into the
// manifest before its first frame; detach takes that binding back — the
// plaintext analogue of "the key turns".
func TestASensorSpeaksPlaintextInABroadcastSpace(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "owner")
	defer rt.Close()

	tid, err := rt.CreateSpaceWithOptions("greenhouse", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	iid, err := rt.AttachSimulatedInstrument(tid, "Greenhouse", 1, 0)
	if err != nil {
		t.Fatalf("attach refused in a broadcast space: %v", err)
	}
	rt.mu.Lock()
	ir := rt.instruments[iid]
	rt.mu.Unlock()
	if ir == nil {
		t.Fatal("the attached instrument is not in the runtime")
	}
	// The live ticker is left alone: it first fires at 5s, far beyond this
	// test, and DetachInstrument below owns closing ir.stop.

	// The sensor's device is an attested writer now; the owner's binding
	// from creation is untouched.
	pol := policyOf(t, rt, tid)
	if !pol.AllowsWriter(rt.PrincipalID, ir.part.Device.ID) {
		t.Fatal("the sensor's device is not an attested writer")
	}
	if !pol.AllowsWriter(rt.PrincipalID, rt.Device.ID) {
		t.Fatal("binding the sensor cost the owner their own binding")
	}

	// A reading emits — and rides PLAINTEXT, like everything else in a
	// public space: no plane exists to seal it to.
	if err := rt.emitSimTick(ir, 1, uint64(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}
	sawReading := false
	_ = rt.withSpace(tid, func(st *spaceState) error {
		if st.space.InstrumentCount() != 0 {
			t.Fatalf("a plaintext space grew an instrument plane: %d devices",
				st.space.InstrumentCount())
		}
		return st.space.Log.Replay(func(a eventlog.Applied) error {
			if a.Env.Schema == schemas.ObservationValue {
				sawReading = true
				if a.Env.PayloadEncoding == signal.PayloadEncrypted ||
					a.Env.PayloadEncoding == signal.PayloadInstrumentSealed {
					t.Fatalf("a public reading rode sealed encoding %d", a.Env.PayloadEncoding)
				}
			}
			return nil
		})
	})
	if !sawReading {
		t.Fatal("no reading reached the log")
	}

	// Detach takes the binding back.
	if err := rt.DetachInstrument(tid, iid); err != nil {
		t.Fatal(err)
	}
	pol = policyOf(t, rt, tid)
	if pol.AllowsWriter(rt.PrincipalID, ir.part.Device.ID) {
		t.Fatal("a detached sensor still holds a writer binding")
	}
	if !pol.AllowsWriter(rt.PrincipalID, rt.Device.ID) {
		t.Fatal("detaching the sensor removed the owner's binding")
	}
}
