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
	if rec.ExpiresAt != 0 && uint64(time.Now().Unix()) >= rec.ExpiresAt {
		return
	}
	for _, eid := range rec.FrameIDs {
		_ = st.space.Trust.RecordTransportReceipt(eid, tid, claims.DeliveryAcceptedByRelay)
		r.trackCarried(eid, "bridge")
	}
}
