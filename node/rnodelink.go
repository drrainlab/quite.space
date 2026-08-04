// RNode as the node's radio: the second carrier, attached the first-class way.
//
// This file is deliberately a mirror of the wireTransfer branch in mesh.go,
// because that is the claim being kept: a carrier is a driver behind
// RadioDatagram, and everything above it — the transfer layer, the peer link,
// the invitation saga, the pump's cadence — must not know which radio is
// underneath. What differs is honest and small: RNode needs no supervision
// wrapper (it is a modem on a serial port, not a session that renegotiates),
// no channel table, and no want_ack decision, because it never had either.
package node

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/transports"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
	"github.com/drrainlab/quiet_places/transports/rnode"
)

// StartRNodeTransfer attaches an RNode modem with the Quiet Radio Transfer
// layer. device is a serial path; seed is the SEGMENT SEED every radio in the
// segment shares — the frame-authentication key derives from it, and a peer
// holding a different one is not a peer. Carrying the seed inside the
// ordinary invitation is the RadioSegment descriptor's job (recorded); until
// then it is passed in, exactly as the Meshtastic path does.
func (r *Runtime) StartRNodeTransfer(device string, seed []byte) error {
	return r.startRNodeOn(device, seed, rnode.ProfileLongFastRU)
}

// startRNodeOn is the same thing with the PHY named rather than assumed, so a
// segment that arrived in an invitation can say which air it lives on and a
// build that does not know that air can refuse instead of guessing.
func (r *Runtime) startRNodeOn(device string, seed []byte, profile string) error {
	phy, ok := rnode.SettingsForProfile(profile)
	if !ok {
		return fmt.Errorf("this build does not know the radio profile %q, so "+
			"it will not bring a radio up on air it cannot name. Whoever set "+
			"the segment up is running a version this one does not match",
			profile)
	}
	r.mu.Lock()
	if r.meshSupervised {
		r.mu.Unlock()
		return errors.New("node: a radio is already connected")
	}
	r.mu.Unlock()

	key, err := radiotransfer.DeriveTransferKey(seed, radiotransfer.KDFVersion)
	if err != nil {
		return fmt.Errorf("radio transfer needs a segment seed every radio "+
			"shares: %w", err)
	}

	// The PHY is the regulatory RU profile the whole project transmits on.
	// The first dial is synchronous and its error is the caller's: a wrong
	// device path, or a modem whose radio will not start, fails HERE with
	// its own sentence rather than becoming a node quietly retrying a radio
	// that does not exist.
	radio, err := rnode.Open(device, phy)
	if err != nil {
		return err
	}

	// Per-carrier limits, which is what Limits being per-endpoint exists for.
	// The defaults are documented as "sized for the carrier this was measured
	// on" — Meshtastic, ~2 s a packet, a 16-deep firmware queue — and on this
	// carrier they are not merely slow but WRONG: an AckTimeout of 45 s is
	// wider than the 20-second life of a probe, so a probe whose single frame
	// was lost never got a second attempt inside its own deadline. Found on
	// the live gate: five probes in a row, each one frame, none retried.
	//
	// Here the RD-6b machinery does the honest pacing — the queue bound and
	// the drain-based patience are derived from the modem's own airtime
	// model — so the static numbers can be small and the retries frequent.
	lim := radiotransfer.Limits{
		FrameGap:   300 * time.Millisecond,
		AckTimeout: 10 * time.Second,
		SACKDelay:  500 * time.Millisecond,
		SendFloor:  200 * time.Millisecond,
	}
	opts := radiotransfer.EndpointOptions{
		Options:       radiotransfer.Options{Limits: lim},
		OnControl:     r.onRadioControl,
		OnControlSent: r.onRadioControlSent,
	}
	// Forensics on demand: the same structured trace that found the queue,
	// the stale SACKs and the COMMIT storm, attachable to a live node.
	if path := os.Getenv("QUIET_RADIO_TRACE"); path != "" {
		if f, ferr := os.OpenFile(path,
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); ferr == nil {
			var mu sync.Mutex
			opts.Options.Trace = func(ev radiotransfer.TraceEvent) {
				mu.Lock()
				fmt.Fprintln(f, ev.String())
				mu.Unlock()
			}
		}
	}
	ep, err := radiotransfer.Wrap(radio, key, opts)
	if err != nil {
		radio.Close()
		return err
	}

	r.mu.Lock()
	r.meshSupervised = true // ONE radio per node, whatever the carrier
	r.meshSeed = append([]byte(nil), seed...)
	r.rnodeRadio = radio
	r.rnodeEP = ep
	r.mu.Unlock()

	// Remembered HERE, where a radio actually came up, rather than in the
	// attach path only. The live gate caught that: a node started with
	// --rnode carried no segment in its invitations at all, because the
	// keystore record was written by the interface path and by nothing else.
	// One radio coming up is one place, whoever asked for it.
	r.rememberRadio(device, seed)

	lk := rnodeLink{Endpoint: ep, radio: radio, transfer: ep}
	r.mu.Lock()
	r.rnodeLink = lk
	r.mu.Unlock()
	r.adoptLink(lk, meshPumpEvery, meshSummaryEvery, "radio")
	return nil
}

// rnodeLink pairs the Radio Transfer endpoint with the modem's liveness — the
// same shape as transferLink, for the same reasons, including this one:
//
// It embeds transports.Endpoint — an INTERFACE — so only that interface's own
// methods are promoted. Everything else is forwarded EXPLICITLY, because
// three separate times a method was added to radioControlEndpoint and not to
// the link type, and three times the failure surfaced as "no radio is
// attached" in somebody's hand rather than as a build error. The compile-time
// assertions at the bottom are what make a fourth time impossible.
type rnodeLink struct {
	transports.Endpoint
	radio    *rnode.Radio
	transfer *radiotransfer.Endpoint
}

func (l rnodeLink) Closed() (bool, error) { return l.radio.Closed() }

func (l rnodeLink) SendControl(msg []byte) error { return l.transfer.SendControl(msg) }
func (l rnodeLink) Close() error                 { l.transfer.Close(); return l.radio.Close() }

func (l rnodeLink) SendControlWithin(msg []byte, within time.Duration) error {
	return l.transfer.SendControlWithin(msg, within)
}
func (l rnodeLink) SendControlTagged(tag string, msg []byte, within time.Duration) error {
	return l.transfer.SendControlTagged(tag, msg, within)
}

func (l rnodeLink) Credit() transports.Credit { return l.transfer.Credit() }

func (l rnodeLink) TransferStats() radiotransfer.Stats { return l.transfer.Stats() }

var (
	_ radioControlEndpoint = rnodeLink{}
	_ transports.Pacer     = rnodeLink{}
)

// rememberRadio stores what this device is attached to, and saves only when
// something actually changed. Restoring on start re-derives the same record,
// and rewriting the keystore on every open would be a write nobody asked for.
func (r *Runtime) rememberRadio(device string, seed []byte) {
	r.mu.Lock()
	cur := r.ks.Radio
	if cur.Carrier == carrierRNode && cur.Device == device &&
		bytes.Equal(cur.Seed, seed) {
		r.mu.Unlock()
		return
	}
	r.ks.Radio = storage.RadioRecord{
		Carrier: carrierRNode, Device: device,
		Seed: append([]byte(nil), seed...),
	}
	r.mu.Unlock()
	if err := r.saveKeystore(); err != nil {
		log.Printf("radio: attached but not remembered: %v", err)
	}
}
