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
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
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
	for _, rec := range r.ks.Instruments {
		if rec.Space == space && terminalOfRecord(rec) == instrumentID {
			found = true
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
	dev := ir.part.Device.ID
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
