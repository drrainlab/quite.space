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
	}{hex.EncodeToString(n.Device[:]), n.Name, n.Heard})
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
}

func newRadioMeet() *radioMeet {
	return &radioMeet{
		neighbour: map[id.DeviceID]*RadioNeighbour{},
		offers:    map[string]*RadioOffer{},
	}
}

// AnnounceOnRadio broadcasts this node's card to the segment.
//
// An explicit act, never automatic. Announcing tells everyone within radio
// range that this device exists and what it is called; that is a reasonable
// thing to do when standing in a field with somebody, and not a reasonable
// default for a node sitting in a flat.
func (r *Runtime) AnnounceOnRadio() error {
	ep, err := r.radioControl()
	if err != nil {
		return err
	}
	name := r.DisplayName()
	if len(name) > maxRadioName {
		name = name[:maxRadioName]
	}
	// Keys STRICTLY ASCENDING. The decoder enforces it, so a map written in
	// the order the fields were thought of decodes as nothing at all — which
	// is silence on a radio, and silence on a radio looks like being out of
	// range. That is the exact confusion this wave exists to end, so the
	// version goes last because its key number is highest, not first because
	// it feels like a header.
	b := codec.AppendMap(nil, 5)
	b = codec.AppendUint(b, rkKind)
	b = codec.AppendUint(b, radioMsgCard)
	b = codec.AppendUint(b, rkDevice)
	b = codec.AppendBytes(b, r.Device.ID[:])
	b = codec.AppendUint(b, rkX25519)
	b = codec.AppendBytes(b, r.Device.X25519Pub[:])
	b = codec.AppendUint(b, rkName)
	b = codec.AppendBytes(b, []byte(name))
	b = codec.AppendUint(b, rkVersion)
	b = codec.AppendUint(b, radioControlVersion)
	return ep.SendControl(b)
}

// RadioNeighbours lists who has been heard, most recent first.
func (r *Runtime) RadioNeighbours() []RadioNeighbour {
	r.radioMeetOnce()
	r.meet.mu.Lock()
	defer r.meet.mu.Unlock()
	out := make([]RadioNeighbour, 0, len(r.meet.neighbour))
	for _, n := range r.meet.neighbour {
		out = append(out, *n)
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
func (r *Runtime) onRadioControl(msg []byte) {
	r.radioMeetOnce()
	d := codec.NewDecoder(msg)
	m, err := d.ReadMapHeader()
	if err != nil {
		return
	}
	var (
		version, kind uint64
		dev           []byte
		x25519        []byte
		name, title   string
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
		case rkX25519:
			x25519, er = d.ReadBytes()
		case rkName:
			var b []byte
			b, er = d.ReadBytes()
			name = clip(string(b), maxRadioName)
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
		if len(x25519) != 32 || device == r.Device.ID {
			return
		}
		r.meet.mu.Lock()
		defer r.meet.mu.Unlock()
		// Bounded, and oldest-first: a segment somebody is standing in has a
		// handful of radios on it. A cap is what stops a neighbour from
		// filling this by announcing under a thousand device ids.
		if _, known := r.meet.neighbour[device]; !known &&
			len(r.meet.neighbour) >= maxRadioHeard {
			dropOldest(r.meet.neighbour)
		}
		n := &RadioNeighbour{Device: device, Name: name, Heard: time.Now()}
		copy(n.x25519[:], x25519)
		r.meet.neighbour[device] = n
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
type radioControlEndpoint interface{ SendControl([]byte) error }

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
