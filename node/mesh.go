// Meshtastic wiring (M1.7): the runtime can attach one radio and sync all
// spaces over the mesh at LoRa-respectful cadence. Airtime honesty: the
// summary interval is a minute, not milliseconds — a LoRa mesh is a
// low-bandwidth projection of the same Terminal, never a fast pipe
// pretending otherwise (invariant §2.6).
package node

import (
	"errors"
	"fmt"
	"time"

	"github.com/drrainlab/quiet_places/transports"
	"github.com/drrainlab/quiet_places/transports/compact"
	"github.com/drrainlab/quiet_places/transports/meshtastic"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
)

// Mesh cadence; vars so tests can accelerate them.
var (
	meshPumpEvery    = 2 * time.Second
	meshSummaryEvery = 60 * time.Second

	// Reconnect schedule. A radio disappears for ordinary reasons — a USB
	// port knocked loose, a node rebooting after a config change, a TCP node
	// whose WiFi dropped — and before RB-2 any of those made the node
	// permanently deaf with no error anywhere. The schedule itself lives in
	// transports/meshtastic, shared with the bridge daemon.
	meshBackoffMin = time.Second
	meshBackoffMax = 30 * time.Second
	meshStableFor  = 60 * time.Second
)

// StartMeshtastic attaches a radio. target forms:
//
//	tcp:HOST[:PORT]     WiFi node or meshtasticd (default port 4403)
//	serial:/dev/PATH    USB-attached node
//
// Real radios default to the RAW wire (a raw-only peer cannot parse the
// compact profile — TN-2A negotiation is operator config, auto is TN-2B);
// StartMeshtasticCompact opts a link into the compact profile.
func (r *Runtime) StartMeshtastic(target string) error {
	return r.startMesh(target, false)
}

// StartMeshtasticCompact attaches a radio with the TN-2A compact profile
// (deflate-when-smaller, sub-fragmentation, byte-exact reversibility).
// Every peer on the carrier must also run compact.
func (r *Runtime) StartMeshtasticCompact(target string) error {
	return r.startMeshWire(target, wireCompact)
}

// StartMeshtasticTable attaches a radio with the TN-2B stateful profile
// (id-table compaction on top of compact). Every peer must run it.
func (r *Runtime) StartMeshtasticTable(target string) error {
	return r.startMeshWire(target, wireTable)
}

// StartMeshtasticTransfer attaches a radio with the Quiet Radio Transfer
// layer (MR-1): whole messages, fragmented to the carrier's real MTU,
// selectively repaired when a fragment goes missing.
//
// seed is the SEGMENT SEED. Every radio in a segment must hold the same one,
// because the frame-authentication key is derived from it — a peer with a
// different seed is not a peer, and its frames will not verify. Carrying it
// inside the ordinary Quiet invite is MR-2's job; until then it is passed in.
func (r *Runtime) StartMeshtasticTransfer(target string, seed []byte) error {
	r.mu.Lock()
	r.meshSeed = append([]byte(nil), seed...)
	r.mu.Unlock()
	return r.startMeshWire(target, wireTransfer)
}

type meshWire int

const (
	wireRaw meshWire = iota
	wireCompact
	wireTable
	wireTransfer
)

// How hard the FIRST attach tries before reporting a radio absent. Only the
// "opened but said nothing" failure is retried (see SilentHandshake), so a
// mistyped device path still fails immediately.
const (
	meshOpenAttempts = 8
	meshOpenRetryGap = 700 * time.Millisecond
)

func (r *Runtime) startMesh(target string, compactOn bool) error {
	wire := wireRaw
	if compactOn {
		wire = wireCompact
	}
	return r.startMeshWire(target, wire)
}

func (r *Runtime) startMeshWire(target string, wire meshWire) error {
	r.mu.Lock()
	if r.meshSupervised {
		r.mu.Unlock()
		return errors.New("node: a radio is already connected")
	}
	r.mu.Unlock()

	// The FIRST dial is synchronous and its error is the caller's. A
	// supervisor that swallowed it would turn "you typed the wrong device
	// path" into a node quietly retrying a radio that does not exist —
	// indistinguishable, from the outside, from one that is simply out of
	// range. Everything after this point is a link that once worked, and
	// those are worth retrying.
	r.mu.Lock()
	channel := r.meshChannel
	reliable := r.meshReliable
	seed := r.meshSeed
	r.mu.Unlock()

	// A device that opened and said NOTHING is not the same as one that is
	// not there, and only the first is worth asking again. A wrong path or a
	// busy port still fails on the spot with its own message, so the
	// distinction the comment above draws survives intact — what changes is
	// that a radio which is present and merely slow to speak no longer reads
	// as absent. Measured on a native-USB board: a single attempt answers
	// about a third of the time; the link, once up, is stable.
	var radio *meshtastic.Supervised
	var err error
	for attempt := 0; attempt < meshOpenAttempts; attempt++ {
		radio, err = meshtastic.Supervise(target,
			meshtastic.Options{Channel: channel, Reliable: reliable}, meshtastic.Backoff{
				Min: meshBackoffMin, Max: meshBackoffMax, Stable: meshStableFor,
			})
		if err == nil || !meshtastic.SilentHandshake(err) {
			break
		}
		time.Sleep(meshOpenRetryGap)
	}
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.mesh = radio
	r.meshSupervised = true
	r.mu.Unlock()

	// Adopted ONCE, for the life of the runtime. The endpoint outlives the
	// device behind it, so a radio coming and going never re-attaches
	// anything — which is why link stacking is impossible here rather than
	// merely cleaned up afterwards.
	var lk link = radio
	switch wire {
	case wireCompact:
		lk = compactLink{Endpoint: compact.Wrap(radio), radio: radio}
	case wireTable:
		lk = compactLink{Endpoint: compact.WrapStateful(radio), radio: radio}
	case wireTransfer:
		// want_ack is redundant under Radio Transfer and costs real airtime:
		// the firmware's implicit acknowledgement only says a NEIGHBOUR
		// rebroadcast the packet, while a SACK says which fragments the PEER
		// actually holds. Paying for both buys nothing and spends the band
		// this layer exists to use carefully.
		key, err := radiotransfer.DeriveTransferKey(seed, radiotransfer.KDFVersion)
		if err != nil {
			radio.Close()
			return fmt.Errorf("radio transfer needs a segment seed every radio "+
				"shares: %w", err)
		}
		ep, err := radiotransfer.Wrap(meshtastic.NewDatagram(radio), key,
			radiotransfer.EndpointOptions{OnControl: r.onRadioControl})
		if err != nil {
			radio.Close()
			return err
		}
		lk = transferLink{Endpoint: ep, radio: radio, transfer: ep}
	}
	r.adoptLink(lk, meshPumpEvery, meshSummaryEvery, "radio")
	return nil
}

// transferLink pairs the Radio Transfer endpoint with the link's liveness,
// for the same reason compactLink does: liveness is a property of the
// SUPERVISED link, not of whichever device is behind it at this instant.
type transferLink struct {
	transports.Endpoint
	radio    *meshtastic.Supervised
	transfer *radiotransfer.Endpoint
}

func (l transferLink) Closed() (bool, error) { return l.radio.Closed() }
func (l transferLink) Close() error          { l.transfer.Close(); return l.radio.Close() }

// TransferStats reports whole-message delivery, which is the number this
// layer exists to move — packet counts belong to the carrier below it.
func (l transferLink) TransferStats() radiotransfer.Stats { return l.transfer.Stats() }

// compactLink pairs the compact-wrapped endpoint with the link's liveness.
// Liveness comes from the SUPERVISED link, not from whichever device is
// behind it at this instant: a radio being replaced is not a dead link, and
// treating it as one would tear down the pump the reconnect exists to keep.
type compactLink struct {
	transports.Endpoint
	radio *meshtastic.Supervised
}

func (c compactLink) Closed() (bool, error) { return c.radio.Closed() }

// MeshStatus is the transport diagnostic for the UI.
type MeshStatus struct {
	Connected bool
	NodeNum   uint32
	TX, RX    int
	Err       string

	// What the FIRMWARE said became of the packets we asked it to deliver
	// reliably. TX counts what we handed over; these count what happened
	// to it. Retransmission used to be entirely invisible from here, so a
	// run could not tell a retry that worked from one that never ran.
	Acked, GaveUp, Outstanding int
	// The radio's own view of its outgoing queue, and how many packets it
	// REFUSED to take. A refusal never went on the air, however healthy
	// the tx counter looked.
	QueueFree, QueueMax, Refused int
	QueueKnown                   bool

	// Reconnecting distinguishes "there is no radio configured" from "the
	// radio is gone and we are trying to get it back". Both show as not
	// connected, and they call for different reactions.
	Reconnecting bool
	// Attempts counts dials since the link first went down, Reconnects the
	// ones that worked. A high Attempts with Reconnects climbing beside it
	// is a flapping link; a high Attempts alone is a radio that is not there.
	Attempts    int
	Reconnects  int
	NextRetryIn time.Duration
	// Channel is the mesh channel index this link transmits on. Surfaced
	// because "which channel am I actually on?" must never be a question
	// someone answers by guessing.
	Channel uint32
}

// Mesh reports radio state.
func (r *Runtime) Mesh() MeshStatus {
	r.mu.Lock()
	radio := r.mesh
	r.mu.Unlock()
	if radio == nil {
		return MeshStatus{}
	}
	s := radio.Status()
	acked, gaveUp, outstanding := radio.Delivery()
	qFree, qMax, refused, qKnown := radio.QueueState()
	return MeshStatus{
		Acked:        acked,
		GaveUp:       gaveUp,
		Outstanding:  outstanding,
		QueueFree:    qFree,
		QueueMax:     qMax,
		Refused:      refused,
		QueueKnown:   qKnown,
		Channel:      radio.Channel(),
		Connected:    s.Connected,
		NodeNum:      s.NodeNum,
		TX:           s.TX,
		RX:           s.RX,
		Err:          s.Err,
		Reconnecting: s.Reconnecting,
		Attempts:     s.Attempts,
		Reconnects:   s.Reconnects,
		NextRetryIn:  s.NextRetryIn,
	}
}

// SetMeshChannel selects the mesh channel index this node transmits on.
// Must be called BEFORE the radio is attached.
//
// Channel 0 is the node's PRIMARY channel, which on a real device is very
// often the public default-key channel every Meshtastic user in range shares.
// Transmitting there is visible to all of them and spends their airtime, so a
// segment of one's own belongs on a dedicated channel — that is the
// defence-in-depth the threat model asks for, on top of (never instead of)
// the end-to-end encryption the payload already carries.
func (r *Runtime) SetMeshChannel(index uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.meshChannel = index
}

// MeshChannel reports the configured transmit channel index.
func (r *Runtime) MeshChannel() uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.meshChannel
}

// ApplyRadioPlan writes a configuration plan to the attached radio. The radio
// reboots afterwards; the supervised link reconnects on its own, and the
// caller verifies by re-reading once it is back.
func (r *Runtime) ApplyRadioPlan(plan *meshtastic.ApplyPlan) error {
	r.mu.Lock()
	radio := r.mesh
	r.mu.Unlock()
	if radio == nil {
		return errors.New("node: no radio is attached")
	}
	return radio.Apply(plan)
}

// WaitForRadioCycle blocks until the radio has gone away and come back —
// i.e. until the reconnect counter passes `since` AND the link is up again.
//
// Waiting on "connected" alone does not work after a reboot request: the
// device takes a couple of seconds to actually reset, so the link is still up
// when the wait begins and it returns immediately. The caller then reads the
// PRE-reboot configuration and concludes, wrongly, that the write did not
// take. A monotonic counter cannot be missed that way; a transient boolean
// can. Pass the value of Mesh().Reconnects captured BEFORE the write.
func (r *Runtime) WaitForRadioCycle(since int, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if st := r.Mesh(); st.Reconnects > since && st.Connected {
			return true
		}
		select {
		case <-r.stop:
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
	st := r.Mesh()
	return st.Reconnects > since && st.Connected
}

// MeshTarget reports the device the radio link is dialled to, empty when no
// radio is attached.
func (r *Runtime) MeshTarget() string {
	r.mu.Lock()
	radio := r.mesh
	r.mu.Unlock()
	if radio == nil {
		return ""
	}
	return radio.Target()
}

// MeshAttached reports whether a radio has been attached at all —
// distinct from Connected, which is about the device being there right now.
// "No radio configured" and "the radio is gone" call for different actions.
func (r *Runtime) MeshAttached() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.meshSupervised
}

// SetRadioProfile installs the segment's expected radio settings, so the
// Gateway screen can say which field is wrong rather than only what the
// radio is set to. It arrives with the beta package, beside the gateway pin.
func (r *Runtime) SetRadioProfile(p *meshtastic.Profile) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.radioProfile = p
}

// RadioProfile reports the installed profile.
func (r *Runtime) RadioProfile() (meshtastic.Profile, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.radioProfile == nil {
		return meshtastic.Profile{}, false
	}
	return *r.radioProfile, true
}

// MeshConfig reports what the attached radio says it is configured for, for
// the diagnostic in transports/meshtastic. While a radio is connected this
// is live: a node re-sends the affected message when someone changes its
// settings, so the answer keeps up with a radio being reconfigured at the
// bench. While it is not, this is the last thing the radio said.
func (r *Runtime) MeshConfig() meshtastic.NodeConfig {
	r.mu.Lock()
	radio := r.mesh
	r.mu.Unlock()
	if radio == nil {
		return meshtastic.NodeConfig{}
	}
	return radio.Config()
}

// SetMeshReliable asks the radio to retransmit what goes unacknowledged.
//
// Default ON, and the reason is a measurement rather than a preference: on
// a shared LoRa channel 70-90% of packets were lost to other people's
// airtime, and with no retry a multi-fragment message never assembled — not
// one delivery in twenty minutes. Retransmission costs airtime on an
// already busy channel, so a node that would rather be quiet than heard can
// turn it off; a node that wants its messages to arrive should not have to
// discover this setting first.
func (r *Runtime) SetMeshReliable(on bool) {
	r.mu.Lock()
	r.meshReliable = on
	r.mu.Unlock()
}
