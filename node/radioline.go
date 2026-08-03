// Starting a line over the radio, in four addressed messages.
//
//	Alice -> Bob   OFFER    "there is a place, do you want in"
//	Bob   -> Alice ACCEPT   "yes, and here is the device to seal it to"
//	Alice -> Bob   GRANT    membership + epoch material
//	Bob   -> Alice COMMIT   "I am in"
//
// WHY FOUR AND NOT ONE. The one-shot form calls MintInvite, and MintInvite
// adds the member and rotates the epoch AT MINT (node/node.go:718-724) —
// before the invitation has been sent, let alone received or wanted. On a
// carrier where failure takes minutes that leaves a space carrying somebody
// who is not there, and an epoch rotation nobody needed. Nothing is added and
// nothing rotates here until Bob has said yes.
//
// The extra legs are affordable only because the peer link proved presence
// first: without it these would be four more chances to discover that nobody
// was listening. With it, they are four short exchanges between two radios
// that have already answered each other.
//
// Every message is bound to ONE invitation_id, so a repeat is recognised
// rather than acted on twice, and a lost answer leaves a state that names
// exactly how far the exchange got.
package node

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
)

// Message kinds for the line saga, continuing the set in radioinvite.go and
// radiopeer.go. Append-only.
const (
	radioMsgLineOffer = 5
	radioMsgAccept    = 6
	radioMsgGrant     = 7
	radioMsgCommit    = 8
)

const (
	domainLineOffer = "qs.radio.offer.v1"
	domainAccept    = "qs.radio.accept.v1"
	domainGrant     = "qs.radio.grant.v1"
	domainCommit    = "qs.radio.commit.v1"
)

// lineDeadline is how long any one leg of the saga may take.
//
// Longer than a probe, because these carry real content and are worth some
// patience; far shorter than the sync give-up budget, because somebody is
// standing there watching.
const lineDeadline = 90 * time.Second

// Wire keys for the saga bodies. Append-only.
const (
	lbInvite  = 1
	lbFrom    = 2
	lbTo      = 3
	lbSpace   = 4
	lbTitle   = 5
	lbName    = 6
	lbXpub    = 7
	lbPayload = 8
)

type lineBody struct {
	Invite  string
	From    id.DeviceID
	To      id.DeviceID
	Space   id.TerminalID
	Title   string
	Name    string
	Xpub    [32]byte
	Payload []byte
}

// encodeLineBody writes the fields the caller set, keys strictly ascending.
//
// Arity is COMPUTED, never a literal: the decoder enforces ascending keys, so
// a map whose header disagrees with its contents decodes as nothing — which on
// a radio is silence, and silence looks exactly like being out of range.
func encodeLineBody(b lineBody) []byte {
	n := 3 // invite, from, to are always present
	if b.Space != (id.TerminalID{}) {
		n++
	}
	if b.Title != "" {
		n++
	}
	if b.Name != "" {
		n++
	}
	if b.Xpub != ([32]byte{}) {
		n++
	}
	if len(b.Payload) > 0 {
		n++
	}
	out := codec.AppendMap(nil, n)
	out = codec.AppendUint(out, lbInvite)
	out = codec.AppendText(out, b.Invite)
	out = codec.AppendUint(out, lbFrom)
	out = codec.AppendBytes(out, b.From[:])
	out = codec.AppendUint(out, lbTo)
	out = codec.AppendBytes(out, b.To[:])
	if b.Space != (id.TerminalID{}) {
		out = codec.AppendUint(out, lbSpace)
		out = codec.AppendBytes(out, b.Space[:])
	}
	if b.Title != "" {
		out = codec.AppendUint(out, lbTitle)
		out = codec.AppendText(out, b.Title)
	}
	if b.Name != "" {
		out = codec.AppendUint(out, lbName)
		out = codec.AppendText(out, b.Name)
	}
	if b.Xpub != ([32]byte{}) {
		out = codec.AppendUint(out, lbXpub)
		out = codec.AppendBytes(out, b.Xpub[:])
	}
	if len(b.Payload) > 0 {
		out = codec.AppendUint(out, lbPayload)
		out = codec.AppendBytes(out, b.Payload)
	}
	return out
}

func decodeLineBody(raw []byte) (lineBody, error) {
	var b lineBody
	d := codec.NewDecoder(raw)
	m, err := d.ReadMapHeader()
	if err != nil {
		return b, err
	}
	for range m.Len() {
		k, err := d.ReadUint()
		if err != nil {
			return b, err
		}
		switch k {
		case lbInvite:
			b.Invite, err = d.ReadText()
			if len(b.Invite) > 64 {
				err = errors.New("node: invitation id too long")
			}
		case lbFrom:
			err = readFixed(d, b.From[:])
		case lbTo:
			err = readFixed(d, b.To[:])
		case lbSpace:
			err = readFixed(d, b.Space[:])
		case lbTitle:
			b.Title, err = d.ReadText()
			b.Title = clip(b.Title, maxRadioTitle)
		case lbName:
			b.Name, err = d.ReadText()
			b.Name = clip(b.Name, maxRadioName)
		case lbXpub:
			err = readFixed(d, b.Xpub[:])
		case lbPayload:
			b.Payload, err = d.ReadBytes()
			if len(b.Payload) > maxRadioInvite {
				err = fmt.Errorf("node: grant payload of %d bytes exceeds %d",
					len(b.Payload), maxRadioInvite)
			}
		default:
			err = d.SkipItem()
		}
		if err != nil {
			return b, err
		}
	}
	return b, nil
}

// ---- Alice's side ----

// OfferLineOverRadio creates a place and OFFERS it, without granting anything.
//
// This is the step that used to grant. Now it says only "there is a place",
// which is all that can honestly be said before the other person has answered.
func (r *Runtime) OfferLineOverRadio(dev id.DeviceID) (id.TerminalID, error) {
	ep, err := r.radioControl()
	if err != nil {
		return id.TerminalID{}, err
	}
	r.radioPeerOnce()
	r.meet.mu.Lock()
	n := r.meet.neighbour[dev]
	r.meet.mu.Unlock()
	if n == nil {
		return id.TerminalID{}, fmt.Errorf("node: no radio neighbour %s has been "+
			"heard — they have to announce themselves first", dev.String()[:8])
	}

	// A second press re-offers the SAME place, from the durable journal.
	if rec, existing := r.liveTargetedInvitation(dev); existing {
		if tid, err := id.ParseTerminalID(rec.Space); err == nil {
			r.mu.Lock()
			_, alive := r.spaces[tid]
			r.mu.Unlock()
			if alive {
				return tid, r.sendLineOffer(ep, rec.ID, tid, dev)
			}
		}
	}

	// Unnamed on purpose: QL-3 projects the name from who is in it, so once
	// they accept, the row reads as their name rather than as a title
	// somebody had to invent for a person they just met.
	tid, err := r.CreateSpace("")
	if err != nil {
		return id.TerminalID{}, err
	}
	invID := newInvitationID()
	if err := r.recordInvitation(InvitationRecord{
		ID: invID, Mode: InvitationTargeted, Space: tid.Hex(),
		Target: hex.EncodeToString(dev[:]), IssuedAt: time.Now().Unix(),
		State: InvitationOffered,
	}); err != nil {
		return tid, fmt.Errorf("node: the place exists but the invitation could "+
			"not be recorded, so pressing again would open a second one: %w", err)
	}
	return tid, r.sendLineOffer(ep, invID, tid, dev)
}

func (r *Runtime) sendLineOffer(ep radioControlEndpoint, invID string,
	tid id.TerminalID, dev id.DeviceID) error {

	body := encodeLineBody(lineBody{
		Invite: invID, From: r.Device.ID, To: dev, Space: tid,
		Title: clip(r.spaceTitle(tid), maxRadioTitle),
		Name:  clip(r.DisplayName(), maxRadioName),
	})
	// TAGGED, so the record can say whether they heard it. Without the tag the
	// screen can only report "queued", which on this carrier is
	// indistinguishable from "gone" for minutes.
	return ep.SendControlTagged(tagOffer+invID,
		signedRadio(radioMsgLineOffer, domainLineOffer, body, r.Device.SignKey()),
		lineDeadline)
}

// onRadioAccept is where membership is FINALLY granted — and the first moment
// it would be honest to.
func (r *Runtime) onRadioAccept(_ radiotransfer.RadioAddress, msg []byte) {
	_, raw, ok := peekSignedRadio(msg)
	if !ok {
		return
	}
	claimed, err := decodeLineBody(raw)
	if err != nil || claimed.To != r.Device.ID || claimed.From == r.Device.ID {
		return
	}
	if _, _, err := openSignedRadio(msg, domainAccept, claimed.From); err != nil {
		return
	}
	ep, err := r.radioControl()
	if err != nil {
		return
	}
	// The invitation must be one WE offered, to THIS device, and still live.
	rec, ok := r.invitationByID(claimed.Invite)
	if !ok || rec.Mode != InvitationTargeted ||
		rec.Target != hex.EncodeToString(claimed.From[:]) ||
		!rec.Live(time.Now()) {
		return
	}
	tid, err := id.ParseTerminalID(rec.Space)
	if err != nil {
		return
	}
	// NOW: add the member and rotate. Not a moment earlier, which is the whole
	// difference between this and the one-shot form.
	invite, err := r.MintInvite(tid, claimed.From, claimed.Xpub)
	if err != nil {
		return
	}
	rawInvite, err := base64.StdEncoding.DecodeString(invite)
	if err != nil {
		return
	}
	// RAW bytes, not base64. Measured on two boards: base64 cost a third more
	// bytes, which on this carrier is frames, which is seconds.
	body := encodeLineBody(lineBody{
		Invite: rec.ID, From: r.Device.ID, To: claimed.From,
		Space: tid, Payload: rawInvite,
	})
	_ = ep.SendControlWithin(
		signedRadio(radioMsgGrant, domainGrant, body, r.Device.SignKey()),
		lineDeadline)
}

// onRadioCommit records that somebody actually came through.
func (r *Runtime) onRadioCommit(_ radiotransfer.RadioAddress, msg []byte) {
	_, raw, ok := peekSignedRadio(msg)
	if !ok {
		return
	}
	claimed, err := decodeLineBody(raw)
	if err != nil || claimed.To != r.Device.ID {
		return
	}
	if _, _, err := openSignedRadio(msg, domainCommit, claimed.From); err != nil {
		return
	}
	rec, ok := r.invitationByID(claimed.Invite)
	if !ok || rec.Target != hex.EncodeToString(claimed.From[:]) {
		return
	}
	r.markInvitationAccepted(rec.ID)
}

// ---- Bob's side ----

// onRadioLineOffer holds an offer until the person answers it.
//
// Nothing is joined automatically — the rule QL-2 settled for doors: an
// invitation is a question, and answering it is somebody's own act.
func (r *Runtime) onRadioLineOffer(_ radiotransfer.RadioAddress, msg []byte) {
	_, raw, ok := peekSignedRadio(msg)
	if !ok {
		return
	}
	claimed, err := decodeLineBody(raw)
	if err != nil || claimed.To != r.Device.ID || claimed.From == r.Device.ID {
		return
	}
	if _, _, err := openSignedRadio(msg, domainLineOffer, claimed.From); err != nil {
		return
	}
	r.mu.Lock()
	_, already := r.spaces[claimed.Space]
	r.mu.Unlock()
	if already {
		return // we are in it; the offer is a repeat
	}
	r.radioMeetOnce()
	r.meet.mu.Lock()
	defer r.meet.mu.Unlock()
	if _, seen := r.meet.offers[claimed.Invite]; seen {
		return
	}
	if len(r.meet.offers) >= maxRadioOffers {
		dropOldestOffer(r.meet.offers)
	}
	r.meet.offers[claimed.Invite] = &RadioOffer{
		ID: claimed.Invite, Space: claimed.Space, Title: claimed.Title,
		From: claimed.Name, Heard: time.Now(), offerer: claimed.From,
	}
}

// AcceptRadioLine answers an offer, which is what causes the other side to
// grant. The join itself happens when the grant arrives.
func (r *Runtime) AcceptRadioLine(invID string) error {
	ep, err := r.radioControl()
	if err != nil {
		return err
	}
	r.radioMeetOnce()
	r.meet.mu.Lock()
	off := r.meet.offers[invID]
	r.meet.mu.Unlock()
	if off == nil {
		return errors.New("node: no such invitation arrived over the radio")
	}
	body := encodeLineBody(lineBody{
		Invite: invID, From: r.Device.ID, To: off.offerer,
		Xpub: r.Device.X25519Pub,
	})
	return ep.SendControlWithin(
		signedRadio(radioMsgAccept, domainAccept, body, r.Device.SignKey()),
		lineDeadline)
}

// onRadioGrant joins, then says so.
func (r *Runtime) onRadioGrant(_ radiotransfer.RadioAddress, msg []byte) {
	_, raw, ok := peekSignedRadio(msg)
	if !ok {
		return
	}
	claimed, err := decodeLineBody(raw)
	if err != nil || claimed.To != r.Device.ID || len(claimed.Payload) == 0 {
		return
	}
	if _, _, err := openSignedRadio(msg, domainGrant, claimed.From); err != nil {
		return
	}
	// It must answer an offer WE were holding, from the SAME device. Otherwise
	// anybody in range who heard an invitation id could hand us a space.
	r.radioMeetOnce()
	r.meet.mu.Lock()
	off := r.meet.offers[claimed.Invite]
	r.meet.mu.Unlock()
	if off == nil || off.offerer != claimed.From {
		return
	}
	if _, err := r.JoinInvite(base64.StdEncoding.EncodeToString(claimed.Payload)); err != nil {
		return
	}
	r.meet.mu.Lock()
	delete(r.meet.offers, claimed.Invite)
	r.meet.mu.Unlock()

	if ep, err := r.radioControl(); err == nil {
		body := encodeLineBody(lineBody{
			Invite: claimed.Invite, From: r.Device.ID, To: claimed.From,
			Space: claimed.Space,
		})
		_ = ep.SendControlWithin(
			signedRadio(radioMsgCommit, domainCommit, body, r.Device.SignKey()),
			lineDeadline)
	}
}

// invitationByID reads one out of the durable journal.
func (r *Runtime) invitationByID(invID string) (InvitationRecord, bool) {
	for _, rec := range r.Invitations() {
		if rec.ID == invID {
			return rec, true
		}
	}
	return InvitationRecord{}, false
}

func dropOldestOffer(m map[string]*RadioOffer) {
	var oldest string
	var at time.Time
	first := true
	for k, v := range m {
		if first || v.Heard.Before(at) {
			oldest, at, first = k, v.Heard, false
		}
	}
	if !first {
		delete(m, oldest)
	}
}

// Tags for control messages whose outcome somebody is waiting on.
const (
	tagOffer = "offer:"
	tagProbe = "probe:"
)

// Delivery states for an invitation, as OBSERVED rather than assumed.
const (
	// DeliveryQueued means it is on its way and nothing is known yet.
	DeliveryQueued = "queued"
	// DeliveryHeard means the peer assembled it. Not that they read it, and
	// certainly not that they agreed — only that the bytes arrived.
	DeliveryHeard = "heard"
	// DeliveryUnheard means the transfer gave up. Nothing is waiting for them
	// anywhere: this is a live exchange, and it did not happen.
	DeliveryUnheard = "unheard"
)

// onRadioControlSent turns a transfer outcome into something a person can read.
func (r *Runtime) onRadioControlSent(tag string, err error) {
	switch {
	case len(tag) > len(tagOffer) && tag[:len(tagOffer)] == tagOffer:
		state := DeliveryHeard
		if err != nil {
			state = DeliveryUnheard
		}
		r.setInvitationDelivery(tag[len(tagOffer):], state)
	case len(tag) > len(tagProbe) && tag[:len(tagProbe)] == tagProbe:
		if err == nil {
			return // the ACK will settle it; a delivered probe proves nothing yet
		}
		// A probe that was given up on is the cheap answer this layer exists
		// to get: nobody is listening, and we know it in seconds rather than
		// after committing six frames of invitation.
		r.failProbe(tag[len(tagProbe):])
	}
}

// failProbe resolves a probing link that could not even be delivered.
func (r *Runtime) failProbe(devHex string) {
	dev, err := id.ParseDeviceID(devHex)
	if err != nil {
		return
	}
	r.radioPeerOnce()
	r.meet.mu.Lock()
	defer r.meet.mu.Unlock()
	if l := r.meet.peers[dev]; l != nil && l.State == PeerLinkProbing {
		l.State = PeerLinkNoAnswer
		l.ExpiresAt = time.Now()
	}
}
