// One invitation, two authority modes.
//
// A person hands somebody a way in. That is one act, one record, one withdraw
// and one line on one screen — however the bytes travelled. What differs is
// not the concept but the AUTHORITY:
//
//	BEARER    the recipient is unknown. Five words, a pass, MaxUses, a TTL,
//	          custody at a relay. It can be opened later, by whoever holds
//	          the words. This is the ordinary quite.link.
//	TARGETED  the recipient is already known from a beacon. It names one
//	          device, rides a live radio link, and has no mailbox anywhere.
//	          It CANNOT be opened later without meeting again.
//
// A pass exists because the issuer does not know who will redeem it. On a
// segment where you pressed a person's name you DO know — you hold their
// device id and their key from the card they signed. Minting a pass there
// would buy three store-and-forward legs to solve a problem you do not have,
// and on this carrier a leg is the unit of risk.
//
// So the two share a JOURNAL, not a mechanism. That is the whole of the
// unification, and it is the part that was missing: a radio invitation lived
// in a memory-only map, invisible in "what did I hand out", not withdrawable,
// and gone on restart — which is why pressing again after a restart opened
// yet another empty room.
package node

import (
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// InvitationMode is how a way in was authorised.
type InvitationMode string

const (
	InvitationBearer   InvitationMode = "bearer"
	InvitationTargeted InvitationMode = "targeted"
)

// InvitationState is what became of it.
const (
	InvitationOffered   = "offered"
	InvitationAccepted  = "accepted"
	InvitationWithdrawn = "withdrawn"
)

// InvitationRecord is the local proof of what was handed out.
//
// It deliberately holds NO bearer material: not the words, not the sealed
// invite. Those were shown or sent once, and a node that keeps them is a node
// that can hand out the same access twice without the issuer knowing.
type InvitationRecord struct {
	ID    string         `json:"id"`
	Mode  InvitationMode `json:"mode"`
	Space string         `json:"space"`
	// Hint and PassID belong to a bearer invitation: they identify the sealed
	// payload and the pass that enforces its expiry and use count.
	Hint   string `json:"hint,omitempty"`
	PassID string `json:"pass_id,omitempty"`
	// Target is the device a targeted invitation names, hex. Empty for bearer,
	// because a bearer invitation is precisely one that names nobody.
	Target    string `json:"target,omitempty"`
	Note      string `json:"note,omitempty"`
	IssuedAt  int64  `json:"issued_at"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	State     string `json:"state,omitempty"`
	// Delivery is what the CARRIER observed, kept apart from State because
	// they answer different questions: State is what the people did, Delivery
	// is whether the bytes arrived. Conflating them is how "queued" came to
	// read as "sent".
	Delivery string `json:"delivery,omitempty"`
}

// setInvitationDelivery records what the carrier observed.
func (r *Runtime) setInvitationDelivery(invID, state string) {
	quickLinkMu.Lock()
	defer quickLinkMu.Unlock()
	st := r.loadQuickLinks()
	for i := range st.Invitations {
		if st.Invitations[i].ID == invID {
			st.Invitations[i].Delivery = state
			_ = r.saveQuickLinks(st)
			return
		}
	}
}

// Live reports whether this invitation is still a way in.
func (rec InvitationRecord) Live(now time.Time) bool {
	if rec.State == InvitationWithdrawn {
		return false
	}
	if rec.ExpiresAt != 0 && now.Unix() > rec.ExpiresAt {
		return false
	}
	return true
}

// recordInvitation appends one to the journal.
func (r *Runtime) recordInvitation(rec InvitationRecord) error {
	quickLinkMu.Lock()
	defer quickLinkMu.Unlock()
	st := r.loadQuickLinks()
	st.Invitations = append(st.Invitations, rec)
	return r.saveQuickLinks(st)
}

// Invitations lists what this node handed out, newest first.
func (r *Runtime) Invitations() []InvitationRecord {
	quickLinkMu.Lock()
	defer quickLinkMu.Unlock()
	st := r.loadQuickLinks()
	out := make([]InvitationRecord, len(st.Invitations))
	copy(out, st.Invitations)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// liveTargetedInvitation finds an unwithdrawn targeted invitation to a device.
//
// This is what makes a second press re-send rather than open another room, and
// it now survives a restart — which the memory-only map it replaces did not.
func (r *Runtime) liveTargetedInvitation(dev id.DeviceID) (InvitationRecord, bool) {
	quickLinkMu.Lock()
	defer quickLinkMu.Unlock()
	st := r.loadQuickLinks()
	want := hex.EncodeToString(dev[:])
	now := time.Now()
	for i := len(st.Invitations) - 1; i >= 0; i-- {
		rec := st.Invitations[i]
		if rec.Mode == InvitationTargeted && rec.Target == want && rec.Live(now) {
			return rec, true
		}
	}
	return InvitationRecord{}, false
}

// WithdrawInvitation closes a way in, whichever mode carried it.
//
// A bearer invitation additionally revokes its pass and tells anybody waiting
// at that door that the door is gone. A targeted one has no pass and no
// mailbox: withdrawing it is a local decision about what this node will honour
// and re-offer, which is stated rather than dressed up as a recall.
func (r *Runtime) WithdrawInvitation(invID string) error {
	quickLinkMu.Lock()
	st := r.loadQuickLinks()
	var rec *InvitationRecord
	for i := range st.Invitations {
		if st.Invitations[i].ID == invID {
			rec = &st.Invitations[i]
			break
		}
	}
	if rec == nil {
		quickLinkMu.Unlock()
		return errors.New("node: no such invitation was issued from this device")
	}
	rec.State = InvitationWithdrawn
	mode, passID := rec.Mode, rec.PassID
	err := r.saveQuickLinks(st)
	quickLinkMu.Unlock()
	if err != nil {
		return err
	}
	if mode == InvitationBearer && passID != "" {
		r.declinePendingForPass(passID, "the link was withdrawn")
		return r.RevokePass(passID)
	}
	return nil
}

// markInvitationAccepted records that somebody came through.
func (r *Runtime) markInvitationAccepted(invID string) {
	quickLinkMu.Lock()
	defer quickLinkMu.Unlock()
	st := r.loadQuickLinks()
	for i := range st.Invitations {
		if st.Invitations[i].ID == invID {
			st.Invitations[i].State = InvitationAccepted
			_ = r.saveQuickLinks(st)
			return
		}
	}
}

// newInvitationID mints a local identifier. Random rather than a counter: the
// journal is rewritten wholesale, and a counter would collide with itself
// after a restore from backup.
func newInvitationID() string {
	b, err := randomBytes(8)
	if err != nil {
		return fmt.Sprintf("inv-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Rendezvous custody: whether somewhere HOLDS a message for a peer who is not
// here, or whether the exchange only works while both are on the air.
type custody int

const (
	custodyHeld custody = iota
	custodyLiveOnly
)

// radioRendezvousPrefix marks an address that is a radio segment rather than a
// relay. A scheme on a string, so every PassRecord and JoinRecord already on
// disk loads unchanged — the field keeps its meaning and gains a vocabulary.
const radioRendezvousPrefix = "radio:"

// custodyOf reads the scheme. An address with no scheme is a relay, which is
// what every existing record holds.
func custodyOf(addr string) custody {
	if len(addr) >= len(radioRendezvousPrefix) &&
		addr[:len(radioRendezvousPrefix)] == radioRendezvousPrefix {
		return custodyLiveOnly
	}
	return custodyHeld
}

// relayShaped reports whether falling back to the configured relay could ever
// be right for this address.
//
// It exists because of a real trap: entry.go falls back to the global relay
// when a record carries no address, for legacy records minted before RR-0.
// Applied to a live-only rendezvous that fallback would seal a host's decision
// onto a relay the guest has never looked at — and report success. An empty
// address is legacy; a radio address is not missing, it is different.
func relayShaped(addr string) bool { return custodyOf(addr) == custodyHeld }
