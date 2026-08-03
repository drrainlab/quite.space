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

	// want_ack is redundant under Radio Transfer and costs real airtime.
	//
	// The firmware's implicit acknowledgement only says a NEIGHBOUR rebroadcast
	// the packet; a SACK says which fragments the PEER actually holds. Paying
	// for both buys nothing and spends the band this layer exists to use
	// carefully.
	//
	// That sentence was already written below, beside the wire switch — but the
	// decision is consumed HERE, by Supervise, several lines earlier, so the
	// comment described an intention the code never carried out. Every Radio
	// Transfer frame has been paying twice. Deciding it where it is used is the
	// difference between a true comment and a false one.
	if wire == wireTransfer {
		reliable = false
	}

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
			radiotransfer.EndpointOptions{
				OnControl:     r.onRadioControl,
				OnControlSent: r.onRadioControlSent,
			})
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

// SendControl is forwarded EXPLICITLY, not inherited.
//
// transferLink embeds transports.Endpoint — an INTERFACE — so only that
// interface's own methods are promoted. SendControl is not one of them, so
// the control channel compiled, ran, and reported "no radio is attached with
// the transfer layer" on a node whose banner said radio-transfer. Found by
// running it against two real boards; a type assertion that silently does not
// match is invisible until something asks.
func (l transferLink) SendControl(msg []byte) error { return l.transfer.SendControl(msg) }
func (l transferLink) Close() error                 { l.transfer.Close(); return l.radio.Close() }

// The REST of the control surface, forwarded for the same reason and missed
// twice before this line existed.
//
// radioControl() finds a radio by asserting this type against an interface. An
// interface the type ALMOST satisfies fails the assertion silently, and the
// node then reports that no radio is attached — on a node whose banner says
// radio-transfer. That is what happened when the interface grew
// SendControlWithin and again when it grew SendControlTagged: every test
// passed, because the e2e harness embeds the CONCRETE *radiotransfer.Endpoint
// and gets every method for free, while production embeds the interface and
// gets three.
func (l transferLink) SendControlWithin(msg []byte, within time.Duration) error {
	return l.transfer.SendControlWithin(msg, within)
}
func (l transferLink) SendControlTagged(tag string, msg []byte, within time.Duration) error {
	return l.transfer.SendControlTagged(tag, msg, within)
}

// COMPILE-TIME, so this class of defect cannot reach a radio again.
//
// Three times now a method has been added to radioControlEndpoint and not to
// the type that must satisfy it, and three times the failure surfaced as "no
// radio is attached" in somebody's hand rather than as a build error. A
// silently-unsatisfied interface is invisible until something asks; this line
// asks at compile time.
var _ radioControlEndpoint = transferLink{}

// Credit is forwarded EXPLICITLY, for exactly the reason above — and it was
// left behind when SendControl was fixed.
//
// transports.Pacer is an OPTIONAL interface, so losing it is silent by
// construction: transports.CreditOf finds no Pacer, answers CreditUnlimited,
// and kernel/sync's Allows() returns true however full the radio is. The
// engine believed it was pacing against a carrier and was pacing against
// nothing — which is why three shipped backpressure fixes changed no measured
// outcome. Only the RAW wire ever reached the engine as a Pacer.
//
// The e2e helper endpointLink embeds the CONCRETE *radiotransfer.Endpoint and
// therefore promotes Credit for free, so the harness had the property
// production lacked and no test could see the difference.
func (l transferLink) Credit() transports.Credit { return l.transfer.Credit() }

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

// Credit is forwarded explicitly for the same reason transferLink's is.
// transports/compact already forwards it from the radio with a comment saying
// a wrapper that did not "would silently restore the flood"; embedding the
// Endpoint INTERFACE here dropped it again one layer further up.
func (c compactLink) Credit() transports.Credit { return transports.CreditOf(c.Endpoint) }

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

	// Transfer is whole-MESSAGE delivery, which is the number this project
	// spent nine days unable to see. Everything above counts packets, and a
	// packet count cannot tell a carrier problem from a reassembly one.
	// Present only on a link running the transfer layer.
	Transfer      *radiotransfer.Stats
	TransferQueue [3]int // waiting, dropped for want of room, failed outright
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
	var transfer *radiotransfer.Stats
	var tq [3]int
	r.mu.Lock()
	for _, l := range r.links {
		if tl, ok := l.c.(transferLink); ok {
			st := tl.TransferStats()
			transfer = &st
			w, d, f := tl.transfer.Queued()
			tq = [3]int{w, d, f}
		}
	}
	r.mu.Unlock()
	return MeshStatus{
		Transfer:      transfer,
		TransferQueue: tq,
		Acked:         acked,
		GaveUp:        gaveUp,
		Outstanding:   outstanding,
		QueueFree:     qFree,
		QueueMax:      qMax,
		Refused:       refused,
		QueueKnown:    qKnown,
		Channel:       radio.Channel(),
		Connected:     s.Connected,
		NodeNum:       s.NodeNum,
		TX:            s.TX,
		RX:            s.RX,
		Err:           s.Err,
		Reconnecting:  s.Reconnecting,
		Attempts:      s.Attempts,
		Reconnects:    s.Reconnects,
		NextRetryIn:   s.NextRetryIn,
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

// RadioStatus is the CARRIER-NEUTRAL face of a radio link.
//
// MeshStatus below it is a Meshtastic diagnostic wearing a generic name: node
// numbers, channel indices and firmware queue counters are that carrier's
// vocabulary, and a second driver would have to either fill them with
// nonsense or grow a parallel struct. What every radio can honestly answer is
// this much — is it there, is it coming back, and what is it moving in whole
// messages.
type RadioStatus struct {
	// Carrier names the driver, so a person reading a diagnostic knows which
	// one answered. It is a label, never something to branch on: policy that
	// switched on a carrier name is the leak this split exists to close.
	Carrier   string `json:"carrier"`
	Connected bool   `json:"connected"`
	// Reconnecting distinguishes "no radio configured" from "the radio is
	// gone and we are trying to get it back". Both show as not connected and
	// they call for different reactions.
	Reconnecting bool   `json:"reconnecting,omitempty"`
	Err          string `json:"error,omitempty"`

	// Transfer is whole-MESSAGE delivery — the number this project spent nine
	// days unable to see. Packet counts cannot tell a carrier problem from a
	// reassembly one. Present only on a link running the transfer layer.
	Transfer *radiotransfer.Stats `json:"transfer,omitempty"`
	// Waiting, Dropped and Failed are the outbound queue's own honesty:
	// queued, discarded for want of room, and given up on. Three different
	// answers that a single "failed" would have merged.
	Waiting int `json:"waiting,omitempty"`
	Dropped int `json:"dropped,omitempty"`
	Failed  int `json:"failed,omitempty"`
	// PeerLinks is how many peers this radio can currently reach by an
	// addressed path. Never a claim about hops.
	PeerLinks int `json:"peer_links,omitempty"`

	// Detail carries whatever the specific carrier wants to say. Nothing
	// generic reads it; it exists so carrier facts have somewhere honest to
	// live instead of being promoted into the neutral shape.
	Detail any `json:"detail,omitempty"`
}

// RadioState reports the carrier-neutral view, with the Meshtastic diagnostic
// carried underneath as detail.
func (r *Runtime) RadioState() RadioStatus {
	m := r.Mesh()
	out := RadioStatus{
		Carrier: "meshtastic", Connected: m.Connected,
		Reconnecting: m.Reconnecting, Err: m.Err,
		Transfer: m.Transfer,
		Waiting:  m.TransferQueue[0], Dropped: m.TransferQueue[1],
		Failed:   m.TransferQueue[2],
		Detail:   m,
	}
	r.radioPeerOnce()
	r.meet.mu.Lock()
	now := time.Now()
	for _, l := range r.meet.peers {
		if l.State == PeerLinkUp && now.Before(l.ExpiresAt) {
			out.PeerLinks++
		}
	}
	r.meet.mu.Unlock()
	return out
}
