package storage

// The OFFER BOOK's wire form (LT-4 S5): what this node has already put
// into which mailbox, kept in a sealed document of its own rather than in
// the keystore — the keystore is re-sealed and rewritten whole on every
// save, and marks change on every successful push.
//
// Append-only like every other record here (ADR-009): offerRecFields is
// NAMED, bumped only when a field is appended, and the decoder skips what
// it does not know. An unknown document version is not decoded at all —
// the caller starts with an empty book, which costs one full re-offer and
// nothing else.

import (
	"errors"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// OfferRecord is one mailbox's mark: a recipient device's box for one
// space at one relay endpoint.
type OfferRecord struct {
	Space    id.TerminalID
	Device   id.DeviceID
	Endpoint string
	// Cursor is a LOG INDEX: frames with order index < Cursor were put into
	// this mailbox. The log's order is append-only, so the index never
	// shifts — unlike a count of custody-bearing frames, which shrinks when
	// one expires and then hides a newer frame under the mark.
	Cursor uint64
	Guess  bool  // laid on the bootstrap guess, not a stated route
	Legacy bool  // laid on a legacy-provenance route
	At     int64 // unix s: the last time anything was put here
	FullAt int64 // unix s: the last time the WHOLE history was laid here
}

// OfferBookVersion is the document version this build writes.
const OfferBookVersion = 1

// offerRecFields is the record arity, named; bump only when appending.
const offerRecFields = 8

// AppendOfferBook encodes the book: [version, [records…]].
func AppendOfferBook(buf []byte, recs []OfferRecord) []byte {
	buf = codec.AppendArray(buf, 2)
	buf = codec.AppendUint(buf, OfferBookVersion)
	buf = codec.AppendArray(buf, len(recs))
	for _, r := range recs {
		buf = codec.AppendArray(buf, offerRecFields)
		buf = codec.AppendBytes(buf, r.Space[:])
		buf = codec.AppendBytes(buf, r.Device[:])
		buf = codec.AppendText(buf, r.Endpoint)
		buf = codec.AppendUint(buf, r.Cursor)
		buf = codec.AppendBool(buf, r.Guess)
		buf = codec.AppendBool(buf, r.Legacy)
		buf = codec.AppendUint(buf, uint64(r.At))
		buf = codec.AppendUint(buf, uint64(r.FullAt))
	}
	return buf
}

// ErrOfferBookVersion says the document was written by a build this one
// does not understand; the caller treats the book as empty.
var ErrOfferBookVersion = errors.New("storage: offer book of an unknown version")

// DecodeOfferBook decodes what AppendOfferBook wrote, skipping appended
// fields it does not know.
func DecodeOfferBook(b []byte) ([]OfferRecord, error) {
	d := codec.NewDecoder(b)
	n, err := d.ReadArray()
	if err != nil || n < 2 {
		return nil, errors.New("storage: offer book: not a document")
	}
	v, err := d.ReadUint()
	if err != nil {
		return nil, err
	}
	if v != OfferBookVersion {
		return nil, ErrOfferBookVersion
	}
	cnt, err := d.ReadArray()
	if err != nil {
		return nil, err
	}
	out := make([]OfferRecord, 0, cnt)
	for i := 0; i < cnt; i++ {
		fields, err := d.ReadArray()
		if err != nil {
			return nil, err
		}
		if fields < offerRecFields {
			return nil, errors.New("storage: offer book: short record")
		}
		var r OfferRecord
		sp, err := d.ReadBytes()
		if err != nil {
			return nil, err
		}
		dv, err := d.ReadBytes()
		if err != nil {
			return nil, err
		}
		if len(sp) != len(r.Space) || len(dv) != len(r.Device) {
			return nil, errors.New("storage: offer book: malformed ids")
		}
		copy(r.Space[:], sp)
		copy(r.Device[:], dv)
		if r.Endpoint, err = d.ReadText(); err != nil {
			return nil, err
		}
		if r.Cursor, err = d.ReadUint(); err != nil {
			return nil, err
		}
		if r.Guess, err = d.ReadBool(); err != nil {
			return nil, err
		}
		if r.Legacy, err = d.ReadBool(); err != nil {
			return nil, err
		}
		at, err := d.ReadUint()
		if err != nil {
			return nil, err
		}
		full, err := d.ReadUint()
		if err != nil {
			return nil, err
		}
		r.At, r.FullAt = int64(at), int64(full)
		for j := offerRecFields; j < fields; j++ {
			if err := d.SkipItem(); err != nil {
				return nil, err
			}
		}
		out = append(out, r)
	}
	return out, nil
}
