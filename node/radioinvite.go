// Meeting somebody over the radio, with no relay and no internet.
//
// Every way into a space until now needed a rendezvous: a relay to leave a
// pass at, or a link pasted from one machine to another. On a LoRa segment in
// a field there is neither, and the radio is right there — so the radio
// carries the introduction too.
//
// Two messages, and the asymmetry between them is the whole design:
//
//	CARD    who I am: device id, public key, the name I go by. PUBLIC by
//	        nature — it is what somebody needs in order to seal an invite to
//	        me, and it discloses nothing that a radio exchange does not
//	        already disclose to anyone in range.
//	OFFER   an invite, already SEALED to one device's key by the ordinary
//	        MintInvite. Broadcasting it is safe because only that device can
//	        open it; everyone else hears bytes they cannot use.
//
// Nothing is joined automatically. An offer that arrives is held until the
// person accepts it — the same rule QL-2 settled for doors: an invitation is
// a question, and answering it is somebody's own act. Auto-joining would let
// anyone within radio range who has heard your card put you in a space.
package node

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
)

// Radio control message kinds.
const (
	radioMsgCard  = 1
	radioMsgOffer = 2
)

// Wire keys for a radio control message. Append-only.
const (
	rkKind    = 1
	rkDevice  = 2
	rkX25519  = 3
	rkName    = 4
	rkInvite  = 5
	rkSpace   = 6
	rkTitle   = 7
	rkFrom    = 8
	rkVersion = 9
)

// radioControlVersion is the grammar of these messages. A version this build
// does not know is ignored rather than guessed at.
const radioControlVersion = 1

// Bounds, checked on decode before anything is kept. These arrive from
// whoever is in radio range.
const (
	maxRadioName   = 64
	maxRadioTitle  = 96
	maxRadioInvite = 8 << 10
	maxRadioHeard  = 32
	maxRadioOffers = 16
)

// RadioNeighbour is somebody heard announcing themselves on the segment.
//
// Everything in it is a CLAIM. A device id and a public key are the two
// things needed to seal an invite, and they are self-asserted here — which is
// exactly as much as they were when a card travelled by QR code. What makes
// the invite safe is that it is sealed TO that key: an impostor who claims
// somebody else's name receives an invite they cannot open.
type RadioNeighbour struct {
	Device id.DeviceID `json:"-"`
	Name   string      `json:"name"`
	Heard  time.Time   `json:"heard"`
	x25519 [32]byte
	// addr is OBSERVED, never claimed: it is where this card actually arrived
	// from, which is the only way to answer the right radio. A card that named
	// its own address would be inviting somebody to point us elsewhere.
	addr radiotransfer.RadioAddress
	// eph is the ephemeral key from that card, and generation/expires are what
	// stop an old card being replayed after a restart.
	eph        [32]byte
	generation uint64
	expires    time.Time
	// Link is what we can honestly say about REACHING them right now. Nil
	// when nothing has been asked. Filled by RadioNeighbours, never stored.
	Link *NeighbourLink `json:"link,omitempty"`
}

// NeighbourLink is the peer link, reduced to what a screen may say.
//
// Direct is separate from State on purpose: "addressed" and "direct" are
// different claims, and only observed zero hops earns the second.
type NeighbourLink struct {
	State  string `json:"state"`
	Direct bool   `json:"direct"`
}

// MarshalJSON writes the device id as HEX.
//
// id.DeviceID is [32]byte, and Go marshals an array as a list of numbers. The
// screen then showed a row of integers, and the invite it posted back carried
// that list where the node expects the hex string ParseDeviceID reads — so
// the button could never have worked. Found by running two nodes over a real
// radio; both sides compiled and neither was wrong on its own.
func (n RadioNeighbour) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Device string    `json:"device"`
		Name   string    `json:"name"`
		Heard  time.Time `json:"heard"`
		// FULL hex, not String(). String() is a DISPLAY form — "device:"
		// plus a truncated digest — and feeding it back to ParseDeviceID
		// fails on the colon. The screen posts this value straight back, so
		// the two have to be the same alphabet.
		Link *NeighbourLink `json:"link,omitempty"`
	}{hex.EncodeToString(n.Device[:]), n.Name, n.Heard, n.Link})
}

// RadioOffer is an invite that arrived over the air and is waiting for an
// answer.
type RadioOffer struct {
	ID     string        `json:"id"`
	Space  id.TerminalID `json:"-"`
	Title  string        `json:"title"`
	From   string        `json:"from"`
	Heard  time.Time     `json:"heard"`
	invite string
	// offerer is the device that made this offer. A grant is accepted only
	// from the same device, or anybody in range who overheard an invitation id
	// could hand us a space.
	offerer id.DeviceID
}

// MarshalJSON writes the space id as hex, for the same reason as above.
func (o RadioOffer) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID    string    `json:"id"`
		Space string    `json:"space"`
		Title string    `json:"title"`
		From  string    `json:"from"`
		Heard time.Time `json:"heard"`
	}{o.ID, hex.EncodeToString(o.Space[:]), o.Title, o.From, o.Heard})
}

// radioMeet holds what has been heard on the segment. It is memory-only and
// bounded: this is a room somebody walked into, not a directory.
type radioMeet struct {
	mu        sync.Mutex
	neighbour map[id.DeviceID]*RadioNeighbour
	offers    map[string]*RadioOffer
	// peers holds the live peer links. Memory-only and short-lived: a link is
	// a fact about right now, and a durable one would be a claim about a radio
	// that may since have been switched off.
	peers map[id.DeviceID]*RadioPeerLink
	// generation changes every time this process starts, so a card minted by a
	// previous run cannot be replayed as current.
	generation uint64
	// myEph is the private half of the ephemeral key our last card advertised,
	// and myEphPub the public half. Kept so a probe answering that card can
	// complete the agreement; replaced on every announcement, because a key
	// reused across conversations stops being ephemeral.
	myEph    [32]byte
	myEphPub [32]byte
}

func newRadioMeet() *radioMeet {
	return &radioMeet{
		neighbour: map[id.DeviceID]*RadioNeighbour{},
		offers:    map[string]*RadioOffer{},
	}
}

// AnnounceOnRadio lives in node/radiopeer.go, where the card became SIGNED.
//
// The unsigned version that stood here bound nothing: an impostor could
// announce somebody else's device id alongside their own key, and the
// invitation meant for that person was sealed to the impostor instead. The
// comment above RadioNeighbour argued the arrangement was safe because an
// impostor receives an invite they cannot open — true for the person being
// impersonated, and false for the person doing the inviting.

// RadioNeighbours lists who has been heard, most recent first.
func (r *Runtime) RadioNeighbours() []RadioNeighbour {
	r.radioMeetOnce()
	r.meet.mu.Lock()
	defer r.meet.mu.Unlock()
	now := time.Now()
	out := make([]RadioNeighbour, 0, len(r.meet.neighbour))
	for dev, n := range r.meet.neighbour {
		cp := *n
		// Resolve the link the same way PeerLink does, so a screen and an API
		// never disagree about whether somebody is reachable.
		if l := r.meet.peers[dev]; l != nil {
			state := l.State
			if now.After(l.ExpiresAt) {
				switch state {
				case PeerLinkProbing:
					state = PeerLinkNoAnswer
				case PeerLinkUp:
					state = PeerLinkGone
				}
			}
			cp.Link = &NeighbourLink{State: string(state), Direct: l.Direct()}
		}
		out = append(out, cp)
	}
	sortByHeard(out)
	return out
}

// InviteOverRadio mints an invite for a heard neighbour and broadcasts it.
//
// The invite is sealed to that device's key by the ordinary MintInvite, so
// putting it on the air costs nothing in confidentiality: everyone in range
// hears bytes only the addressee can open. What IS disclosed is that an
// invitation happened, and to which device — a radio segment cannot hide
// that, and this does not pretend to.
func (r *Runtime) InviteOverRadio(tid id.TerminalID, dev id.DeviceID) error {
	ep, err := r.radioControl()
	if err != nil {
		return err
	}
	r.radioMeetOnce()
	r.meet.mu.Lock()
	n, ok := r.meet.neighbour[dev]
	r.meet.mu.Unlock()
	if !ok {
		return fmt.Errorf("node: no radio neighbour %s has been heard — they "+
			"have to announce themselves before an invite can be sealed to them",
			dev.String()[:8])
	}
	// A PUBLIC space is not entered by a sealed invitation, and saying so is
	// the difference between a person changing what they do and a person
	// staring at "private space has no epoch yet" about a space they created
	// as public.
	//
	// A sealed invite carries epoch key material, which is exactly what a
	// public space does not have: its content is signed plaintext that
	// anybody holding the address may read. The way in is the address, and
	// today that route needs a relay to fetch the projection from — which is
	// the one thing a radio segment in a field does not have. Carrying a
	// public space over the radio alone is real work and is not pretended at
	// here.
	r.mu.Lock()
	st, known := r.spaces[tid]
	r.mu.Unlock()
	if known && st.space.Policy().IsPublic() {
		return fmt.Errorf("node: %q is a public space, and a public space is not "+
			"entered by an invitation — its content is signed plaintext that "+
			"anyone holding the address can read, so there is nothing to seal to "+
			"one device. Meeting over the radio works for an ordinary space: make "+
			"one, open it, and invite from there", r.spaceTitle(tid))
	}

	inviteB64, err := r.MintInvite(tid, dev, n.x25519)
	if err != nil {
		return err
	}
	// RAW bytes on the air, not base64.
	//
	// MintInvite hands back a base64 string because that is what travels in a
	// link somebody pastes. Putting it inside a BINARY message costs a third
	// more bytes for nothing — and on this carrier bytes are frames and
	// frames are seconds: measured, the offer was 8 frames at 2.5s apart, so
	// the padding alone was costing about five seconds of a person's evening.
	invite, err := base64.StdEncoding.DecodeString(inviteB64)
	if err != nil {
		return err
	}
	if len(invite) > maxRadioInvite {
		return fmt.Errorf("node: this invite is %d bytes, and the radio carries "+
			"at most %d", len(invite), maxRadioInvite)
	}
	title := r.spaceTitle(tid)
	if len(title) > maxRadioTitle {
		title = title[:maxRadioTitle]
	}
	from := r.DisplayName()
	if len(from) > maxRadioName {
		from = from[:maxRadioName]
	}
	b := codec.AppendMap(nil, 7) // keys strictly ascending, as above
	b = codec.AppendUint(b, rkKind)
	b = codec.AppendUint(b, radioMsgOffer)
	b = codec.AppendUint(b, rkDevice)
	b = codec.AppendBytes(b, dev[:])
	b = codec.AppendUint(b, rkInvite)
	b = codec.AppendBytes(b, invite)
	b = codec.AppendUint(b, rkSpace)
	b = codec.AppendBytes(b, tid[:])
	b = codec.AppendUint(b, rkTitle)
	b = codec.AppendBytes(b, []byte(title))
	b = codec.AppendUint(b, rkFrom)
	b = codec.AppendBytes(b, []byte(from))
	b = codec.AppendUint(b, rkVersion)
	b = codec.AppendUint(b, radioControlVersion)
	return ep.SendControl(b)
}

// RadioOffers lists invitations waiting for an answer.
func (r *Runtime) RadioOffers() []RadioOffer {
	r.radioMeetOnce()
	r.meet.mu.Lock()
	defer r.meet.mu.Unlock()
	out := make([]RadioOffer, 0, len(r.meet.offers))
	for _, o := range r.meet.offers {
		out = append(out, *o)
	}
	return out
}

// AcceptRadioOffer joins the space an offer names.
//
// The one act that changes durable state, and it is the person's. An offer
// sitting in the list has cost them nothing.
func (r *Runtime) AcceptRadioOffer(offerID string) (id.TerminalID, error) {
	r.radioMeetOnce()
	r.meet.mu.Lock()
	o, ok := r.meet.offers[offerID]
	r.meet.mu.Unlock()
	if !ok {
		return id.TerminalID{}, errors.New("node: no such radio invitation")
	}
	tid, err := r.JoinInvite(o.invite)
	if err != nil {
		return id.TerminalID{}, err
	}
	r.meet.mu.Lock()
	delete(r.meet.offers, offerID)
	r.meet.mu.Unlock()
	return tid, nil
}

// onRadioControl folds one control message in. It is called from the radio's
// read loop, so it does the least possible and never blocks on the radio.
func (r *Runtime) onRadioControl(src radiotransfer.RadioAddress, msg []byte) {
	r.radioMeetOnce()
	// SIGNED kinds are dispatched first and never fall through to the flat
	// parser below. Peeking the kind is safe because nothing is acted upon
	// until a signature has been checked against the device the body names.
	if kind, _, ok := peekSignedRadio(msg); ok {
		switch kind {
		case radioMsgCard:
			r.onRadioCard(src, msg)
			return
		case radioMsgProbe:
			r.onRadioProbe(src, msg)
			return
		case radioMsgAck:
			r.onRadioAck(src, msg)
			return
		case radioMsgLineOffer:
			r.onRadioLineOffer(src, msg)
			return
		case radioMsgAccept:
			r.onRadioAccept(src, msg)
			return
		case radioMsgGrant:
			r.onRadioGrant(src, msg)
			return
		case radioMsgCommit:
			r.onRadioCommit(src, msg)
			return
		}
	}
	d := codec.NewDecoder(msg)
	m, err := d.ReadMapHeader()
	if err != nil {
		return
	}
	var (
		version, kind uint64
		dev           []byte
		title         string
		from          string
		invite        []byte
		space         []byte
	)
	for {
		k, ok, er := m.Next()
		if er != nil || !ok {
			break
		}
		switch k {
		case rkVersion:
			version, er = d.ReadUint()
		case rkKind:
			kind, er = d.ReadUint()
		case rkDevice:
			dev, er = d.ReadBytes()
		case rkX25519, rkName:
			// Card-only fields. A card is a SIGNED message handled above, so
			// reaching them here means somebody sent an unsigned one.
			er = d.SkipItem()
		case rkTitle:
			var b []byte
			b, er = d.ReadBytes()
			title = clip(string(b), maxRadioTitle)
		case rkFrom:
			var b []byte
			b, er = d.ReadBytes()
			from = clip(string(b), maxRadioName)
		case rkInvite:
			invite, er = d.ReadBytes()
		case rkSpace:
			space, er = d.ReadBytes()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return
		}
	}
	if version != radioControlVersion || len(dev) != len(id.DeviceID{}) {
		return
	}
	var device id.DeviceID
	copy(device[:], dev)

	switch kind {
	case radioMsgCard:
		// Unreachable: a card is a SIGNED message and onRadioControl
		// dispatches it above. An unsigned card is not a card.
		return
	case radioMsgOffer:
		// An offer for somebody else. Everyone in range hears it; only the
		// addressee can open it, and only the addressee keeps it.
		if device != r.Device.ID || len(invite) == 0 ||
			len(invite) > maxRadioInvite || len(space) != len(id.TerminalID{}) {
			return
		}
		var tid id.TerminalID
		copy(tid[:], space)
		r.mu.Lock()
		_, already := r.spaces[tid]
		r.mu.Unlock()
		if already {
			return // already in it; an offer to rejoin is not news
		}
		r.meet.mu.Lock()
		defer r.meet.mu.Unlock()
		if len(r.meet.offers) >= maxRadioOffers {
			return
		}
		key := tid.String()[:16]
		r.meet.offers[key] = &RadioOffer{
			ID: key, Space: tid, Title: title, From: from, Heard: time.Now(),
			// Back into the form JoinInvite reads. The saving is on the AIR,
			// which is the only place it mattered.
			invite: base64.StdEncoding.EncodeToString(invite),
		}
	}
}

// radioControl returns the radio endpoint, or says why there is none.
func (r *Runtime) radioControl() (radioControlEndpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.links {
		if ep, ok := l.c.(radioControlEndpoint); ok {
			return ep, nil
		}
	}
	return nil, errors.New("node: no radio is attached with the transfer layer. " +
		"Start one with --mesh and --mesh-seed; meeting over the air needs the " +
		"segment key that seed derives")
}

// radioControlEndpoint is the part of the radio link this file uses.
// radioControlEndpoint is the one-method-plus-one seam this file needs from a
// radio. SendControlWithin is separate rather than a parameter on SendControl
// because most control traffic wants the session's own patience and only the
// peer link's questions want their own.
type radioControlEndpoint interface {
	SendControl([]byte) error
	SendControlWithin([]byte, time.Duration) error
	SendControlTagged(string, []byte, time.Duration) error
}

func (r *Runtime) radioMeetOnce() {
	r.mu.Lock()
	if r.meet == nil {
		r.meet = newRadioMeet()
	}
	r.mu.Unlock()
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func dropOldest(m map[id.DeviceID]*RadioNeighbour) {
	var oldest id.DeviceID
	var at time.Time
	for k, v := range m {
		if at.IsZero() || v.Heard.Before(at) {
			oldest, at = k, v.Heard
		}
	}
	delete(m, oldest)
}

func sortByHeard(out []RadioNeighbour) {
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Heard.After(out[j-1].Heard); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
}

// StartLineOverRadio opens a NEW space for the two of you and offers it.
//
// This is the shape a radio meeting actually has, and the shape the rest of
// this project already settled on. QL-1's rule is that a link never picks a
// space implicitly — it opens a new place — and QL-3's is that a DM is a
// presentation mode rather than a data type: a space with one other person in
// it renders as that person's name. Two radios that just found each other are
// exactly that case.
//
// Inviting into whatever space happened to be OPEN was the wrong default. It
// made the result depend on where somebody's cursor was, and it fell over
// entirely when the open space was public — a public space has no epoch to
// seal an invitation to, which is a true sentence about the wrong question.
func (r *Runtime) StartLineOverRadio(dev id.DeviceID) (id.TerminalID, error) {
	// Checked BEFORE a space is created, so a segment with no radio does not
	// leave an empty room behind as the price of finding out.
	if _, err := r.radioControl(); err != nil {
		return id.TerminalID{}, err
	}
	r.radioMeetOnce()
	r.meet.mu.Lock()
	_, known := r.meet.neighbour[dev]
	r.meet.mu.Unlock()
	if !known {
		return id.TerminalID{}, fmt.Errorf("node: no radio neighbour %s has been "+
			"heard — they have to announce themselves before an invitation can be "+
			"sealed to them", dev.String()[:8])
	}
	// A SECOND press re-offers the SAME line rather than minting another.
	//
	// On this carrier an offer takes tens of seconds and there is nothing to
	// see while it travels, so a person presses again — and the first version
	// answered that by creating a second empty room, then a third. Observed:
	// six spaces, five of them empty, none of them wanted. Pressing again
	// means "I do not think they got it", and the honest answer to that is to
	// send it again.
	//
	// It reads from the DURABLE journal now. The memory-only map this replaces
	// forgot everything on restart, so the very first press after one opened
	// yet another room — the same bug, merely rarer and therefore harder to
	// believe.
	if rec, existing := r.liveTargetedInvitation(dev); existing {
		if tid, err := id.ParseTerminalID(rec.Space); err == nil {
			r.mu.Lock()
			_, alive := r.spaces[tid]
			r.mu.Unlock()
			if alive {
				return tid, r.InviteOverRadio(tid, dev)
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
	if err := r.InviteOverRadio(tid, dev); err != nil {
		return id.TerminalID{}, err
	}
	// TARGETED: it names one device and has no mailbox anywhere, so it mints
	// no pass. A pass exists to solve the problem of an unknown future
	// redeemer, and pressing somebody's name means that problem does not
	// exist here.
	if err := r.recordInvitation(InvitationRecord{
		ID: newInvitationID(), Mode: InvitationTargeted, Space: tid.Hex(),
		Target: hex.EncodeToString(dev[:]), IssuedAt: time.Now().Unix(),
		State: InvitationOffered,
	}); err != nil {
		// The offer is on the air whether or not we wrote it down, so say so
		// rather than pretending the press did nothing.
		return tid, fmt.Errorf("node: the invitation went out but could not be "+
			"recorded locally, so pressing again will open a second room: %w", err)
	}
	return tid, nil
}
