package bridge

import (
	"crypto/rand"
	"encoding/binary"
	"time"

	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
)

const (
	// beaconEvery is how often a gateway announces itself. Presence is worth
	// far less airtime than carrying messages: at LoRa rates a beacon every
	// few minutes is already the most a segment should spend on saying
	// hello, and a person waiting to see a gateway appear can wait that long.
	beaconEvery = 5 * time.Minute
	// beaconValidFor is how long one announcement stands. Longer than the
	// interval, so a single lost packet does not make a live gateway look
	// gone — the failure this exists to distinguish from real absence.
	beaconValidFor = 16 * time.Minute
)

// newBootID is random per process start. It orders announcements within one
// run without any shared clock, and a restart is visible as a different id
// rather than as a sequence that mysteriously went backwards.
func newBootID() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Entropy failure at startup is not something to paper over: an
		// unpredictable boot id is what makes replays across restarts
		// detectable.
		panic("bridge: no entropy for the gateway boot id: " + err.Error())
	}
	return binary.BigEndian.Uint64(b[:])
}

// PushBeacon announces this gateway on the carrier if one is due. Returns
// true if it transmitted.
//
// Three situations look identical to a person off the internet: no gateway
// on this mesh, a gateway that is present and busy, and a radio on the wrong
// channel. In all three nothing happens. This is what separates them.
//
// The announcement is advisory in both directions: a node that hears it does
// not gain a right to anything, and a node that hears nothing is not stopped
// from sending. It rides the control lane by construction — sent before
// PushRadio drains the data queue — so a backed-up gateway never goes silent
// about its own existence.
func (b *Bridge) PushBeacon(now time.Time) bool {
	if b.cfg.Radio == nil || b.cfg.NetworkID == "" {
		// Without a network id a beacon cannot say which segment it belongs
		// to, and a listener could not tell it from another operator's
		// gateway on the same frequency. Silence is the honest answer.
		return false
	}
	b.mu.Lock()
	every := b.cfg.BeaconEvery
	if every <= 0 {
		every = beaconEvery
	}
	if !b.lastBeacon.IsZero() && now.Sub(b.lastBeacon) < every {
		b.mu.Unlock()
		return false
	}
	b.beaconSeq++
	msg := Beacon{
		Version:    1,
		NetworkID:  b.cfg.NetworkID,
		Label:      b.cfg.Instance,
		BootID:     b.bootID,
		Sequence:   b.beaconSeq,
		IssuedSlot: uint64(now.Unix()),
		ValidFor:   uint64((every*3 + time.Minute).Seconds()),
		UplinkUp:   b.relayHealthy,
	}
	b.mu.Unlock()

	if len(msg.Label) > MaxLabelLen {
		msg.Label = msg.Label[:MaxLabelLen]
	}
	msg.QueueDepth = uint64(b.QueueLen())

	raw, err := SignBeacon(b.custodian, msg)
	if err != nil {
		return false
	}
	wire := kernelsync.EncodeBeaconMessage(raw)
	if !b.airtime.Take(len(wire), now) {
		return false
	}
	b.mu.Lock()
	b.nextStream++
	stream := b.nextStream
	b.mu.Unlock()

	frags, err := kernelsync.FragmentStream(stream, wire,
		b.cfg.Radio.Capabilities().MaxPayload)
	if err != nil {
		return false
	}
	for _, f := range frags {
		if err := b.cfg.Radio.Send(f); err != nil {
			// The carrier is down. Do not record this as an announcement:
			// the next pass should try again rather than wait out a cooldown
			// for something nobody heard.
			return false
		}
	}
	b.mu.Lock()
	b.lastBeacon = now
	b.stats.Beacons++
	b.mu.Unlock()
	return true
}

// noteRelayResult records whether the internet side is currently working, so
// the beacon can say so. A gateway with a dead uplink still carries within
// the segment — this changes what a person should expect from it, not
// whether it is useful.
func (b *Bridge) noteRelayResult(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.relayHealthy = err == nil
}

// NoteRelayResult records the outcome of the last internet-side exchange.
// Both directions matter: a gateway that can push but not pull is not a
// working uplink, and reporting it as one would have a person waiting for
// replies that cannot arrive.
func (b *Bridge) NoteRelayResult(push, pull error) {
	if push != nil {
		b.noteRelayResult(push)
		return
	}
	b.noteRelayResult(pull)
}
