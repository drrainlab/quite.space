// spacing: a separator that draws nothing.
//
// It is its own type rather than a prop on separator, and the reason is what
// happens to somebody running an older build. An unknown TYPE stays opaque
// and renders a fallback — one box in an otherwise readable post. An
// unexpected PROP on a known type fails validation, and validation failure
// is not local: the whole document is refused, so a single breath between
// two paragraphs would cost the reader everything else on the page.
package publication

import "testing"

func TestSpacingIsPartOfTheGrammar(t *testing.T) {
	if !KnownBlockType("spacing") {
		t.Fatal("spacing is not authorable")
	}
}

func TestSpacingCarriesNothing(t *testing.T) {
	// It is room. Room has no text, no asset and no list, and saying so in
	// the grammar is what stops it becoming a place to hide props later.
	doc := &Document{
		DocumentID: [16]byte{1}, Kind: "article", Title: "t", Visibility: "space",
		Blocks: []Block{{ID: "b1", Type: "spacing"}},
	}
	if err := Validate(doc, func(string) bool { return true }); err != nil {
		t.Fatalf("a bare spacing block was refused: %v", err)
	}

	doc.Blocks[0].RawProps = EncodeTextProps(TextProps{Text: "surprise"})
	if err := Validate(doc, func(string) bool { return true }); err == nil {
		t.Fatal("spacing accepted props; it is room, and room holds nothing")
	}
}
