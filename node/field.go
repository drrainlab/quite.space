// Field emits (SP-3, ADR-031): positions, markers, check-ins. Every path
// sits behind canWrite; the honesty laws live where they belong — the
// SOS fallback default HERE (the wire never parses prose), the freshness
// ladder at the API projection, never in the kernel.
package node

import (
	"crypto/rand"
	"errors"
	"time"

	"github.com/drrainlab/quiet_places/protocol/geo"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/terminals/human"
)

// SetPosition emits a position claim with a signed TTL.
func (r *Runtime) SetPosition(tid id.TerminalID, pt geo.Point, accuracyM, ttl uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errors.New("node: unknown space")
	}
	if err := r.canWrite(st); err != nil {
		return err
	}
	now := uint64(time.Now().Unix())
	if ttl == 0 {
		ttl = 600 // the field default: ten minutes of current-tense
	}
	return human.SetPosition(r.Self, st.space, &schemas.PositionObservation{
		Point: pt, AccuracyM: accuracyM, ExpiresAt: now + ttl,
	}, now)
}

// PlaceMarker emits a marker claim. An empty label defaults to the kind —
// the fallback must always read as something on an old node.
func (r *Runtime) PlaceMarker(tid id.TerminalID, kind, text string, pt geo.Point, objectID *[16]byte, expiresAt uint64) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	if err := r.canWrite(st); err != nil {
		return id.EventID{}, err
	}
	if text == "" {
		text = kind
	}
	m := &schemas.PlacedMarker{Text: text, Kind: kind, Point: pt,
		ObjectID: objectID, ExpiresAt: expiresAt}
	if _, err := rand.Read(m.MarkerID[:]); err != nil {
		return id.EventID{}, err
	}
	payload, err := m.Encode()
	if err != nil {
		return id.EventID{}, err
	}
	return r.emitLocked(st, schemas.MarkerPlaced, payload)
}

// SendCheckin emits a contact fact. THE SOS FALLBACK LAW lives here
// (ADR-031 §4): an empty note defaults to an emergency-semantic string
// when sos is set — the wire's sos flag is the truth, the text is only
// the human fallback, and an old node must see it scream, not soothe.
func (r *Runtime) SendCheckin(tid id.TerminalID, note string, pt *geo.Point, batteryPct uint64, hasBattery, sos bool) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	if err := r.canWrite(st); err != nil {
		return id.EventID{}, err
	}
	if note == "" {
		if sos {
			note = "🆘 SOS"
		} else {
			note = "✓ check-in"
		}
	}
	c := &schemas.Checkin{Text: note, Point: pt,
		BatteryPct: batteryPct, HasBattery: hasBattery, SOS: sos}
	payload, err := c.Encode()
	if err != nil {
		return id.EventID{}, err
	}
	return r.emitLocked(st, schemas.CheckinSent, payload)
}
