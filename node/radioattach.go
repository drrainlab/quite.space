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
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/drrainlab/quiet_places/protocol/quicklink"
	"github.com/drrainlab/quiet_places/transports/rnode"

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
// An EMPTY phrase means "use the segment this device already knows" — the one
// that arrived in an invitation. That is the payoff of carrying it: a person
// who was invited plugs a board in and is asked nothing at all, because the
// words were never theirs to know. An empty phrase with no known segment is
// refused rather than derived from "".
func (r *Runtime) AttachRNode(device, phrase string) error {
	if device == "" {
		return errors.New("a serial device is required — scan for radios first")
	}
	var seed []byte
	if phrase == "" {
		r.mu.Lock()
		seed = append([]byte(nil), r.ks.Radio.Seed...)
		r.mu.Unlock()
		if len(seed) == 0 {
			return errors.New("this device knows no radio segment yet, so it " +
				"needs the phrase yours shares. A segment arrives on its own " +
				"with an invitation from somebody who already has a radio")
		}
	} else if s, err := radiotransfer.SeedFromPhrase(phrase); err != nil {
		return fmt.Errorf("segment phrase: %w", err)
	} else {
		seed = s
	}
	// StartRNodeTransfer remembers the attachment itself, and only after the
	// radio actually came up: storing the intent first would leave a node
	// trying to attach a device that never worked, every start, with the
	// failure a little further from its cause each time.
	return r.StartRNodeTransfer(device, seed)
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

// SegmentDescriptor is this node's radio segment, for an invitation to carry.
//
// Absent when no radio is attached, which is the ordinary case and never an
// error: an invitation without a segment is exactly today's invitation.
//
// It is built from the STORED attachment rather than from the live radio, so
// a board that is momentarily unplugged does not silently strip the segment
// out of every link minted while the cable is out. What the person configured
// is what they are sharing.
func (r *Runtime) SegmentDescriptor() quicklink.RadioSegment {
	r.mu.Lock()
	rec := r.ks.Radio
	r.mu.Unlock()
	if len(rec.Seed) == 0 || rec.Carrier == "" {
		return quicklink.RadioSegment{}
	}
	return quicklink.RadioSegment{
		KDFVersion: uint64(radiotransfer.KDFVersion),
		Carrier:    rec.Carrier,
		Profile:    rnode.ProfileLongFastRU,
		Seed:       append([]byte(nil), rec.Seed...),
	}
}

// AdoptSegment records a segment that arrived in an invitation.
//
// This is the point of the whole exercise: the configuration lands BEFORE it
// is needed. Automatic failover assumes both devices already hold a compatible
// segment, and the moment the internet disappears is precisely the moment
// nobody can send anybody one.
//
// Three refusals, each because the alternative is a radio that hears nobody:
//
//   - a descriptor this build cannot act on (already validated on decode)
//   - a carrier or profile this build does not speak
//   - a DIFFERENT segment when one is already configured
//
// The last is the one worth arguing about. Silently re-keying somebody's air
// because they opened a link would take a radio they had working and point it
// somewhere else, with the only symptom being silence. A person can always
// detach and attach again; nothing here decides that for them.
func (r *Runtime) AdoptSegment(seg quicklink.RadioSegment) error {
	if !seg.Present() {
		return nil
	}
	if err := seg.Validate(); err != nil {
		return err
	}
	if seg.Carrier != carrierRNode {
		return fmt.Errorf("that invitation is for a %q radio, and this build "+
			"attaches %q", seg.Carrier, carrierRNode)
	}
	if _, ok := rnode.SettingsForProfile(seg.Profile); !ok {
		return fmt.Errorf("that invitation names the radio profile %q, which "+
			"this build does not know", seg.Profile)
	}
	if uint32(seg.KDFVersion) != radiotransfer.KDFVersion {
		return fmt.Errorf("that segment derives its key with version %d and "+
			"this build uses %d — every radio on a segment must agree, so "+
			"nothing is derived rather than deriving a key nobody else holds",
			seg.KDFVersion, radiotransfer.KDFVersion)
	}

	r.mu.Lock()
	cur := r.ks.Radio
	same := bytes.Equal(cur.Seed, seg.Seed)
	if len(cur.Seed) > 0 && !same {
		r.mu.Unlock()
		return errors.New("this device is already on a different radio segment. " +
			"Adopting this one would point the radio at air the people you " +
			"already share it with are not on. Detach the radio first if that " +
			"is what you want")
	}
	if same {
		r.mu.Unlock()
		return nil // already ours; nothing to do and nothing to say
	}
	// The DEVICE is deliberately left empty. A segment is known long before a
	// board is plugged in — often on a machine that has no radio at all — and
	// that gap is the entire value: when a radio does arrive, nobody has to be
	// asked for a phrase, and nobody has to be reachable to supply one.
	r.ks.Radio = storage.RadioRecord{
		Carrier: seg.Carrier, Seed: append([]byte(nil), seg.Seed...),
	}
	r.mu.Unlock()
	return r.saveKeystore()
}

// KnownSegment reports whether this device has a segment it could bring a
// radio up on without asking anybody anything.
func (r *Runtime) KnownSegment() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ks.Radio.Seed) > 0
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
