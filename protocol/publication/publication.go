// Package publication defines the Content half of ADR-014: a publication
// document is a versioned, bounded, ordered block tree — meaning, not markup.
//
// Wire model: canonical CBOR (ADR-003) with append-only integer key tables.
// A Block is decoded GENERICALLY — id, type, raw props, children — so an
// unknown type survives as an opaque block (forward compatibility, ADR-014
// invariant 3); known types interpret their raw props via typed codecs. The
// AUTHORING validator rejects unknown types; the decoder never does.
package publication

import (
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

// Bounds (ADR-014 invariant 2). Checked by Validate, not the decoder.
const (
	MaxDepth          = 6
	MaxChildren       = 64
	MaxBlocks         = 512
	MaxTitle          = 200
	MaxSummary        = 1000
	MaxTextField      = 20_000
	MaxTotalText      = 120_000
	MaxSerializedSize = 512 * 1024
	MaxTags           = 12
	MaxGalleryAssets  = 24
	MaxCredits        = 32
)

// Document kinds and visibility intents (bounded vocabularies).
var allowedKinds = map[string]bool{"article": true, "note": true, "release": true, "log": true}
var allowedVisibility = map[string]bool{"space": true, "public-intent": true}
var allowedDiscussion = map[string]bool{"": true, "thread": true, "off": true}
var allowedLayout = map[string]bool{"": true, "editorial": true, "compact": true}

// Content block types carry no children; composition types carry children.
var contentTypes = map[string]bool{
	"heading": true, "text": true, "quote": true, "image": true,
	"gallery": true, "audio": true, "video-link": true, "file": true,
	"link": true, "code": true, "callout": true, "separator": true,
	"credits": true,
	// Reserved since PUB-0 (renders fallback until APP-1) so the grammar
	// never changes silently between gates.
	"app": true,
}
var compositionTypes = map[string]bool{
	"stack": true, "columns": true, "hero": true, "section": true,
}

// KnownBlockType reports whether the authoring grammar includes t.
func KnownBlockType(t string) bool { return contentTypes[t] || compositionTypes[t] }

// Document is the authored publication.
type Document struct {
	DocumentID     [16]byte
	Kind           string
	Title          string
	Summary        string
	Cover          string // hex asset id, optional
	DisplayAuthors []string
	Tags           []string
	Layout         string
	Discussion     string
	Visibility     string // visibility_intent: space | public-intent
	Blocks         []Block
}

// Block is one node of the tree. Props stays RAW on the wire; known types
// parse it via their typed codec (props*.go), unknown types keep it opaque.
type Block struct {
	ID       string
	Type     string
	RawProps []byte // canonical CBOR item; nil = no props
	Children []Block
}

const (
	docKeyID         = 1
	docKeyKind       = 2
	docKeyTitle      = 3
	docKeySummary    = 4
	docKeyCover      = 5
	docKeyAuthors    = 6
	docKeyTags       = 7
	docKeyLayout     = 8
	docKeyDiscussion = 9
	docKeyVisibility = 10
	docKeyBlocks     = 11
)

const (
	blKeyID       = 1
	blKeyType     = 2
	blKeyProps    = 3
	blKeyChildren = 4
)

// Encode serializes the document canonically.
func (doc *Document) Encode() []byte {
	n := 4 // id, kind, title, visibility always present
	if doc.Summary != "" {
		n++
	}
	if doc.Cover != "" {
		n++
	}
	if len(doc.DisplayAuthors) > 0 {
		n++
	}
	if len(doc.Tags) > 0 {
		n++
	}
	if doc.Layout != "" {
		n++
	}
	if doc.Discussion != "" {
		n++
	}
	if len(doc.Blocks) > 0 {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, docKeyID)
	buf = codec.AppendBytes(buf, doc.DocumentID[:])
	buf = codec.AppendUint(buf, docKeyKind)
	buf = codec.AppendText(buf, doc.Kind)
	buf = codec.AppendUint(buf, docKeyTitle)
	buf = codec.AppendText(buf, doc.Title)
	if doc.Summary != "" {
		buf = codec.AppendUint(buf, docKeySummary)
		buf = codec.AppendText(buf, doc.Summary)
	}
	if doc.Cover != "" {
		buf = codec.AppendUint(buf, docKeyCover)
		buf = codec.AppendText(buf, doc.Cover)
	}
	if len(doc.DisplayAuthors) > 0 {
		buf = codec.AppendUint(buf, docKeyAuthors)
		buf = codec.AppendArray(buf, len(doc.DisplayAuthors))
		for _, a := range doc.DisplayAuthors {
			buf = codec.AppendText(buf, a)
		}
	}
	if len(doc.Tags) > 0 {
		buf = codec.AppendUint(buf, docKeyTags)
		buf = codec.AppendArray(buf, len(doc.Tags))
		for _, tg := range doc.Tags {
			buf = codec.AppendText(buf, tg)
		}
	}
	if doc.Layout != "" {
		buf = codec.AppendUint(buf, docKeyLayout)
		buf = codec.AppendText(buf, doc.Layout)
	}
	if doc.Discussion != "" {
		buf = codec.AppendUint(buf, docKeyDiscussion)
		buf = codec.AppendText(buf, doc.Discussion)
	}
	buf = codec.AppendUint(buf, docKeyVisibility)
	buf = codec.AppendText(buf, doc.Visibility)
	if len(doc.Blocks) > 0 {
		buf = codec.AppendUint(buf, docKeyBlocks)
		buf = codec.AppendArray(buf, len(doc.Blocks))
		for i := range doc.Blocks {
			buf = doc.Blocks[i].encode(buf)
		}
	}
	return buf
}

func (b *Block) encode(buf []byte) []byte {
	n := 2 // id, type
	if len(b.RawProps) > 0 {
		n++
	}
	if len(b.Children) > 0 {
		n++
	}
	buf = codec.AppendMap(buf, n)
	buf = codec.AppendUint(buf, blKeyID)
	buf = codec.AppendText(buf, b.ID)
	buf = codec.AppendUint(buf, blKeyType)
	buf = codec.AppendText(buf, b.Type)
	if len(b.RawProps) > 0 {
		buf = codec.AppendUint(buf, blKeyProps)
		buf = append(buf, b.RawProps...) // already a canonical item
	}
	if len(b.Children) > 0 {
		buf = codec.AppendUint(buf, blKeyChildren)
		buf = codec.AppendArray(buf, len(b.Children))
		for i := range b.Children {
			buf = b.Children[i].encode(buf)
		}
	}
	return buf
}

// Decode parses a document, preserving unknown block types opaquely.
func Decode(payload []byte) (*Document, error) {
	if len(payload) > MaxSerializedSize {
		return nil, errors.New("publication: document too large")
	}
	d := codec.NewDecoder(payload)
	doc, err := decodeDocument(d)
	if err != nil {
		return nil, err
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	return doc, nil
}

func decodeDocument(d *codec.Decoder) (*Document, error) {
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	doc := &Document{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case docKeyID:
			b, e := d.ReadBytes()
			if e != nil {
				return nil, e
			}
			if len(b) != 16 {
				return nil, errors.New("publication: document id must be 16 bytes")
			}
			copy(doc.DocumentID[:], b)
		case docKeyKind:
			doc.Kind, er = d.ReadText()
		case docKeyTitle:
			doc.Title, er = d.ReadText()
		case docKeySummary:
			doc.Summary, er = d.ReadText()
		case docKeyCover:
			doc.Cover, er = d.ReadText()
		case docKeyAuthors:
			doc.DisplayAuthors, er = readTextArray(d, 16)
		case docKeyTags:
			doc.Tags, er = readTextArray(d, MaxTags)
		case docKeyLayout:
			doc.Layout, er = d.ReadText()
		case docKeyDiscussion:
			doc.Discussion, er = d.ReadText()
		case docKeyVisibility:
			doc.Visibility, er = d.ReadText()
		case docKeyBlocks:
			cnt, e := d.ReadArray()
			if e != nil {
				return nil, e
			}
			if cnt > MaxBlocks {
				return nil, errors.New("publication: too many blocks")
			}
			for range cnt {
				bl, e := decodeBlock(d, 1)
				if e != nil {
					return nil, e
				}
				doc.Blocks = append(doc.Blocks, bl)
			}
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	return doc, nil
}

func decodeBlock(d *codec.Decoder, depth int) (Block, error) {
	if depth > MaxDepth {
		return Block{}, errors.New("publication: block tree too deep")
	}
	mr, err := d.ReadMapHeader()
	if err != nil {
		return Block{}, err
	}
	var b Block
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return b, er
		}
		if !ok {
			break
		}
		switch k {
		case blKeyID:
			b.ID, er = d.ReadText()
		case blKeyType:
			b.Type, er = d.ReadText()
		case blKeyProps:
			// Preserve raw canonical bytes: unknown types stay opaque.
			b.RawProps, er = d.ReadRawItem()
		case blKeyChildren:
			cnt, e := d.ReadArray()
			if e != nil {
				return b, e
			}
			if cnt > MaxChildren {
				return b, errors.New("publication: too many children")
			}
			for range cnt {
				ch, e := decodeBlock(d, depth+1)
				if e != nil {
					return b, e
				}
				b.Children = append(b.Children, ch)
			}
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return b, er
		}
	}
	return b, nil
}

func readTextArray(d *codec.Decoder, max int) ([]string, error) {
	cnt, err := d.ReadArray()
	if err != nil {
		return nil, err
	}
	if cnt > max {
		return nil, fmt.Errorf("publication: array exceeds %d entries", max)
	}
	out := make([]string, 0, cnt)
	for range cnt {
		s, err := d.ReadText()
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}
