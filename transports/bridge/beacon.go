// Gateway beacons (RB-2). A segment gateway announces that it exists.
//
// Without this, a person off the internet has no way to tell "there is no
// gateway on this mesh" from "the gateway is there and busy" from "my radio
// is on the wrong channel". All three look the same: nothing happens. The
// first custody receipt does eventually prove a gateway exists, but only
// after a message has already been sent into what might be silence.
//
// A beacon is a TRANSPORT message, not an event (ADR-015 §8). It never
// enters any log. Replicating gateway topology into the event log would hand
// every member a map of every gateway that ever served them, permanently.
//
// It names nobody. No terminal id, no device id, no space: a beacon says a
// gateway is here and how it is doing, and a listener on the carrier learns
// nothing about who uses it.
//
// Trust is the same pin as for custody receipts (ADR-015 §7): a beacon from
// an unpinned key is shown as UNTRUSTED, never silently believed. That is
// the bootstrap ritual — the person sees a fingerprint and checks it against
// what the operator told them, rather than trusting whatever spoke first.
package bridge

import (
	"crypto/ed25519"
	"errors"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

// Field limits. A beacon rides a shared LoRa carrier, so its size is a cost
// everyone on the segment pays.
const (
	MaxNetworkIDLen = 32
	MaxLabelLen     = 24
)

// Beacon is a gateway's signed announcement of itself.
//
// Freshness deliberately does NOT depend on the receiver's clock. A device
// that has been off the internet for days may have a badly wrong system
// time, and a presence check that silently failed because of clock drift
// would be the least debuggable failure in the whole wave. BootID, Sequence
// and ValidFor are all the receiver needs: it measures elapsed time on its
// own monotonic clock and never compares anything to a wall clock.
type Beacon struct {
	Version   uint64
	NetworkID string
	Label     string

	// BootID is random per gateway process start, and Sequence increases
	// within one boot. Together they order announcements without any shared
	// clock: a replayed beacon repeats a (BootID, Sequence) already seen.
	BootID   uint64
	Sequence uint64

	// IssuedSlot is the gateway's own absolute time. It is used ONLY to
	// order one boot against another — comparing two gateway claims, never a
	// claim against the receiver's clock. Skew here cannot break discovery.
	IssuedSlot uint64

	// ValidFor is how many seconds this announcement stays good, measured by
	// the receiver's own elapsed time from when it arrived.
	ValidFor uint64

	// UplinkUp says the gateway can currently reach the internet relay. A
	// gateway with a dead uplink still carries within the segment, so this
	// changes what a person should expect, not whether to use it.
	UplinkUp   bool
	QueueDepth uint64

	PublicKey []byte
	Signature []byte
}

// Beacon field keys (append-only; unknown keys are skipped).
const (
	bkVersion   = 1
	bkNetwork   = 2
	bkLabel     = 3
	bkBootID    = 4
	bkSequence  = 5
	bkIssued    = 6
	bkValidFor  = 7
	bkUplink    = 8
	bkQueue     = 9
	bkPublicKey = 10
	bkSignature = 11
)

func (b *Beacon) encode(withSig bool) []byte {
	n := 10
	if withSig {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, bkVersion)
	buf = codec.AppendUint(buf, b.Version)
	buf = codec.AppendUint(buf, bkNetwork)
	buf = codec.AppendText(buf, b.NetworkID)
	buf = codec.AppendUint(buf, bkLabel)
	buf = codec.AppendText(buf, b.Label)
	buf = codec.AppendUint(buf, bkBootID)
	buf = codec.AppendUint(buf, b.BootID)
	buf = codec.AppendUint(buf, bkSequence)
	buf = codec.AppendUint(buf, b.Sequence)
	buf = codec.AppendUint(buf, bkIssued)
	buf = codec.AppendUint(buf, b.IssuedSlot)
	buf = codec.AppendUint(buf, bkValidFor)
	buf = codec.AppendUint(buf, b.ValidFor)
	buf = codec.AppendUint(buf, bkUplink)
	buf = codec.AppendUint(buf, boolUint(b.UplinkUp))
	buf = codec.AppendUint(buf, bkQueue)
	buf = codec.AppendUint(buf, b.QueueDepth)
	buf = codec.AppendUint(buf, bkPublicKey)
	buf = codec.AppendBytes(buf, b.PublicKey)
	if withSig {
		buf = codec.AppendUint(buf, bkSignature)
		buf = codec.AppendBytes(buf, b.Signature)
	}
	return buf
}

func boolUint(v bool) uint64 {
	if v {
		return 1
	}
	return 0
}

// SignBeacon signs a beacon with the gateway's custodian key — the same key
// its custody receipts carry, so a person has one fingerprint to check
// rather than two.
func SignBeacon(priv ed25519.PrivateKey, b Beacon) ([]byte, error) {
	if len(b.NetworkID) > MaxNetworkIDLen {
		return nil, errors.New("bridge: network id too long for a broadcast beacon")
	}
	if len(b.Label) > MaxLabelLen {
		return nil, errors.New("bridge: gateway label too long for a broadcast beacon")
	}
	if b.ValidFor == 0 {
		return nil, errors.New("bridge: a beacon that is valid for no time says nothing")
	}
	b.PublicKey = priv.Public().(ed25519.PublicKey)
	b.Signature = ed25519.Sign(priv, b.encode(false))
	return b.encode(true), nil
}

// VerifyBeacon checks the signature and returns the claims. It says nothing
// about whether the key is TRUSTED — that is the receiver's pin decision,
// kept separate on purpose: authenticity and authority are different
// questions, and merging them is how "the signature is valid" quietly
// becomes "this gateway is mine".
func VerifyBeacon(raw []byte) (*Beacon, error) {
	b := &Beacon{}
	d := codec.NewDecoder(raw)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, errors.New("bridge: beacon is not a map")
	}
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case bkVersion:
			b.Version, err = d.ReadUint()
		case bkNetwork:
			b.NetworkID, err = d.ReadText()
		case bkLabel:
			b.Label, err = d.ReadText()
		case bkBootID:
			b.BootID, err = d.ReadUint()
		case bkSequence:
			b.Sequence, err = d.ReadUint()
		case bkIssued:
			b.IssuedSlot, err = d.ReadUint()
		case bkValidFor:
			b.ValidFor, err = d.ReadUint()
		case bkUplink:
			var v uint64
			v, err = d.ReadUint()
			b.UplinkUp = v != 0
		case bkQueue:
			b.QueueDepth, err = d.ReadUint()
		case bkPublicKey:
			b.PublicKey, err = d.ReadBytes()
		case bkSignature:
			b.Signature, err = d.ReadBytes()
		default:
			err = d.SkipItem()
		}
		if err != nil {
			return nil, err
		}
	}
	if len(b.PublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("bridge: beacon without a custodian key")
	}
	if len(b.Signature) != ed25519.SignatureSize {
		return nil, errors.New("bridge: beacon without a signature")
	}
	if len(b.NetworkID) > MaxNetworkIDLen || len(b.Label) > MaxLabelLen {
		return nil, errors.New("bridge: beacon fields exceed their limits")
	}
	sig := b.Signature
	b.Signature = nil
	if !ed25519.Verify(b.PublicKey, b.encode(false), sig) {
		return nil, errors.New("bridge: beacon signature does not verify")
	}
	b.Signature = sig
	return b, nil
}
