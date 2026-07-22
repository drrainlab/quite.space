// Package receipts defines delivery/execution receipt payloads. A receipt is
// carried inside a normal signed Signal (schema receipt.delivery.v1), so its
// authenticity comes from the envelope signature; transports cannot forge
// destination-level proofs (ADR-007, ADR-008).
package receipts

import (
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// SchemaDelivery is the payload schema id for delivery receipts.
const SchemaDelivery = "receipt.delivery.v1"

// Payload key table v0.
const (
	keySubject = 1
	keyLevel   = 2
	keyAt      = 3
)

// Delivery asserts that the emitting terminal reached the given level for
// the subject event. Emitting receipts above one's proof is a protocol
// violation; the trust engine only accepts levels the receipt author can
// legitimately assert.
type Delivery struct {
	Subject id.EventID
	Level   claims.DeliveryLevel
	At      uint64 // unix seconds
}

// Encode returns the canonical payload bytes.
func (r *Delivery) Encode() ([]byte, error) {
	if r.Level == claims.DeliveryUnknown {
		return nil, errors.New("receipts: level required")
	}
	n := 2
	if r.At != 0 {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, keySubject)
	buf = codec.AppendBytes(buf, r.Subject[:])
	buf = codec.AppendUint(buf, keyLevel)
	buf = codec.AppendUint(buf, uint64(r.Level))
	if r.At != 0 {
		buf = codec.AppendUint(buf, keyAt)
		buf = codec.AppendUint(buf, r.At)
	}
	return buf, nil
}

// Decode parses a delivery receipt payload.
func Decode(payload []byte) (*Delivery, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	r := &Delivery{}
	var seenSubject, seenLevel bool
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case keySubject:
			b, e := d.ReadBytes()
			if e != nil {
				return nil, e
			}
			if len(b) != id.Size {
				return nil, fmt.Errorf("receipts: subject must be %d bytes", id.Size)
			}
			copy(r.Subject[:], b)
			seenSubject = true
		case keyLevel:
			v, e := d.ReadUint()
			if e != nil {
				return nil, e
			}
			if v > uint64(claims.DeliveryAcknowledgedByHuman) {
				// Unknown future levels degrade to unknown, never upward.
				v = uint64(claims.DeliveryUnknown)
			}
			r.Level = claims.DeliveryLevel(v)
			seenLevel = true
		case keyAt:
			v, e := d.ReadUint()
			if e != nil {
				return nil, e
			}
			r.At = v
		default:
			if err := d.SkipItem(); err != nil {
				return nil, err
			}
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if !seenSubject || !seenLevel {
		return nil, errors.New("receipts: missing subject or level")
	}
	return r, nil
}
