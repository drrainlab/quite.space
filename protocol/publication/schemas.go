package publication

import (
	"errors"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// Event schema ids (registry grammar <domain>.<type>.vN). Key 1 in every
// payload is the human fallback text (the block-family convention).
const (
	SchemaPublished = "publication.published.v1"
	SchemaRevised   = "publication.revised.v1"
	SchemaArchived  = "publication.archived.v1"
	SchemaRestored  = "publication.restored.v1" // reserved + registered (ADR-014 inv. 6)
	SchemaComment   = "publication.comment.v1"
)

// RevisionPayload carries a full document (published + revised share it).
// BaseRevision is what the author edited (optimistic concurrency at the API);
// PrevRevision is the projection tip the event chains from.
type RevisionPayload struct {
	Fallback     string // key 1 — the document title
	Document     []byte // key 2 — encoded Document
	BaseRevision *id.EventID
	PrevRevision *id.EventID
}

const (
	rpKeyFallback = 1
	rpKeyDocument = 2
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
	buf = codec.AppendUint(buf, rpKeyDocument)
	buf = codec.AppendBytes(buf, p.Document)
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
		case rpKeyDocument:
			p.Document, er = d.ReadBytes()
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
	if len(p.Document) == 0 {
		return nil, errors.New("publication: revision payload has no document")
	}
	// The embedded document must decode (opaque blocks allowed).
	if _, err := Decode(p.Document); err != nil {
		return nil, err
	}
	return p, nil
}

// LifecyclePayload covers archive/restore.
type LifecyclePayload struct {
	Fallback         string // key 1
	DocumentID       [16]byte
	ArchivedRevision *id.EventID // restore: the archived revision it undoes
}

const (
	lcKeyFallback = 1
	lcKeyDocument = 2
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
	buf = codec.AppendUint(buf, lcKeyDocument)
	buf = codec.AppendBytes(buf, p.DocumentID[:])
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
		case lcKeyDocument:
			er = read16(d, p.DocumentID[:])
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
	return p, d.Done()
}

// CommentPayload is thread-shaped from day one (ADR-014 invariant 7).
type CommentPayload struct {
	Text       string // key 1 — also the fallback
	CommentID  [16]byte
	DocumentID [16]byte
	ParentID   *[16]byte
}

const (
	cmKeyText     = 1
	cmKeyComment  = 2
	cmKeyDocument = 3
	cmKeyParent   = 4
)

func (p *CommentPayload) Encode() []byte {
	n := 3
	if p.ParentID != nil {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, cmKeyText)
	buf = codec.AppendText(buf, p.Text)
	buf = codec.AppendUint(buf, cmKeyComment)
	buf = codec.AppendBytes(buf, p.CommentID[:])
	buf = codec.AppendUint(buf, cmKeyDocument)
	buf = codec.AppendBytes(buf, p.DocumentID[:])
	if p.ParentID != nil {
		buf = codec.AppendUint(buf, cmKeyParent)
		buf = codec.AppendBytes(buf, p.ParentID[:])
	}
	return buf
}

func DecodeCommentPayload(payload []byte) (*CommentPayload, error) {
	d := codec.NewDecoder(payload)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	p := &CommentPayload{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case cmKeyText:
			p.Text, er = d.ReadText()
		case cmKeyComment:
			er = read16(d, p.CommentID[:])
		case cmKeyDocument:
			er = read16(d, p.DocumentID[:])
		case cmKeyParent:
			var pid [16]byte
			er = read16(d, pid[:])
			p.ParentID = &pid
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
	if p.Text == "" || len(p.Text) > MaxTextField {
		return nil, errors.New("publication: comment text missing or too long")
	}
	if p.CommentID == ([16]byte{}) || p.DocumentID == ([16]byte{}) {
		return nil, errors.New("publication: comment needs comment and document ids")
	}
	return p, nil
}

func read16(d *codec.Decoder, dst []byte) error { return readN(d, dst, 16) }
func read32(d *codec.Decoder, dst []byte) error { return readN(d, dst, 32) }

func readN(d *codec.Decoder, dst []byte, n int) error {
	b, err := d.ReadBytes()
	if err != nil {
		return err
	}
	if len(b) != n {
		return errors.New("publication: fixed-size field has wrong length")
	}
	copy(dst, b)
	return nil
}

func init() {
	rev := func(payload []byte) error { _, err := DecodeRevisionPayload(payload); return err }
	life := func(payload []byte) error { _, err := DecodeLifecyclePayload(payload); return err }
	schemas.Register(SchemaPublished, rev)
	schemas.Register(SchemaRevised, rev)
	schemas.Register(SchemaArchived, life)
	schemas.Register(SchemaRestored, life)
	schemas.Register(SchemaComment, func(payload []byte) error {
		_, err := DecodeCommentPayload(payload)
		return err
	})
}
