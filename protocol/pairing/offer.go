// Pairing offers (MD-1): the capability that lets a second device ASK to
// become you.
//
// The offer is a BEARER CAPABILITY WITH A SIXTY-SECOND LIFE: a 32-byte
// secret, the parent's LAN address, an expiry. Whoever holds it may ATTEMPT
// to pair — and nothing more. It is broadcast as sound in a room (or shown
// as a QR), so the threat model is written for a microphone that was
// listening:
//
//	replayed later      the 60 s expiry makes the recording worthless, and
//	                    the six confirmation digits derive from the LIVE TLS
//	                    session binding, so a replay shows different digits
//	man in the middle   two sessions, two bindings, two screens disagreeing
//	spent on connect    never — the capability is spent on CONFIRMATION,
//	                    or any neighbour who heard the tones could burn it
//	                    with one TCP packet
//
// What the offer deliberately is NOT: an identity, a key, or a membership.
// The identity freight travels later, over the TLS session the humans
// confirmed, and the root seed never travels at all (MD plan, decision 5).
//
// SOUND IS THE BUDGET. The carrier moves 24 payload bytes per 2.44-second
// block (audiopass.js AP.PAYLOAD), so every byte here is ~0.1 s of tone in
// somebody's room. The ordinary offer fits three blocks — the seven-second
// loop the plan promises — and the hard ceiling is four; an address that
// cannot fit is refused AT MINT, where it can still be fixed, not
// discovered by a listener waiting on a block that never decodes.
package pairing

import (
	"errors"
	"fmt"
	"io"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

// OfferVersion is the current wire version. Refusing unknown versions is
// deliberate: an offer is a security artifact, and "probably compatible" is
// not a property to guess about while somebody is holding two phones
// together.
const OfferVersion = 1

// OfferTTLSeconds is the offer's whole life. Sixty seconds is long enough
// to hold two devices near each other and short enough that a recording is
// stale before an attacker can act on it in the same room.
const OfferTTLSeconds = 60

// SoundBlockPayload mirrors audiopass.js AP.PAYLOAD — the sound carrier's
// bytes per 2.44-second block. Named here so the size ceiling is derived
// from the carrier rather than remembered about it.
const SoundBlockPayload = 24

// MaxOfferBytes is the encoded ceiling: four sound blocks. Three is the
// ordinary case (a LAN IPv4 address); the fourth exists for the longer
// addresses IPv6 makes real, and past it the offer is refused at mint.
const MaxOfferBytes = 4 * SoundBlockPayload

// offerFields is the record arity, NAMED (append-only — see ADR-009).
const offerFields = 4

// Offer is one pairing capability. See the package comment for what holding
// one does and does not grant.
type Offer struct {
	Version   uint8
	Secret    [32]byte
	Addr      string // the parent's dialable LAN address, host:port
	ExpiresAt uint64 // unix seconds; the offer's whole life is OfferTTLSeconds
}

// NewOffer mints an offer for a ceremony starting now: a fresh secret, the
// parent's address, the sixty-second window.
func NewOffer(rng io.Reader, addr string, nowUnix uint64) (*Offer, error) {
	o := &Offer{Version: OfferVersion, Addr: addr, ExpiresAt: nowUnix + OfferTTLSeconds}
	if _, err := io.ReadFull(rng, o.Secret[:]); err != nil {
		return nil, err
	}
	if err := o.validate(); err != nil {
		return nil, err
	}
	// The size ceiling is checked at MINT — the one moment the address can
	// still be chosen differently.
	if n := len(o.Encode()); n > MaxOfferBytes {
		return nil, fmt.Errorf("pairing: offer is %d bytes — past the %d-byte sound budget (address too long)",
			n, MaxOfferBytes)
	}
	return o, nil
}

// Expired reports whether the offer's window has closed.
func (o *Offer) Expired(nowUnix uint64) bool { return nowUnix >= o.ExpiresAt }

// Encode serializes the offer (deterministic CBOR, ADR-003).
func (o *Offer) Encode() []byte {
	var buf []byte
	buf = codec.AppendArray(buf, offerFields)
	buf = codec.AppendUint(buf, uint64(o.Version))
	buf = codec.AppendBytes(buf, o.Secret[:])
	buf = codec.AppendText(buf, o.Addr)
	buf = codec.AppendUint(buf, o.ExpiresAt)
	return buf
}

var errMalformedOffer = errors.New("pairing: malformed offer")

// DecodeOffer parses and VALIDATES an offer. Everything here arrived out of
// the air from an unauthenticated source, so a decoded offer is only ever a
// well-formed claim — proof comes later, from the session and the humans.
func DecodeOffer(data []byte) (*Offer, error) {
	d := codec.NewDecoder(data)
	acount, err := d.ReadArray()
	if err != nil || acount < offerFields {
		return nil, errMalformedOffer
	}
	o := &Offer{}
	v, err := d.ReadUint()
	if err != nil {
		return nil, errMalformedOffer
	}
	o.Version = uint8(v)
	raw, err := d.ReadBytes()
	if err != nil || len(raw) != len(o.Secret) {
		return nil, errMalformedOffer
	}
	copy(o.Secret[:], raw)
	if o.Addr, err = d.ReadText(); err != nil {
		return nil, errMalformedOffer
	}
	if o.ExpiresAt, err = d.ReadUint(); err != nil {
		return nil, errMalformedOffer
	}
	// Future fields are skipped, never refused (ADR-009): a listener from
	// this build must still hear an offer that grew a fallback relay.
	for i := offerFields; i < acount; i++ {
		if err := d.SkipItem(); err != nil {
			return nil, errMalformedOffer
		}
	}
	if err := o.validate(); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *Offer) validate() error {
	if o.Version != OfferVersion {
		return fmt.Errorf("pairing: unknown offer version %d", o.Version)
	}
	if o.Secret == ([32]byte{}) {
		// An all-zero secret is not a weak capability, it is NO capability —
		// and the one value a broken RNG produces most readily.
		return errors.New("pairing: offer carries no secret")
	}
	if o.Addr == "" {
		return errors.New("pairing: offer names no address to dial")
	}
	if o.ExpiresAt == 0 {
		return errors.New("pairing: offer has no expiry — a capability that cannot die is not one")
	}
	return nil
}
