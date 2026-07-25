package publication

import (
	"bytes"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

func sampleDoc() *Document {
	doc := &Document{
		Kind: "release", Title: "Grain & Silk", Summary: "New EP.",
		Visibility: "space", Tags: []string{"music"},
	}
	doc.DocumentID[0] = 0xD1
	doc.Blocks = []Block{
		{ID: "b1", Type: "heading", RawProps: EncodeTextProps(TextProps{Text: "Grain & Silk"})},
		{ID: "b2", Type: "text", RawProps: EncodeTextProps(TextProps{Text: "Recorded at night."})},
		{ID: "b3", Type: "section", RawProps: EncodeTextProps(TextProps{Text: "Credits"}), Children: []Block{
			{ID: "b4", Type: "credits", RawProps: EncodeListProps(ListProps{Items: []string{"music", "Robert"}})},
		}},
	}
	return doc
}

func TestDocumentRoundTrip(t *testing.T) {
	doc := sampleDoc()
	enc := doc.Encode()
	got, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != doc.Title || len(got.Blocks) != 3 || got.Blocks[2].Children[0].Type != "credits" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if !bytes.Equal(got.Encode(), enc) {
		t.Fatal("re-encode not canonical")
	}
	if err := Validate(got, nil); err != nil {
		t.Fatalf("valid doc rejected: %v", err)
	}
}

// PA-1 space-card: a catalog card is a kind "space" document whose link
// block carries the target space's share link. It round-trips and passes
// authoring like any other document.
func TestSpaceCardRoundTrip(t *testing.T) {
	doc := &Document{
		Kind: "space", Title: "Quiet Commons", Summary: "An open room for everyone.",
		Visibility: "space", Tags: []string{"community", "open"},
	}
	doc.DocumentID[0] = 0xCA
	doc.Blocks = []Block{
		{ID: "l1", Type: "link", RawProps: EncodeTextProps(TextProps{
			Text: "https://relay.example:7411", More: "Open this space",
		})},
		{ID: "t1", Type: "text", RawProps: EncodeTextProps(TextProps{Text: "Say hello."})},
	}
	enc := doc.Encode()
	got, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "space" || len(got.Tags) != 2 || got.Blocks[0].Type != "link" {
		t.Fatalf("space-card round-trip mismatch: %+v", got)
	}
	if !bytes.Equal(got.Encode(), enc) {
		t.Fatal("space-card re-encode not canonical")
	}
	if err := Validate(got, nil); err != nil {
		t.Fatalf("valid space-card rejected: %v", err)
	}
}

// Forward compatibility: a document containing an UNKNOWN block type decodes
// (opaque), survives re-encode byte-exactly, but is rejected at authoring.
func TestUnknownBlockOpaqueSurvival(t *testing.T) {
	doc := sampleDoc()
	futureProps := codec.AppendMap(nil, 1)
	futureProps = codec.AppendUint(futureProps, 1)
	futureProps = codec.AppendText(futureProps, "hologram data")
	doc.Blocks = append(doc.Blocks, Block{ID: "bx", Type: "hologram.v2", RawProps: futureProps})
	enc := doc.Encode()

	got, err := Decode(enc)
	if err != nil {
		t.Fatalf("document with unknown block failed to decode: %v", err)
	}
	last := got.Blocks[len(got.Blocks)-1]
	if last.Type != "hologram.v2" || !bytes.Equal(last.RawProps, futureProps) {
		t.Fatal("unknown block not preserved opaquely")
	}
	if !bytes.Equal(got.Encode(), enc) {
		t.Fatal("opaque block did not survive re-encode")
	}
	// Authoring rejects it.
	if err := Validate(got, nil); err == nil || !strings.Contains(err.Error(), "unknown block type") {
		t.Fatalf("authoring accepted an unknown type: %v", err)
	}
}

func TestValidatorRejections(t *testing.T) {
	// Duplicate ids.
	d1 := sampleDoc()
	d1.Blocks[1].ID = "b1"
	if err := Validate(d1, nil); err == nil {
		t.Fatal("duplicate block id accepted")
	}
	// Children under a content block.
	d2 := sampleDoc()
	d2.Blocks[0].Children = []Block{{ID: "c", Type: "text", RawProps: EncodeTextProps(TextProps{Text: "x"})}}
	if err := Validate(d2, nil); err == nil {
		t.Fatal("children under content block accepted")
	}
	// Foreign asset ref.
	d3 := sampleDoc()
	d3.Blocks = append(d3.Blocks, Block{ID: "img", Type: "image",
		RawProps: EncodeAssetProps(AssetProps{Asset: strings.Repeat("ab", 16), Text: "alt"})})
	if err := Validate(d3, func(string) bool { return false }); err == nil {
		t.Fatal("unresolvable asset accepted")
	}
	if err := Validate(d3, func(string) bool { return true }); err != nil {
		t.Fatalf("resolvable asset rejected: %v", err)
	}
	// Bad visibility.
	d4 := sampleDoc()
	d4.Visibility = "public" // not a legal intent
	if err := Validate(d4, nil); err == nil {
		t.Fatal("illegal visibility accepted")
	}
	// Bad link URL.
	d5 := sampleDoc()
	d5.Blocks = append(d5.Blocks, Block{ID: "lnk", Type: "link",
		RawProps: EncodeTextProps(TextProps{Text: "javascript:alert(1)"})})
	if err := Validate(d5, nil); err == nil {
		t.Fatal("non-http link accepted")
	}
	// Depth bound.
	deep := Block{ID: "d0", Type: "stack"}
	cur := &deep
	for i := 1; i <= MaxDepth+1; i++ {
		child := Block{ID: "d" + strings.Repeat("x", i), Type: "stack"}
		cur.Children = []Block{child}
		cur = &cur.Children[0]
	}
	d6 := sampleDoc()
	d6.Blocks = []Block{deep}
	if err := Validate(d6, nil); err == nil {
		t.Fatal("over-deep tree accepted")
	}
}

func TestPayloadRoundTrips(t *testing.T) {
	doc := sampleDoc()
	rp := &RevisionPayload{Fallback: doc.Title, Document: doc.Encode()}
	got, err := DecodeRevisionPayload(rp.Encode())
	if err != nil || got.Fallback != doc.Title {
		t.Fatalf("revision payload: %v", err)
	}
	cp := &CommentPayload{Text: "great release"}
	cp.CommentID[0] = 1
	cp.DocumentID[0] = 2
	var parent [16]byte
	parent[0] = 3
	cp.ParentID = &parent
	gc, err := DecodeCommentPayload(cp.Encode())
	if err != nil || gc.ParentID == nil || gc.ParentID[0] != 3 {
		t.Fatalf("comment payload: %v", err)
	}
}
