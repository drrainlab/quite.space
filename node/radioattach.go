// Attaching and detaching a radio from the interface, and remembering it.
//
// Until this file existed a radio could only be attached by a flag at startup,
// which meant it could not be attached from the application at all — and there
// was NO detach path anywhere in the tree for any carrier. meshSupervised was
// set to true and never set back, so the first attach was the last one until
// the process died. An attach button without a detach is a trap, so both are
// here, and both go through the one place that keeps the invariant the runtime
// has always claimed: ONE radio per node, whatever the carrier.
package node

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
)

// carrierRNode is the stored name of the modem driver. A name, never
// something to branch policy on — that is the leak the carrier-neutral face
// exists to close.
const carrierRNode = "rnode"

// AttachRNode brings up an RNode modem and remembers it for next time.
//
// phrase is what a PERSON types: the words a segment shares. It is turned into
// a seed by the one derivation in the tree, so a node attached here and a node
// started with --mesh-seed land on the same segment when the words match — and
// there is no second implementation that could quietly disagree.
//
// The phrase itself is not kept. Only the derived seed is stored, because
// nothing needs the words again and a phrase is the form of this secret a
// person is most likely to have reused somewhere else.
func (r *Runtime) AttachRNode(device, phrase string) error {
	if device == "" {
		return errors.New("a serial device is required — scan for radios first")
	}
	seed, err := radiotransfer.SeedFromPhrase(phrase)
	if err != nil {
		return fmt.Errorf("segment phrase: %w", err)
	}
	if err := r.StartRNodeTransfer(device, seed); err != nil {
		return err
	}
	// Remembered only AFTER the radio actually came up. Storing the intent
	// first would leave a node trying to attach a device that never worked,
	// every start, with the failure a little further from its cause each time.
	r.mu.Lock()
	r.ks.Radio = storage.RadioRecord{
		Carrier: carrierRNode, Device: device,
		Seed: append([]byte(nil), seed...),
	}
	r.mu.Unlock()
	return r.saveKeystore()
}

// DetachRadio puts the radio down and forgets it.
//
// Forgetting is not a detail: without it the next start would bring back a
// radio somebody had just switched off, which is the kind of disobedience that
// makes people stop trusting a switch.
func (r *Runtime) DetachRadio() error {
	r.mu.Lock()
	radio, ep, lk := r.rnodeRadio, r.rnodeEP, r.rnodeLink
	attached := r.meshSupervised
	r.rnodeRadio, r.rnodeEP, r.rnodeLink = nil, nil, nil
	r.meshSupervised = false
	r.meshSeed = nil
	r.ks.Radio = storage.RadioRecord{}
	r.mu.Unlock()

	if !attached {
		return errors.New("no radio is attached")
	}
	// Out of the link registry BEFORE the endpoint dies, so the pump never
	// gets a tick at a half-closed link.
	if lk != nil {
		r.dropConn(lk)
	}
	if ep != nil {
		ep.Close()
	}
	if radio != nil {
		// The port must actually be released, or the next scan reports this
		// very process as "something else is using this port".
		_ = radio.Close()
	}
	return r.saveKeystore()
}

// restoreRadio brings back the radio this device was last attached to.
//
// BEST EFFORT, and that is a decision rather than laziness: these boards
// enumerate under a different serial path after a reset, and a radio that is
// simply unplugged is an ordinary Tuesday. Neither may stop a person opening
// their own data. The failure is not swallowed either — it comes back as the
// error on the carrier-neutral status, where a screen can say it.
func (r *Runtime) restoreRadio() {
	r.mu.Lock()
	rec := r.ks.Radio
	r.mu.Unlock()
	if !rec.Attached() || rec.Carrier != carrierRNode {
		return
	}
	if err := r.StartRNodeTransfer(rec.Device, rec.Seed); err != nil {
		// Kept, not cleared. A person who attached this radio still means to
		// have it; the cable will be back. Clearing here would silently turn
		// "unplugged" into "never configured".
		r.mu.Lock()
		r.radioRestoreErr = err
		r.mu.Unlock()
	}
}

// handleRadioAttach brings up a modem the scan found.
//
// The phrase is refused rather than trimmed or padded. A segment phrase is
// the one input here where being helpful is harmful: silently altering it
// produces a node that is confidently on a segment nobody else is on, and the
// only symptom is silence.
func (a *APIServer) handleRadioAttach(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Port   string `json:"port"`
		Phrase string `json:"phrase"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("port and phrase required"))
		return
	}
	if err := a.rt.AttachRNode(strings.TrimSpace(body.Port), body.Phrase); err != nil {
		httpErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, a.rt.RadioState())
}

// handleRadioDetach puts the radio down and forgets it.
func (a *APIServer) handleRadioDetach(w http.ResponseWriter, r *http.Request) {
	if err := a.rt.DetachRadio(); err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
