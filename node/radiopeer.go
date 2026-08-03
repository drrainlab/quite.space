// The Radio Peer Link: the layer between hearing somebody and handing them a
// space.
//
// Four things, and none of them pretends to be another:
//
//	Radio beacon      discovers a specific device
//	Radio Peer Link   a temporary secured ADDRESSED channel between devices
//	Invitation        creates the space and the membership
//	Quiet sync        carries the events of a space that already exists
//
// A peer link is NOT a space. It grants no membership, holds no epoch key, and
// does not outlive the radio. It says exactly one thing: THIS PEER IS
// REACHABLE BY AN ADDRESSED RADIO PATH RIGHT NOW.
//
// Why it earns its place, in airtime. An invitation is six frames — thirteen
// seconds on the air, and up to five minutes of not knowing if nobody is
// listening. Establishing a link first is two one-frame messages, about a
// second, so the expensive thing is never committed blind. In a live exchange
// the unit of risk is not bytes but LEGS: each one is an independent chance
// for the other person to have walked away.
//
// WHAT IT DOES NOT CLAIM. An addressed Meshtastic packet may still travel
// through intermediate nodes, so a link says WHO, never how far. "Radio link
// established" and "Direct radio link" are different sentences and only the
// second requires observed zero hops.
package node

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/curve25519"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
)

// Peer-link message kinds, continuing node/radioinvite.go's set.
const (
	radioMsgProbe = 3
	radioMsgAck   = 4
)

// Wire keys for a SIGNED radio message. The signature covers the body bytes
// EXACTLY as they travel — never a re-encoding of decoded fields, because a
// re-encoder silently drops keys it does not know and a card that gains a
// field would then fail verification on every older build.
const (
	rsKind    = 1
	rsBody    = 2
	rsSig     = 3
	rsVersion = 9
)

// Signing domains. Distinct strings so a card can never be replayed as a
// probe, nor a probe as an acknowledgement.
const (
	domainCard  = "qs.radio.card.v1"
	domainProbe = "qs.radio.probe.v1"
	domainAck   = "qs.radio.ack.v1"
)

// Bounds and lifetimes. All memory-only: a peer link is a fact about right
// now, and a durable one would be a claim about a radio that may be off.
const (
	// peerLinkTTL is how long a link stands without being refreshed. Short on
	// purpose: the honest lifetime of "they can hear me" is minutes.
	peerLinkTTL = 5 * time.Minute
	// cardTTL bounds how long a beacon may be replayed as current.
	cardTTL = 10 * time.Minute
	// probeDeadline is the peer link's whole reason for existing: a one-frame
	// question must not inherit the sync give-up budget, which is 270 seconds
	// with the shipped limits. A frame plus its COMMIT is a few seconds on
	// LONG_FAST, so this is generous against the medium and brutal against a
	// radio nobody is listening to.
	probeDeadline = 20 * time.Second
	maxPeerLinks  = 32
	nonceLen      = 16
	fingerprintLn = 8
)

// PeerLinkState is what we can honestly say about a link.
type PeerLinkState string

const (
	// PeerLinkProbing means a question is on the air and nothing has answered.
	PeerLinkProbing PeerLinkState = "probing"
	// PeerLinkUp means this peer answered and both sides derived the same key.
	PeerLinkUp PeerLinkState = "up"
	// PeerLinkGone means the link stood and then stopped answering.
	PeerLinkGone PeerLinkState = "gone"
	// PeerLinkNoAnswer means nobody ever answered the probe.
	//
	// Its own state, and not a shade of "gone": "they answered and then left"
	// and "nobody was there" are different sentences to a person standing in a
	// field, and this codebase's rule (node/pass.go's JoinUnknown versus
	// JoinDeclined) is that they never collapse into one.
	PeerLinkNoAnswer PeerLinkState = "no_answer"
)

// RadioPeerLink is a temporary addressed channel to one device.
type RadioPeerLink struct {
	Peer      id.DeviceID
	Address   radiotransfer.RadioAddress
	SessionID [16]byte
	State     PeerLinkState
	LastSeen  time.Time
	ExpiresAt time.Time
	// HopEstimate is 0 only when zero hops were OBSERVED, and unknown
	// otherwise. It is never inferred from an addressed packet arriving,
	// because an addressed packet is exactly what a mesh forwards.
	HopEstimate uint8
	HopKnown    bool

	key   [32]byte
	eph   [32]byte // our ephemeral private scalar for this attempt
	nonce [nonceLen]byte
}

// Direct reports whether zero hops were observed. Anything else — including
// an addressed packet that plainly arrived — is not evidence of directness.
func (l *RadioPeerLink) Direct() bool { return l.HopKnown && l.HopEstimate == 0 }

// segmentFingerprint identifies the segment a card belongs to, without
// disclosing the seed. Two radios on different segments derive different
// fingerprints, so a card from a neighbouring segment is visibly not ours.
func segmentFingerprint(seed []byte) [fingerprintLn]byte {
	sum := sha256.Sum256(append([]byte("qs.radio.segment.v1"), seed...))
	var out [fingerprintLn]byte
	copy(out[:], sum[:fingerprintLn])
	return out
}

// signedRadio wraps a body in the signed envelope.
//
// The signature covers the DOMAIN and the body bytes, so a message of one kind
// can never be replayed as another, and verification never re-encodes.
func signedRadio(kind uint64, domain string, body []byte, priv ed25519.PrivateKey) []byte {
	sig := ed25519.Sign(priv, append([]byte(domain), body...))
	b := codec.AppendMap(nil, 4)
	b = codec.AppendUint(b, rsKind)
	b = codec.AppendUint(b, kind)
	b = codec.AppendUint(b, rsBody)
	b = codec.AppendBytes(b, body)
	b = codec.AppendUint(b, rsSig)
	b = codec.AppendBytes(b, sig)
	b = codec.AppendUint(b, rsVersion)
	b = codec.AppendUint(b, radioControlVersion)
	return b
}

// openSignedRadio unwraps and VERIFIES, given the claimed signer's device id.
//
// The device id IS the ed25519 public key in this project, so checking that a
// card really came from the device it names costs one verification and no key
// distribution at all. That matters more than it looks: without it an impostor
// announces somebody else's device id alongside their OWN ephemeral key, and
// the person who then invites "Bob" seals it to the impostor. The existing
// comment in radioinvite.go argues a self-asserted key is safe because the
// impostor receives an invite they cannot open — true for Bob, and false for
// the person doing the inviting.
func openSignedRadio(msg []byte, domain string, signer id.DeviceID) (kind uint64, body []byte, err error) {
	d := codec.NewDecoder(msg)
	m, err := d.ReadMapHeader()
	if err != nil {
		return 0, nil, err
	}
	var sig []byte
	var version uint64
	for range m.Len() {
		k, err := d.ReadUint()
		if err != nil {
			return 0, nil, err
		}
		switch k {
		case rsKind:
			kind, err = d.ReadUint()
		case rsBody:
			body, err = d.ReadBytes()
		case rsSig:
			sig, err = d.ReadBytes()
		case rsVersion:
			version, err = d.ReadUint()
		default:
			err = d.SkipItem()
		}
		if err != nil {
			return 0, nil, err
		}
	}
	if version != radioControlVersion {
		return 0, nil, fmt.Errorf("node: radio message version %d is not %d",
			version, radioControlVersion)
	}
	if len(body) == 0 || len(sig) != ed25519.SignatureSize {
		return 0, nil, errors.New("node: a signed radio message with no signature")
	}
	if !ed25519.Verify(ed25519.PublicKey(signer[:]), append([]byte(domain), body...), sig) {
		return 0, nil, errors.New("node: the signature does not match the device " +
			"that claims to have sent it")
	}
	return kind, body, nil
}

// peekSignedRadio reads the KIND and the claimed signer out of a message
// without trusting either, so the dispatcher can find the right verifier.
// Nothing read here may be acted upon before openSignedRadio has passed.
func peekSignedRadio(msg []byte) (kind uint64, body []byte, ok bool) {
	d := codec.NewDecoder(msg)
	m, err := d.ReadMapHeader()
	if err != nil {
		return 0, nil, false
	}
	for range m.Len() {
		k, err := d.ReadUint()
		if err != nil {
			return 0, nil, false
		}
		switch k {
		case rsKind:
			kind, err = d.ReadUint()
		case rsBody:
			body, err = d.ReadBytes()
		default:
			err = d.SkipItem()
		}
		if err != nil {
			return 0, nil, false
		}
	}
	return kind, body, body != nil
}

// linkKey binds the derived key to the WHOLE exchange, not just the shared
// secret. Both device ids, both nonces and the segment fingerprint go into the
// derivation, so a key can only be produced by two parties who saw the same
// conversation — and a replayed half is useless.
//
// The device ids are ordered so both sides derive identically without either
// having to know who "went first".
func linkKey(shared []byte, a, b id.DeviceID, nonceA, nonceB [nonceLen]byte,
	fp [fingerprintLn]byte) ([32]byte, error) {

	lo, hi := a, b
	nLo, nHi := nonceA, nonceB
	for i := range a {
		if a[i] != b[i] {
			if a[i] > b[i] {
				lo, hi, nLo, nHi = b, a, nonceB, nonceA
			}
			break
		}
	}
	info := make([]byte, 0, 64+2*nonceLen+fingerprintLn)
	info = append(info, lo[:]...)
	info = append(info, hi[:]...)
	info = append(info, nLo[:]...)
	info = append(info, nHi[:]...)
	info = append(info, fp[:]...)

	out, err := hkdf.Key(sha256.New, shared, nil, string(info), 32)
	if err != nil {
		return [32]byte{}, err
	}
	var k [32]byte
	copy(k[:], out)
	return k, nil
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// Wire keys for a card body. Append-only.
const (
	cbDevice     = 1
	cbStaticX    = 2
	cbName       = 3
	cbEphemeral  = 4
	cbFingerpr   = 5
	cbGeneration = 6
	cbExpires    = 7
)

// Wire keys for a probe or an acknowledgement body. Append-only.
const (
	pbFrom       = 1
	pbTo         = 2
	pbEphemeral  = 3
	pbNonce      = 4
	pbPeerNonce  = 5
	pbGeneration = 6
)

// cardBody is the signed half of a beacon.
type cardBody struct {
	Device     id.DeviceID
	StaticX    [32]byte
	Name       string
	Ephemeral  [32]byte
	Fingerpr   [fingerprintLn]byte
	Generation uint64
	Expires    uint64
}

func encodeCardBody(c cardBody) []byte {
	b := codec.AppendMap(nil, 7)
	b = codec.AppendUint(b, cbDevice)
	b = codec.AppendBytes(b, c.Device[:])
	b = codec.AppendUint(b, cbStaticX)
	b = codec.AppendBytes(b, c.StaticX[:])
	b = codec.AppendUint(b, cbName)
	b = codec.AppendText(b, c.Name)
	b = codec.AppendUint(b, cbEphemeral)
	b = codec.AppendBytes(b, c.Ephemeral[:])
	b = codec.AppendUint(b, cbFingerpr)
	b = codec.AppendBytes(b, c.Fingerpr[:])
	b = codec.AppendUint(b, cbGeneration)
	b = codec.AppendUint(b, c.Generation)
	b = codec.AppendUint(b, cbExpires)
	b = codec.AppendUint(b, c.Expires)
	return b
}

func decodeCardBody(body []byte) (cardBody, error) {
	var c cardBody
	d := codec.NewDecoder(body)
	m, err := d.ReadMapHeader()
	if err != nil {
		return c, err
	}
	for range m.Len() {
		k, err := d.ReadUint()
		if err != nil {
			return c, err
		}
		switch k {
		case cbDevice:
			err = readFixed(d, c.Device[:])
		case cbStaticX:
			err = readFixed(d, c.StaticX[:])
		case cbName:
			var s string
			if s, err = d.ReadText(); err == nil {
				if len(s) > maxRadioName {
					s = s[:maxRadioName]
				}
				c.Name = s
			}
		case cbEphemeral:
			err = readFixed(d, c.Ephemeral[:])
		case cbFingerpr:
			err = readFixed(d, c.Fingerpr[:])
		case cbGeneration:
			c.Generation, err = d.ReadUint()
		case cbExpires:
			c.Expires, err = d.ReadUint()
		default:
			err = d.SkipItem()
		}
		if err != nil {
			return c, err
		}
	}
	return c, nil
}

// probeBody is the signed half of a PROBE or an ACK.
type probeBody struct {
	From       id.DeviceID
	To         id.DeviceID
	Ephemeral  [32]byte
	Nonce      [nonceLen]byte
	PeerNonce  [nonceLen]byte
	Generation uint64
}

func encodeProbeBody(p probeBody, withPeerNonce bool) []byte {
	n := 5
	if withPeerNonce {
		n = 6
	}
	b := codec.AppendMap(nil, n)
	b = codec.AppendUint(b, pbFrom)
	b = codec.AppendBytes(b, p.From[:])
	b = codec.AppendUint(b, pbTo)
	b = codec.AppendBytes(b, p.To[:])
	b = codec.AppendUint(b, pbEphemeral)
	b = codec.AppendBytes(b, p.Ephemeral[:])
	b = codec.AppendUint(b, pbNonce)
	b = codec.AppendBytes(b, p.Nonce[:])
	if withPeerNonce {
		b = codec.AppendUint(b, pbPeerNonce)
		b = codec.AppendBytes(b, p.PeerNonce[:])
	}
	b = codec.AppendUint(b, pbGeneration)
	b = codec.AppendUint(b, p.Generation)
	return b
}

func decodeProbeBody(body []byte) (probeBody, error) {
	var p probeBody
	d := codec.NewDecoder(body)
	m, err := d.ReadMapHeader()
	if err != nil {
		return p, err
	}
	for range m.Len() {
		k, err := d.ReadUint()
		if err != nil {
			return p, err
		}
		switch k {
		case pbFrom:
			err = readFixed(d, p.From[:])
		case pbTo:
			err = readFixed(d, p.To[:])
		case pbEphemeral:
			err = readFixed(d, p.Ephemeral[:])
		case pbNonce:
			err = readFixed(d, p.Nonce[:])
		case pbPeerNonce:
			err = readFixed(d, p.PeerNonce[:])
		case pbGeneration:
			p.Generation, err = d.ReadUint()
		default:
			err = d.SkipItem()
		}
		if err != nil {
			return p, err
		}
	}
	return p, nil
}

// readFixed insists on the exact width. A short key that decoded into a
// zero-padded array would derive a key nobody else derives, and the failure
// would look like being out of range.
func readFixed(d *codec.Decoder, into []byte) error {
	b, err := d.ReadBytes()
	if err != nil {
		return err
	}
	if len(b) != len(into) {
		return fmt.Errorf("node: expected %d bytes, got %d", len(into), len(b))
	}
	copy(into, b)
	return nil
}

// ---- runtime ----

// radioPeerOnce prepares the peer-link tables and this run's generation.
func (r *Runtime) radioPeerOnce() {
	r.radioMeetOnce()
	r.meet.mu.Lock()
	defer r.meet.mu.Unlock()
	if r.meet.peers == nil {
		r.meet.peers = map[id.DeviceID]*RadioPeerLink{}
	}
	if r.meet.generation == 0 {
		// A fresh number every run, so a card this process minted before a
		// restart cannot be replayed as current. Random rather than a counter:
		// nothing here is persisted, so a counter would restart at the same
		// value and prove nothing.
		b, err := randomBytes(8)
		if err != nil {
			r.meet.generation = 1
			return
		}
		var g uint64
		for _, x := range b {
			g = g<<8 | uint64(x)
		}
		if g == 0 {
			g = 1
		}
		r.meet.generation = g
	}
}

// segmentFP is this node's segment fingerprint, or false when no radio segment
// seed is configured.
func (r *Runtime) segmentFP() ([fingerprintLn]byte, bool) {
	r.mu.Lock()
	seed := r.meshSeed
	r.mu.Unlock()
	if len(seed) == 0 {
		return [fingerprintLn]byte{}, false
	}
	return segmentFingerprint(seed), true
}

// ephemeralForAnnounce mints the ephemeral key this card advertises and keeps
// its private half, so a probe answering this card can complete the agreement.
//
// One per announcement: a fresh key each time is what makes the derived link
// key specific to this conversation rather than to this device forever.
func (r *Runtime) ephemeralForAnnounce() ([32]byte, error) {
	var pub [32]byte
	priv, err := randomBytes(32)
	if err != nil {
		return pub, err
	}
	p, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return pub, err
	}
	copy(pub[:], p)
	r.meet.mu.Lock()
	copy(r.meet.myEph[:], priv)
	r.meet.myEphPub = pub
	r.meet.mu.Unlock()
	return pub, nil
}

// AnnounceOnRadio broadcasts this node's signed card.
//
// An explicit act, never automatic: it tells everyone within radio range that
// this device exists and what it is called, which is reasonable when standing
// in a field with somebody and not a reasonable default for a node in a flat.
func (r *Runtime) AnnounceOnRadio() error {
	ep, err := r.radioControl()
	if err != nil {
		return err
	}
	fp, ok := r.segmentFP()
	if !ok {
		return errors.New("node: a card needs a radio segment — start one with " +
			"--mesh and --mesh-seed")
	}
	r.radioPeerOnce()

	name := r.DisplayName()
	if len(name) > maxRadioName {
		name = name[:maxRadioName]
	}
	eph, err := r.ephemeralForAnnounce()
	if err != nil {
		return err
	}
	r.meet.mu.Lock()
	gen := r.meet.generation
	r.meet.mu.Unlock()

	body := encodeCardBody(cardBody{
		Device: r.Device.ID, StaticX: r.Device.X25519Pub, Name: name,
		Ephemeral: eph, Fingerpr: fp, Generation: gen,
		Expires: uint64(time.Now().Add(cardTTL).Unix()),
	})
	return ep.SendControl(signedRadio(radioMsgCard, domainCard, body,
		r.Device.SignKey()))
}

// onRadioCard folds a VERIFIED card into the neighbour table.
func (r *Runtime) onRadioCard(src radiotransfer.RadioAddress, msg []byte) bool {
	_, body, ok := peekSignedRadio(msg)
	if !ok {
		return false
	}
	claimed, err := decodeCardBody(body)
	if err != nil {
		return false
	}
	// Verify against the device the card NAMES. The device id is the ed25519
	// public key, so this proves the card was written by whoever holds that
	// device's key — which is what stops an impostor announcing somebody
	// else's id alongside their own ephemeral key and collecting the
	// invitation meant for them.
	if _, _, err := openSignedRadio(msg, domainCard, claimed.Device); err != nil {
		return false
	}
	if claimed.Device == r.Device.ID {
		return true // our own card, echoed back
	}
	fp, hasSeg := r.segmentFP()
	if !hasSeg || fp != claimed.Fingerpr {
		return true // a card from a different segment: heard, not ours
	}
	now := time.Now()
	if claimed.Expires != 0 && now.After(time.Unix(int64(claimed.Expires), 0)) {
		return true // an expired card is not current, however well signed
	}

	r.radioPeerOnce()
	r.meet.mu.Lock()
	defer r.meet.mu.Unlock()
	n := r.meet.neighbour[claimed.Device]
	if n == nil {
		if len(r.meet.neighbour) >= maxRadioHeard {
			dropOldest(r.meet.neighbour)
		}
		n = &RadioNeighbour{Device: claimed.Device}
		r.meet.neighbour[claimed.Device] = n
	}
	// A NEW generation means the peer restarted, so any link we hold to it is
	// about a process that no longer exists.
	if n.generation != 0 && n.generation != claimed.Generation {
		delete(r.meet.peers, claimed.Device)
	}
	n.Name, n.Heard, n.x25519 = claimed.Name, now, claimed.StaticX
	n.eph, n.generation = claimed.Ephemeral, claimed.Generation
	n.expires = time.Unix(int64(claimed.Expires), 0)
	if len(src) > 0 {
		n.addr = append(radiotransfer.RadioAddress(nil), src...)
	}
	return true
}

// LinkToPeer establishes an addressed link to a heard neighbour.
//
// TWO ONE-FRAME MESSAGES, and that is the whole point. Committing a six-frame
// invitation to a radio nobody is listening to costs thirteen seconds of air
// and up to five minutes of not knowing; asking first costs about a second and
// answers in seconds. The expensive thing is never sent blind again.
//
// It creates no space and grants no membership. What it produces is permission
// to stop guessing.
func (r *Runtime) LinkToPeer(dev id.DeviceID) error {
	ep, err := r.radioControl()
	if err != nil {
		return err
	}
	r.radioPeerOnce()

	r.meet.mu.Lock()
	n := r.meet.neighbour[dev]
	gen := r.meet.generation
	r.meet.mu.Unlock()
	if n == nil {
		return fmt.Errorf("node: no radio neighbour %s has been heard — a link "+
			"is made to a device that announced itself, not to a name",
			dev.String()[:8])
	}
	if n.eph == ([32]byte{}) {
		return fmt.Errorf("node: %s announced with an older card that carries no "+
			"ephemeral key; ask them to say who they are again", dev.String()[:8])
	}

	priv, err := randomBytes(32)
	if err != nil {
		return err
	}
	pubB, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return err
	}
	nonceB, err := randomBytes(nonceLen)
	if err != nil {
		return err
	}
	var pub [32]byte
	var nonce [nonceLen]byte
	copy(pub[:], pubB)
	copy(nonce[:], nonceB)

	// A PROBING link expires on the PROBE's clock, not the link's.
	//
	// A question nobody answered must resolve in seconds — that is the entire
	// reason for asking before committing an invitation. Giving an unanswered
	// probe the five-minute lifetime of an established link would reinstate
	// exactly the wait it exists to remove, and the screen would sit on
	// "asking…" long after the answer was in.
	link := &RadioPeerLink{
		Peer: dev, Address: n.addr, State: PeerLinkProbing,
		LastSeen:  time.Now(),
		ExpiresAt: time.Now().Add(probeDeadline + 5*time.Second),
		nonce:     nonce,
	}
	copy(link.eph[:], priv)

	r.meet.mu.Lock()
	if len(r.meet.peers) >= maxPeerLinks {
		dropOldestLink(r.meet.peers)
	}
	r.meet.peers[dev] = link
	r.meet.mu.Unlock()

	body := encodeProbeBody(probeBody{
		From: r.Device.ID, To: dev, Ephemeral: pub, Nonce: nonce,
		Generation: gen,
	}, false)
	// Its OWN deadline. Inheriting the sync give-up budget would make the
	// cheap question cost 270 seconds, which is the expense it exists to
	// avoid.
	return ep.SendControlWithin(
		signedRadio(radioMsgProbe, domainProbe, body, r.Device.SignKey()),
		probeDeadline)
}

// onRadioProbe answers a probe addressed to this device.
func (r *Runtime) onRadioProbe(src radiotransfer.RadioAddress, msg []byte) {
	_, raw, ok := peekSignedRadio(msg)
	if !ok {
		return
	}
	claimed, err := decodeProbeBody(raw)
	if err != nil || claimed.To != r.Device.ID || claimed.From == r.Device.ID {
		return
	}
	if _, _, err := openSignedRadio(msg, domainProbe, claimed.From); err != nil {
		return
	}
	fp, hasSeg := r.segmentFP()
	if !hasSeg {
		return
	}
	ep, err := r.radioControl()
	if err != nil {
		return
	}
	r.radioPeerOnce()

	// Answer with OUR announced ephemeral, whose private half we kept, plus a
	// fresh nonce of our own — the transcript needs both.
	r.meet.mu.Lock()
	myPriv, myPub, gen := r.meet.myEph, r.meet.myEphPub, r.meet.generation
	r.meet.mu.Unlock()
	if myPub == ([32]byte{}) {
		return // we never announced, so there is nothing they could have probed
	}
	nonceB, err := randomBytes(nonceLen)
	if err != nil {
		return
	}
	var nonce [nonceLen]byte
	copy(nonce[:], nonceB)

	shared, err := curve25519.X25519(myPriv[:], claimed.Ephemeral[:])
	if err != nil {
		return
	}
	key, err := linkKey(shared, r.Device.ID, claimed.From, nonce, claimed.Nonce, fp)
	if err != nil {
		return
	}

	now := time.Now()
	link := &RadioPeerLink{
		Peer: claimed.From, Address: append(radiotransfer.RadioAddress(nil), src...),
		State: PeerLinkUp, LastSeen: now, ExpiresAt: now.Add(peerLinkTTL),
		key: key, nonce: nonce,
	}
	copy(link.SessionID[:], key[:16])

	r.meet.mu.Lock()
	if len(r.meet.peers) >= maxPeerLinks {
		dropOldestLink(r.meet.peers)
	}
	r.meet.peers[claimed.From] = link
	r.meet.mu.Unlock()

	body := encodeProbeBody(probeBody{
		From: r.Device.ID, To: claimed.From, Ephemeral: myPub, Nonce: nonce,
		PeerNonce: claimed.Nonce, Generation: gen,
	}, true)
	_ = ep.SendControlWithin(
		signedRadio(radioMsgAck, domainAck, body, r.Device.SignKey()), probeDeadline)
}

// onRadioAck completes the exchange the probe began.
func (r *Runtime) onRadioAck(src radiotransfer.RadioAddress, msg []byte) {
	_, raw, ok := peekSignedRadio(msg)
	if !ok {
		return
	}
	claimed, err := decodeProbeBody(raw)
	if err != nil || claimed.To != r.Device.ID || claimed.From == r.Device.ID {
		return
	}
	if _, _, err := openSignedRadio(msg, domainAck, claimed.From); err != nil {
		return
	}
	fp, hasSeg := r.segmentFP()
	if !hasSeg {
		return
	}
	r.radioPeerOnce()

	r.meet.mu.Lock()
	defer r.meet.mu.Unlock()
	link := r.meet.peers[claimed.From]
	if link == nil || link.State != PeerLinkProbing {
		return // nothing was asked, so this answers nothing
	}
	// The acknowledgement must echo OUR nonce. Without that check an
	// eavesdropper could answer a probe they merely overheard.
	if claimed.PeerNonce != link.nonce {
		return
	}
	shared, err := curve25519.X25519(link.eph[:], claimed.Ephemeral[:])
	if err != nil {
		return
	}
	key, err := linkKey(shared, r.Device.ID, claimed.From, link.nonce, claimed.Nonce, fp)
	if err != nil {
		return
	}
	now := time.Now()
	link.key, link.State = key, PeerLinkUp
	link.LastSeen, link.ExpiresAt = now, now.Add(peerLinkTTL)
	copy(link.SessionID[:], key[:16])
	if len(src) > 0 {
		link.Address = append(radiotransfer.RadioAddress(nil), src...)
	}
}

// PeerLink reports the live link to a device, if there is one.
func (r *Runtime) PeerLink(dev id.DeviceID) (RadioPeerLink, bool) {
	r.radioPeerOnce()
	r.meet.mu.Lock()
	defer r.meet.mu.Unlock()
	l := r.meet.peers[dev]
	if l == nil {
		return RadioPeerLink{}, false
	}
	if time.Now().After(l.ExpiresAt) {
		// Expired links resolve rather than vanishing, and WHICH way they
		// resolve is the whole point: a probe that was never answered becomes
		// no_answer, a link that stood and then went quiet becomes gone. Both
		// are true sentences and they are not the same sentence.
		if l.State == PeerLinkProbing {
			l.State = PeerLinkNoAnswer
		} else if l.State == PeerLinkUp {
			l.State = PeerLinkGone
		}
	}
	return *l, true
}

// dropOldestLink evicts by last contact, so a segment full of strangers cannot
// crowd out the peer somebody is actually talking to.
func dropOldestLink(m map[id.DeviceID]*RadioPeerLink) {
	var oldest id.DeviceID
	var at time.Time
	first := true
	for k, v := range m {
		if first || v.LastSeen.Before(at) {
			oldest, at, first = k, v.LastSeen, false
		}
	}
	if !first {
		delete(m, oldest)
	}
}
