// Pinned custodians (TN-B, ADR-015 §7): a node honors a bridge's signed
// custody ACK only when the custodian public key is PINNED for the link
// domain the receipt arrived on. TOFU is forbidden by default; rotation is
// an explicit pin update. A valid signature under an unpinned key records
// nothing (a local unconfirmed observation at most).
package node

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/bridge"
)

// PinCustodian trusts a custodian public key for a link domain label
// (e.g. "lan", "radio", "relay:host"). Caller supplies the key out-of-band
// (the bridge prints it at startup).
func (r *Runtime) PinCustodian(linkDomain string, pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("node: custodian key must be 32 bytes")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.custodians == nil {
		r.custodians = map[string][]byte{}
	}
	r.custodians[linkDomain] = append([]byte(nil), pub...)
	return nil
}

// handleCustodyReceipt verifies a receipt against the pin for the link it
// arrived on and records accepted_by_relay for the covered events. Caller
// holds r.mu (invoked from the engine callback under the pump lock).
func (r *Runtime) handleCustodyReceipt(tid id.TerminalID, raw []byte) {
	st, ok := r.spaces[tid]
	if !ok {
		return
	}
	rec, err := bridge.DecodeReceipt(raw)
	if err != nil {
		return // bad signature: nothing recorded
	}
	pin, ok := r.custodians[r.curLink]
	if !ok || !bytes.Equal(pin, rec.PublicKey) {
		return // unpinned key: local observation only, never a claim
	}
	if rec.Lapsed {
		// The gateway is WITHDRAWING a claim it made earlier: it held these
		// frames and could not keep them to the promised time. The delivery
		// ladder is closed and has no rung for "was carried, then wasn't",
		// so nothing is un-recorded — a receipt that was true when issued
		// stays true. What changes is what the person is shown: this is
		// recorded locally so the UI can stop implying the message is still
		// somewhere on its way, and so the RB-1 delivery ledger can retry.
		for _, eid := range rec.FrameIDs {
			r.noteCustodyLapsed(eid, tid, rec.Instance)
		}
		return
	}
	if rec.ExpiresAt != 0 && uint64(time.Now().Unix()) >= rec.ExpiresAt {
		return
	}
	for _, eid := range rec.FrameIDs {
		_ = st.space.Trust.RecordTransportReceipt(eid, tid, claims.DeliveryAcceptedByRelay)
		r.trackCarried(eid, "bridge")
	}
}

// CustodyLapse is a gateway's withdrawal of a custody claim, kept locally.
type CustodyLapse struct {
	Space    id.TerminalID
	Instance string
	At       time.Time
}

// maxCustodyLapses bounds the local record (diagnostics, not a log).
const maxCustodyLapses = 512

// noteCustodyLapsed records a withdrawal. Caller holds r.mu.
func (r *Runtime) noteCustodyLapsed(eid id.EventID, tid id.TerminalID, instance string) {
	if r.custodyLapses == nil {
		r.custodyLapses = map[id.EventID]CustodyLapse{}
	}
	if len(r.custodyLapses) >= maxCustodyLapses {
		for k := range r.custodyLapses {
			delete(r.custodyLapses, k)
			break
		}
	}
	r.custodyLapses[eid] = CustodyLapse{Space: tid, Instance: instance, At: time.Now()}
}

// CustodyLapsed reports whether a gateway withdrew custody of an event.
func (r *Runtime) CustodyLapsed(eid id.EventID) (CustodyLapse, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.custodyLapses[eid]
	return l, ok
}
