// Revocation (MD-2, decision 3): the undo that makes pairing shippable —
// a device binding with no way back is an unrevocable key.
//
// What revocation does and does not do, honestly (plan, threat model):
// it stops the device SPEAKING at once — its later signals are refused
// permanently, never held, on every replica that learns the revocation —
// and everything it said BEFORE stands (ADR-002:50-52). It stops the
// device READING an owned space by rotating that space's epoch here and
// now; spaces owned by others rotate on their own schedule, and until
// they do the lost device still holds the keys it already held. The
// thief holds no root seed (decision 5), so it cannot mint and certify a
// replacement — which is what makes this a security control rather than
// a gesture.
package node

import (
	"errors"
	"fmt"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// RevokeDevice invalidates one of this person's devices from now on.
// Authority only; the device underfoot and unknown devices are refused.
func (r *Runtime) RevokeDevice(dev id.DeviceID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Principal == nil {
		return ErrNotAuthority
	}
	if dev == r.Device.ID {
		return errors.New("node: refusing to revoke the device you are standing on — " +
			"use the offline recovery bundle from another machine instead")
	}
	certified := false
	for _, rec := range r.ks.Certs {
		if rec.Device == dev {
			certified = true
		}
	}
	if !certified {
		return errors.New("node: unknown device — nothing to revoke")
	}

	// RevokedAt is the highest logical clock this node has absorbed
	// anywhere: everything the device said up to now stands, everything
	// stamped later is refused. A thief can forge higher clocks — and that
	// is exactly the region the refusal covers.
	var revAt uint64
	for _, st := range r.spaces {
		if c := st.space.MaxClock(); c > revAt {
			revAt = c
		}
	}
	rev := r.Principal.Revoke(dev, revAt)
	frame, err := rev.Encode()
	if err != nil {
		return err
	}
	r.ks.Revs = append(r.ks.Revs, storage.RevRecord{Device: dev, Frame: frame})
	if err := r.ident.store.AddRevocation(rev); err != nil {
		return err
	}

	// The revocation travels in the log — plaintext, like the certificate
	// it cancels, and for the same reason: it is proof for admission, and
	// hiding it from the layer that needs it is how a dead key keeps
	// working. Emitted into EVERY space this person is in, owned or not.
	for tid, st := range r.spaces {
		meta := r.ks.Spaces[tid]
		if meta.LocalOnly {
			continue
		}
		if _, err := r.Self.Emit(st.space, schemas.DeviceRevoked, frame,
			r.Self.DefaultAuthorship(), 0); err != nil {
			return fmt.Errorf("node: publishing revocation into %s: %w", tid, err)
		}
		// Owned spaces rotate NOW: the lost device keeps yesterday's keys,
		// so tomorrow's must be new ones it never receives.
		if meta.Owned {
			st.space.RemoveMember(dev)
			// Expansion cannot resurrect it: the revocation is already in
			// the store, and CertifiedDevices skips revoked outright.
			r.expandMembersLocked(st.space)
			if _, err := r.Self.RotateEpoch(st.space); err != nil {
				return fmt.Errorf("node: rotating %s after revocation: %w", tid, err)
			}
			if _, err := r.Self.RotateInstrumentEpoch(st.space); err != nil {
				return fmt.Errorf("node: rotating instruments of %s after revocation: %w", tid, err)
			}
			r.persistEpochsLocked(tid, st.space)
		}
	}
	// The trust store changed, so what is held may now be REJECTABLE —
	// reconsideration also throws revoked material out of the hold.
	r.admissionStateChanged()
	return r.saveKeystore()
}

// Devices lists this person's certified devices for the UI, with their
// revocation state. The device underfoot is marked so a screen can refuse
// to offer the one revocation that is always a mistake.
type DeviceInfo struct {
	Device  string `json:"device"` // hex id
	Label   string `json:"label"`  // human name; hex is the fallback, never the face
	This    bool   `json:"this"`
	Revoked bool   `json:"revoked"`
}

func (r *Runtime) Devices() []DeviceInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	revoked := map[id.DeviceID]bool{}
	for _, rec := range r.ks.Revs {
		revoked[rec.Device] = true
	}
	out := make([]DeviceInfo, 0, len(r.ks.Certs))
	for _, rec := range r.ks.Certs {
		// The assistant and the gateway are the runtime's own writers, not
		// the person's hands — a devices screen is about hands.
		if r.agent != nil && rec.Device == r.agent.Device.ID {
			continue
		}
		if r.gateway != nil && rec.Device == r.gateway.Device.ID {
			continue
		}
		label := rec.Label
		if label == "" && rec.Device == r.Device.ID {
			label = deviceName()
		}
		out = append(out, DeviceInfo{
			Device:  rec.Device.Hex(),
			Label:   label,
			This:    rec.Device == r.Device.ID,
			Revoked: revoked[rec.Device],
		})
	}
	return out
}

// Fingerprint names the person from the PUBLIC id, so it works on every
// device — a secondary holds no *identity.Principal to ask (MD-2), and the
// three callers that used to ask one panicked on the first paired phone.
func (r *Runtime) Fingerprint() string { return id.Fingerprint(r.PrincipalID[:]) }

// DataDir is where this runtime lives — the passcode file's home, among
// other things a shell needs to name.
func (r *Runtime) DataDir() string { return r.dataDir }

// ownDevicesLocked lists this person's certified, unrevoked devices — the
// hands that must hear everything the person is in. Caller holds r.mu. The
// assistant and the gateway are the runtime's own writers, not hands, and
// they have their own delivery story.
func (r *Runtime) ownDevicesLocked() []id.DeviceID {
	revoked := map[id.DeviceID]bool{}
	for _, rec := range r.ks.Revs {
		revoked[rec.Device] = true
	}
	out := make([]id.DeviceID, 0, len(r.ks.Certs))
	for _, rec := range r.ks.Certs {
		if revoked[rec.Device] {
			continue
		}
		if r.agent != nil && rec.Device == r.agent.Device.ID {
			continue
		}
		if r.gateway != nil && rec.Device == r.gateway.Device.ID {
			continue
		}
		out = append(out, rec.Device)
	}
	return out
}
