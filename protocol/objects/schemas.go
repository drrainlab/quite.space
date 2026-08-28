package objects

import (
	"errors"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// Event schema ids. Created and revised share one payload and one
// validator (the publication precedent): the split in names is for
// humans reading a log, not for machines branching.
const (
	SchemaCreated  = "object.created.v1"
	SchemaRevised  = "object.revised.v1"
	SchemaArchived = "object.archived.v1"
	SchemaRestored = "object.restored.v1"
)

// RevisionPayload carries the FULL record. BaseRevision is what the
// author edited (optimistic concurrency at the API); PrevRevision is the
// projection tip the event chains from.
type RevisionPayload struct {
	Fallback     string // key 1 — the object name
	Record       []byte // key 2 — encoded Record
	BaseRevision *id.EventID
	PrevRevision *id.EventID
}

const (
	rpKeyFallback = 1
	rpKeyRecord   = 2
	rpKeyBase     = 3
	rpKeyPrev     = 4
)

func (p *RevisionPayload) Encode() []byte {
	n := 2
	if p.BaseRevision != nil {
		n++
	}
	if p.PrevRevision != nil {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, rpKeyFallback)
	buf = codec.AppendText(buf, p.Fallback)
	buf = codec.AppendUint(buf, rpKeyRecord)
	buf = codec.AppendBytes(buf, p.Record)
	if p.BaseRevision != nil {
		buf = codec.AppendUint(buf, rpKeyBase)
		buf = codec.AppendBytes(buf, p.BaseRevision[:])
	}
	if p.PrevRevision != nil {
		buf = codec.AppendUint(buf, rpKeyPrev)
		buf = codec.AppendBytes(buf, p.PrevRevision[:])
	}
	return buf
}

func DecodeRevisionPayload(payload []byte) (*RevisionPayload, error) {
	d := codec.NewDecoder(payload)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	p := &RevisionPayload{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case rpKeyFallback:
			p.Fallback, er = d.ReadText()
		case rpKeyRecord:
			p.Record, er = d.ReadBytes()
		case rpKeyBase:
			var e id.EventID
			er = read32(d, e[:])
			p.BaseRevision = &e
		case rpKeyPrev:
			var e id.EventID
			er = read32(d, e[:])
			p.PrevRevision = &e
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if len(p.Record) == 0 {
		return nil, errors.New("objects: revision payload has no record")
	}
	if _, err := Decode(p.Record); err != nil {
		return nil, err
	}
	return p, nil
}

// LifecyclePayload covers archive and restore.
type LifecyclePayload struct {
	Fallback         string // key 1
	ObjectID         [16]byte
	ArchivedRevision *id.EventID // restore: the archive event it undoes
}

const (
	lcKeyFallback = 1
	lcKeyObject   = 2
	lcKeyRevision = 3
)

func (p *LifecyclePayload) Encode() []byte {
	n := 2
	if p.ArchivedRevision != nil {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, lcKeyFallback)
	buf = codec.AppendText(buf, p.Fallback)
	buf = codec.AppendUint(buf, lcKeyObject)
	buf = codec.AppendBytes(buf, p.ObjectID[:])
	if p.ArchivedRevision != nil {
		buf = codec.AppendUint(buf, lcKeyRevision)
		buf = codec.AppendBytes(buf, p.ArchivedRevision[:])
	}
	return buf
}

func DecodeLifecyclePayload(payload []byte) (*LifecyclePayload, error) {
	d := codec.NewDecoder(payload)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	p := &LifecyclePayload{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case lcKeyFallback:
			p.Fallback, er = d.ReadText()
		case lcKeyObject:
			er = read16(d, p.ObjectID[:])
		case lcKeyRevision:
			var e id.EventID
			er = read32(d, e[:])
			p.ArchivedRevision = &e
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	// A zero id covers both the missing key and the dishonestly zeroed
	// one — an object id is never all zeroes.
	if p.ObjectID == ([16]byte{}) {
		return nil, errors.New("objects: lifecycle payload names no object")
	}
	return p, nil
}

func init() {
	rev := func(p []byte) error { _, err := DecodeRevisionPayload(p); return err }
	lc := func(p []byte) error { _, err := DecodeLifecyclePayload(p); return err }
	schemas.Register(SchemaCreated, rev)
	schemas.Register(SchemaRevised, rev)
	schemas.Register(SchemaArchived, lc)
	schemas.Register(SchemaRestored, lc)
}
