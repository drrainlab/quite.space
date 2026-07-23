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
func (r *Runtime) StartMeshtastic(target string) error {
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
	r.adoptLink(radio, meshPumpEvery, meshSummaryEvery, "radio")
	return nil
}

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
