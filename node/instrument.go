package node

// Instrument participants (QI-1): the runtime side of the Instrument
// Plane. An instrument is minted exactly the way the agent and the
// gateway are — its own device, its own terminal identity, the
// operator's principal as controller, certified as an owned device —
// and then, unlike them, it is added to the space's INSTRUMENT wrap
// list, never the conversation one. From that moment its readings seal
// to a key the humans hold and the relay does not, and the human
// conversation stays sealed to a key the instrument has never seen.
//
// v1 ships one driver: the deterministic reference greenhouse. It is
// not a mock — it is the behavioral contract every later driver
// (quiet-terminal on a Pi, an ESP32 with a BME280) must meet: same
// manifest, same identity shape, same observation frames, same panel.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/enrollment"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/instrument"
)

// instrumentRuntime is one attached instrument, alive in this process.
type instrumentRuntime struct {
	part *terminals.Participant
	rec  storage.InstrumentRecord
	stop chan struct{}
}

// greenhouseChannels is the reference driver's panel — the declaration
// an ESP32 greenhouse must one day match verbatim.
func greenhouseChannels() []terminals.ChannelDecl {
	return []terminals.ChannelDecl{
		{Channel: "temperature", Kind: "number", Unit: "°C", Label: "Температура"},
		{Channel: "humidity", Kind: "number", Unit: "%", Label: "Влажность"},
		{Channel: "door", Kind: "boolean", Label: "Дверь"},
		{Channel: "light", Kind: "number", Unit: "%", Label: "Свет"},
	}
}

// simTickEvery is the reference driver's cadence. Slow on purpose: a
// greenhouse is not a stock ticker, and every reading is a frame.
var simTickEvery = 5 * time.Second

// AttachSimulatedInstrument mints the reference greenhouse into a space
// and starts its driver. seed makes the whole value stream reproducible;
// 0 draws a random one (live mode should shimmer, tests pass their own).
func (r *Runtime) AttachSimulatedInstrument(space id.TerminalID, label string, seed uint64) (id.TerminalID, error) {
	if label == "" {
		label = "Greenhouse"
	}
	if seed == 0 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return id.TerminalID{}, err
		}
		seed = binary.BigEndian.Uint64(b[:])
	}
	channels := greenhouseChannels()
	tmpl, err := instrument.Template("sensor", label, channels)
	if err != nil {
		return id.TerminalID{}, err
	}
	dev, err := identity.NewDevice(identity.NewRand())
	if err != nil {
		return id.TerminalID{}, err
	}
	part, seedTerm, err := terminals.NewParticipantFrom(r.PrincipalID, dev, nil, tmpl)
	if err != nil {
		return id.TerminalID{}, err
	}
	chanLabels, err := terminals.InstrumentLabels(channels)
	if err != nil {
		return id.TerminalID{}, err
	}
	rec := storage.InstrumentRecord{
		Space: space, Label: label, Kind: "sensor", Channels: chanLabels,
		DeviceSeed: dev.Seed(), DeviceX25519: dev.X25519Priv(),
		TerminalSeed: seedTerm, ManifestFrame: part.ManifestFrame,
		Simulated: true, SimSeed: seed,
	}

	err = r.withSpace(space, func(st *spaceState) error {
		// Identity first: the certificate is what admits every frame the
		// instrument will ever write, and what carries the X25519 the
		// epoch wraps address.
		r.certifyOwnedDeviceLocked(dev)
		st.space.AddInstrument(dev.ID, dev.X25519Pub)
		if _, err := r.Self.RotateInstrumentEpoch(st.space); err != nil {
			return err
		}
		if _, _, err := part.PublishManifest(st.space); err != nil {
			return err
		}
		r.ks.Instruments = append(r.ks.Instruments, rec)
		r.persistEpochsLocked(space, st.space)
		return r.saveKeystore()
	})
	if err != nil {
		return id.TerminalID{}, err
	}

	r.mu.Lock()
	if r.instruments == nil {
		r.instruments = map[id.TerminalID]*instrumentRuntime{}
	}
	ir := &instrumentRuntime{part: part, rec: rec, stop: make(chan struct{})}
	r.instruments[part.TerminalID] = ir
	r.mu.Unlock()
	r.startSimulator(ir)
	r.kickRelaySync()
	return part.TerminalID, nil
}

// DetachInstrument removes an instrument: its driver stops, it leaves
// the wrap list, and THE KEY TURNS — detachment without rotation would
// let the detached device keep reading every future observation.
func (r *Runtime) DetachInstrument(space, instrumentID id.TerminalID) error {
	r.mu.Lock()
	ir := r.instruments[instrumentID]
	if ir != nil {
		delete(r.instruments, instrumentID)
	}
	kept := r.ks.Instruments[:0]
	found := false
	var detachedDev id.DeviceID
	for _, rec := range r.ks.Instruments {
		if rec.Space == space && terminalOfRecord(rec) == instrumentID {
			found = true
			detachedDev = rec.DevicePub
			continue
		}
		kept = append(kept, rec)
	}
	r.ks.Instruments = kept
	r.mu.Unlock()
	if ir != nil {
		close(ir.stop)
	}
	if !found && ir == nil {
		return errors.New("node: unknown instrument")
	}
	var dev id.DeviceID
	if ir != nil {
		dev = ir.part.Device.ID
	} else {
		// External (QI-M): no runtime, the record carries the public
		// device id the wrap list knows it by.
		dev = detachedDev
	}
	return r.withSpace(space, func(st *spaceState) error {
		st.space.RemoveInstrument(dev)
		if _, err := r.Self.RotateInstrumentEpoch(st.space); err != nil {
			return err
		}
		r.persistEpochsLocked(space, st.space)
		return r.saveKeystore()
	})
}

// terminalOfRecord recovers the InstrumentID from a stored record.
func terminalOfRecord(rec storage.InstrumentRecord) id.TerminalID {
	if rec.External {
		return rec.TerminalPub
	}
	p, err := terminals.ParticipantIDFromSeed(rec.TerminalSeed)
	if err != nil {
		return id.TerminalID{}
	}
	return p
}

// restoreInstruments rebuilds attached instruments after Open, and
// resumes any that were simulating — a restart must not silently turn a
// demo greenhouse into a dead card.
func (r *Runtime) restoreInstruments() {
	r.mu.Lock()
	recs := append([]storage.InstrumentRecord(nil), r.ks.Instruments...)
	r.mu.Unlock()
	for _, rec := range recs {
		if rec.External {
			continue // its keys live on the device; nothing runs here
		}
		dev, err := identity.NewDeviceFromKeys(rec.DeviceSeed, rec.DeviceX25519)
		if err != nil {
			continue
		}
		part, err := terminals.NewParticipantFromManifest(r.PrincipalID, dev,
			rec.TerminalSeed, rec.ManifestFrame)
		if err != nil {
			continue
		}
		// A fourth writer, the same obligation as the agent and the gateway:
		// forgetting to resume its chain forks the log on the first tick
		// after a restart and quarantines the instrument forever.
		_ = r.withSpace(rec.Space, func(st *spaceState) error {
			part.ResumeChain(st.space)
			return nil
		})
		ir := &instrumentRuntime{part: part, rec: rec, stop: make(chan struct{})}
		r.mu.Lock()
		if r.instruments == nil {
			r.instruments = map[id.TerminalID]*instrumentRuntime{}
		}
		r.instruments[part.TerminalID] = ir
		r.mu.Unlock()
		if rec.Simulated {
			r.startSimulator(ir)
		}
	}
}

// ---- the deterministic reference driver ----

// simValues computes tick n of the greenhouse from the seed alone: the
// same seed and the same tick produce the same reading on any machine,
// which is what lets a test move the clock by hand and assert exact
// numbers (owner's amendment 8).
func simValues(seed, n uint64) (tempDeci int64, humidDeci int64, door bool, lightPct uint64) {
	phase := float64(seed%1000) / 1000.0
	t := 22.0 + 2.0*math.Sin(2*math.Pi*(float64(n)/24.0+phase))
	h := 50.0 + 10.0*math.Sin(2*math.Pi*(float64(n)/17.0+phase*3))
	tempDeci = int64(math.Round(t * 10))
	humidDeci = int64(math.Round(h * 10))
	door = (seed+n)%11 == 0
	lightPct = (seed/7 + n*13) % 101
	return
}

// emitSimTick publishes one tick of readings. Exposed on the Runtime so
// a test can drive time by hand; the live goroutine is just a ticker
// around it.
func (r *Runtime) emitSimTick(ir *instrumentRuntime, n uint64, at uint64) error {
	tempDeci, humidDeci, door, light := simValues(ir.rec.SimSeed, n)
	obs := []schemas.ValueObservation{
		{Channel: "temperature", HasNumber: true,
			Magnitude: uint64(abs64(tempDeci)), Negative: tempDeci < 0, Decimals: 1,
			ObservedAt: at, StaleAfter: 60, Simulated: true},
		{Channel: "humidity", HasNumber: true,
			Magnitude: uint64(abs64(humidDeci)), Decimals: 1,
			ObservedAt: at, StaleAfter: 60, Simulated: true},
		{Channel: "door", HasBool: true, BoolValue: door,
			ObservedAt: at, StaleAfter: 120, Simulated: true},
		{Channel: "light", HasNumber: true, Magnitude: light,
			ObservedAt: at, StaleAfter: 60, Simulated: true},
	}
	return r.withSpace(ir.rec.Space, func(st *spaceState) error {
		for i := range obs {
			payload, err := obs[i].Encode()
			if err != nil {
				return err
			}
			if _, err := ir.part.Emit(st.space, schemas.ObservationValue, payload,
				signal.AuthorshipSensor, at); err != nil {
				return fmt.Errorf("node: greenhouse tick: %w", err)
			}
		}
		return nil
	})
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func (r *Runtime) startSimulator(ir *instrumentRuntime) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := time.NewTicker(simTickEvery)
		defer t.Stop()
		var n uint64
		for {
			select {
			case <-r.stop:
				return
			case <-ir.stop:
				return
			case <-t.C:
				n++
				_ = r.emitSimTick(ir, n, uint64(time.Now().Unix()))
			}
		}
	}()
}

// Instruments lists this node's attached instruments (for the API).
func (r *Runtime) Instruments(space id.TerminalID) []storage.InstrumentRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []storage.InstrumentRecord
	for _, rec := range r.ks.Instruments {
		if rec.Space == space {
			out = append(out, rec)
		}
	}
	return out
}

// ---- external instruments (QI-M, ADR-026) ----

// ErrEnrollmentConflict: the same instrument identity arrived with a
// DIFFERENT binding (another terminal, another manifest) — refused, never
// silently re-bound. The same binding twice is not a conflict: see
// AttachInstrumentByEnrollment.
var ErrEnrollmentConflict = errors.New("node: enrollment conflicts with an attached instrument's binding")

// ErrDevIngestClosed: the dev-only frame ingest is off (the default).
var ErrDevIngestClosed = errors.New("node: dev instrument ingest is not enabled (--dev-ingest)")

// AttachInstrumentByEnrollment admits a device whose keys were minted ON
// THE DEVICE: it verifies the enrollment (both signatures and the
// manifest), certifies the public halves with the root this node holds,
// adds the device to the INSTRUMENT wrap list, turns the instrument key,
// republishes the device's manifest on its behalf, and returns the
// provision the device needs to speak — its certificate and the current
// instrument epoch frame. Nothing private crosses in either direction.
//
// IDEMPOTENT (owner's amendment 5): the same (device, terminal, manifest)
// enrolled again — a serial link that dropped, a QR scanned twice —
// returns a fresh provision with no second certificate, no second
// rotation and no second record. A different binding for a known
// identity is ErrEnrollmentConflict.
func (r *Runtime) AttachInstrumentByEnrollment(space id.TerminalID, enrollBytes []byte, nowUnix uint64) ([]byte, id.TerminalID, error) {
	e, err := enrollment.Decode(enrollBytes)
	if err != nil {
		return nil, id.TerminalID{}, err
	}
	m, err := manifest.Decode(e.ManifestFrame)
	if err != nil {
		return nil, id.TerminalID{}, err
	}
	if !terminals.IsInstrumentKind(m.Kind) {
		return nil, id.TerminalID{}, errors.New("node: enrollment manifest is not an instrument")
	}
	var provision []byte
	err = r.withSpace(space, func(st *spaceState) error {
		if r.Principal == nil {
			return ErrNotAuthority
		}
		// Known identity? Same binding → idempotent; different → conflict.
		for _, rec := range r.ks.Instruments {
			if rec.Space != space || !rec.External {
				continue
			}
			sameDev, sameTerm := rec.DevicePub == e.Device, rec.TerminalPub == e.Terminal
			if !sameDev && !sameTerm {
				continue
			}
			if sameDev && sameTerm && rec.X25519Pub == e.X25519Pub &&
				sha256.Sum256(rec.ManifestFrame) == sha256.Sum256(e.ManifestFrame) {
				p, err := r.provisionLocked(st, space, rec)
				provision = p
				return err
			}
			return ErrEnrollmentConflict
		}
		// Certify the public halves; store the certificate where every
		// owned device's lives so publishCertLocked carries it.
		cert := r.Principal.CertifyPublic(e.Device, e.X25519Pub, nowUnix, 0)
		certFrame, err := cert.Encode()
		if err != nil {
			return err
		}
		r.ks.Certs = append(r.ks.Certs, storage.CertRecord{Device: e.Device, Frame: certFrame, Label: e.Label})
		_ = r.ident.store.AddCertificate(cert)
		for _, other := range r.spaces {
			r.publishCertLocked(other.space)
		}
		st.space.AddInstrument(e.Device, e.X25519Pub)
		if _, err := r.Self.RotateInstrumentEpoch(st.space); err != nil {
			return err
		}
		if _, _, err := r.Self.PublishManifestFrameOnBehalf(st.space, e.ManifestFrame); err != nil {
			return err
		}
		rec := storage.InstrumentRecord{
			Space: space, Label: e.Label, Kind: m.Kind.String(),
			Channels:      terminals.InstrumentLabelsOf(m.DeclaredLabels),
			ManifestFrame: e.ManifestFrame,
			External:      true, DevicePub: e.Device, X25519Pub: e.X25519Pub, TerminalPub: e.Terminal,
		}
		r.ks.Instruments = append(r.ks.Instruments, rec)
		r.persistEpochsLocked(space, st.space)
		if err := r.saveKeystore(); err != nil {
			return err
		}
		p, err := r.provisionLocked(st, space, rec)
		provision = p
		return err
	})
	if err != nil {
		return nil, id.TerminalID{}, err
	}
	r.kickRelaySync()
	return provision, e.Terminal, nil
}

// provisionLocked assembles the device's freight from what the log and
// the keystore already hold: its certificate and the CURRENT instrument
// epoch frame (older epochs never addressed it). Callers hold r.mu.
func (r *Runtime) provisionLocked(st *spaceState, space id.TerminalID, rec storage.InstrumentRecord) ([]byte, error) {
	var certFrame []byte
	for _, c := range r.ks.Certs {
		if c.Device == rec.DevicePub {
			certFrame = c.Frame
		}
	}
	var epochFrame []byte
	_ = st.space.Log.Replay(func(a eventlog.Applied) error {
		if a.Env.Schema == schemas.InstrumentEpoch {
			epochFrame = a.Frame // last one wins: the current epoch
		}
		return nil
	})
	if certFrame == nil || epochFrame == nil {
		return nil, errors.New("node: provision incomplete — certificate or epoch missing")
	}
	p := &enrollment.Provision{Space: space, Principal: r.PrincipalID,
		CertFrame: certFrame, EpochFrames: [][]byte{epochFrame},
		ManifestAck: sha256.Sum256(rec.ManifestFrame)}
	return p.Encode()
}

// IngestInstrumentFrames is the DEV STAND's door (QI-M3): frames an
// external instrument produced, handed in over whatever the stand uses
// (USB serial today). It is not a bearer and must not grow into one —
// it is bound to ONE instrument's identity (owner's amendment 12): every
// frame must be signed by that instrument's certified device and name
// that instrument as its source, and the door is shut unless the runtime
// was opened with DevIngest. Returns how many frames applied.
func (r *Runtime) IngestInstrumentFrames(space, instrumentID id.TerminalID, frames [][]byte) (int, error) {
	if !r.DevIngest {
		return 0, ErrDevIngestClosed
	}
	applied := 0
	err := r.withSpace(space, func(st *spaceState) error {
		var rec *storage.InstrumentRecord
		for i := range r.ks.Instruments {
			c := &r.ks.Instruments[i]
			if c.Space == space && c.External && c.TerminalPub == instrumentID {
				rec = c
			}
		}
		if rec == nil {
			return errors.New("node: no external instrument with that id here")
		}
		for _, f := range frames {
			env, err := signal.Decode(f)
			if err != nil {
				return err
			}
			if env.Device != rec.DevicePub || env.SourceTerminal == nil || *env.SourceTerminal != instrumentID {
				return errors.New("node: frame is not from this instrument")
			}
			n, err := st.space.Absorb(f)
			if err != nil {
				return err
			}
			applied += n
		}
		return nil
	})
	if err == nil && applied > 0 {
		r.kickRelaySync()
	}
	return applied, err
}

// ExternalInstrumentEpochFrames returns the current instrument epoch
// frame(s) of a space — what the dev stand pushes back to a device after
// a rotation so it keeps speaking.
func (r *Runtime) ExternalInstrumentEpochFrames(space id.TerminalID) ([][]byte, error) {
	var out [][]byte
	err := r.withSpace(space, func(st *spaceState) error {
		var last []byte
		_ = st.space.Log.Replay(func(a eventlog.Applied) error {
			if a.Env.Schema == schemas.InstrumentEpoch {
				last = a.Frame
			}
			return nil
		})
		if last != nil {
			out = [][]byte{last}
		}
		return nil
	})
	return out, err
}
