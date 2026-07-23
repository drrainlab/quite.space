// Meshtastic wiring (M1.7): the runtime can attach one radio and sync all
// spaces over the mesh at LoRa-respectful cadence. Airtime honesty: the
// summary interval is a minute, not milliseconds — a LoRa mesh is a
// low-bandwidth projection of the same Terminal, never a fast pipe
// pretending otherwise (invariant §2.6).
package node

import (
	"errors"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/transports"
	"github.com/drrainlab/quiet_places/transports/compact"
	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

// Mesh cadence; vars so tests can accelerate them.
var (
	meshPumpEvery    = 2 * time.Second
	meshSummaryEvery = 60 * time.Second
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
	return r.startMesh(target, true)
}

func (r *Runtime) startMesh(target string, compactOn bool) error {
	r.mu.Lock()
	if r.mesh != nil {
		if closed, _ := r.mesh.Closed(); !closed {
			r.mu.Unlock()
			return errors.New("node: a radio is already connected")
		}
	}
	r.mu.Unlock()

	var radio *meshtastic.Radio
	var err error
	switch {
	case strings.HasPrefix(target, "tcp:"):
		radio, err = meshtastic.DialTCP(strings.TrimPrefix(target, "tcp:"))
	case strings.HasPrefix(target, "serial:"):
		radio, err = meshtastic.OpenSerial(strings.TrimPrefix(target, "serial:"))
	default:
		return errors.New("node: mesh target must be tcp:HOST[:PORT] or serial:/dev/PATH")
	}
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.mesh = radio
	r.mu.Unlock()
	var lk link = radio
	if compactOn {
		lk = compactLink{Endpoint: compact.Wrap(radio), radio: radio}
	}
	r.adoptLink(lk, meshPumpEvery, meshSummaryEvery, "radio")
	return nil
}

// compactLink pairs the compact-wrapped endpoint with the radio's liveness.
type compactLink struct {
	transports.Endpoint
	radio *meshtastic.Radio
}

func (c compactLink) Closed() (bool, error) { return c.radio.Closed() }

// MeshStatus is the transport diagnostic for the UI.
type MeshStatus struct {
	Connected bool
	NodeNum   uint32
	TX, RX    int
	Err       string
}

// Mesh reports radio state.
func (r *Runtime) Mesh() MeshStatus {
	r.mu.Lock()
	radio := r.mesh
	r.mu.Unlock()
	if radio == nil {
		return MeshStatus{}
	}
	closed, err := radio.Closed()
	st := MeshStatus{Connected: !closed, NodeNum: radio.NodeNum()}
	st.TX, st.RX = radio.Stats()
	if err != nil {
		st.Err = err.Error()
	}
	return st
}
