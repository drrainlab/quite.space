// The radio segment, carried inside the invitation that already exists.
//
// WHY IT TRAVELS HERE AT ALL. Automatic failover onto a radio assumes both
// devices already hold a compatible segment configuration — the same air and
// the same key. Until now that was agreed out of band, which is a promise with
// a hole in the middle: the moment the internet disappears is precisely the
// moment nobody can send anybody a configuration. A segment that has to be
// arranged AFTER the outage is a segment that does not exist when it matters.
//
// WHAT IT IS, EXACTLY. The seed authenticates the MECHANISM — SACK, COMMIT,
// CANCEL, and the framing of DATA — for radios on one segment. It is not a
// content key and never becomes one: a message is already sealed under the
// space's epoch keys before it reaches the radio layer, and somebody holding
// this seed and nothing else can read nothing at all.
//
// What they CAN do is participate on the air, which includes sending a CANCEL
// that stops a transfer. So this is a capability, and handing it to somebody
// is a real act rather than a routing detail. It rides with an invitation
// because an invitation is already the moment a person decides to let somebody
// in, and the copy in the interface says what is being shared.
package quicklink

import (
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

// Bounds. Every one of them is checked on BOTH sides: a sealed payload is
// attacker-supplied the moment somebody guesses or is given the words, and a
// decoder that trusts a length is a decoder that allocates whatever it is
// told to.
const (
	// MaxCarrierLen bounds the driver name. "rnode" is five.
	MaxCarrierLen = 32
	// MaxProfileLen bounds the PHY's name.
	MaxProfileLen = 64
	// SegmentSeedLen is exact, not a maximum. The seed is a digest, so any
	// other length is a different thing wearing its name.
	SegmentSeedLen = 32
)

// RadioSegment says which air a segment lives on, and how to authenticate on
// it. A zero value means the invitation carries no segment — the ordinary
// case for a node with no radio, and never an error.
type RadioSegment struct {
	// KDFVersion is which derivation the seed expects. Carried rather than
	// assumed because the day a second one exists is the day every radio on a
	// segment must agree on which is in use, and a guest that cannot agree
	// must refuse instead of deriving a key nobody else holds.
	KDFVersion uint64
	// Carrier names the driver the segment runs on. A guest whose radio is a
	// different kind refuses with a sentence rather than joining air it
	// cannot speak on.
	Carrier string
	// Profile names the PHY — frequency, bandwidth, spreading factor, coding
	// rate — as one word rather than as numbers.
	//
	// A NAME, deliberately. Numbers here would be a parameter matrix nothing
	// can set yet, shipped into a sealed payload where an interim shape
	// becomes permanent. A name a build does not recognise is refusable, and
	// refusing is the whole value: a radio on the wrong air hears nothing and
	// is told nothing, which is the failure this entire wave exists to end.
	Profile string
	// Seed is the segment seed itself.
	Seed []byte
}

// Present reports whether an invitation actually carried a segment.
func (s RadioSegment) Present() bool { return len(s.Seed) > 0 }

// Segment field numbers, inside the descriptor's own map.
const (
	segKeyKDF     = 1
	segKeyCarrier = 2
	segKeyProfile = 3
	segKeySeed    = 4
)

// ErrBadSegment is a descriptor this build will not act on.
var ErrBadSegment = errors.New("quicklink: this radio segment is not usable")

// Validate refuses a descriptor rather than half-using one.
func (s RadioSegment) Validate() error {
	if !s.Present() {
		return nil // absent is ordinary
	}
	if len(s.Seed) != SegmentSeedLen {
		return fmt.Errorf("%w: the seed is %d bytes, not %d",
			ErrBadSegment, len(s.Seed), SegmentSeedLen)
	}
	if s.Carrier == "" || len(s.Carrier) > MaxCarrierLen {
		return fmt.Errorf("%w: the carrier name is empty or too long", ErrBadSegment)
	}
	if s.Profile == "" || len(s.Profile) > MaxProfileLen {
		return fmt.Errorf("%w: the profile name is empty or too long", ErrBadSegment)
	}
	if s.KDFVersion == 0 {
		return fmt.Errorf("%w: no key-derivation version", ErrBadSegment)
	}
	return nil
}

func appendSegment(buf []byte, s RadioSegment) []byte {
	buf = codec.AppendMap(buf, 4)
	buf = codec.AppendUint(buf, segKeyKDF)
	buf = codec.AppendUint(buf, s.KDFVersion)
	buf = codec.AppendUint(buf, segKeyCarrier)
	buf = codec.AppendText(buf, s.Carrier)
	buf = codec.AppendUint(buf, segKeyProfile)
	buf = codec.AppendText(buf, s.Profile)
	buf = codec.AppendUint(buf, segKeySeed)
	buf = codec.AppendBytes(buf, s.Seed)
	return buf
}

func readSegment(d *codec.Decoder) (RadioSegment, error) {
	var s RadioSegment
	m, err := d.ReadMapHeader()
	if err != nil {
		return s, err
	}
	for {
		k, ok, err := m.Next()
		if err != nil {
			return s, err
		}
		if !ok {
			break
		}
		switch k {
		case segKeyKDF:
			s.KDFVersion, err = d.ReadUint()
		case segKeyCarrier:
			s.Carrier, err = d.ReadText()
		case segKeyProfile:
			s.Profile, err = d.ReadText()
		case segKeySeed:
			s.Seed, err = d.ReadBytes()
		default:
			// Must-ignore, the same rule as every other map in this package:
			// a newer build's extra field is skipped, not stumbled over.
			err = d.SkipItem()
		}
		if err != nil {
			return s, err
		}
	}
	// Bounds are enforced on the way IN, not left to the caller. A decoder
	// that hands back an unchecked 4 KiB "carrier" has already lost.
	if err := s.Validate(); err != nil {
		return RadioSegment{}, err
	}
	return s, nil
}
