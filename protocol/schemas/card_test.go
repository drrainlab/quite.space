// The post card on the wire (PS-0).
//
// These bytes get signed and live forever, so the tests here are about the
// wire's obligations rather than the feature's: a message without a card is
// byte-identical to yesterday's, a card survives a round-trip, a decoder
// that has never heard of key 6 skips it, and every bound holds on both
// sides — because a bound checked only on encode is a bound only for
// honest senders.
package schemas

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

func TestCardRoundTrip(t *testing.T) {
	m := &TextMessage{
		Text: "> Robert · Slow Technology · 2026-07-31\n> Почему локальные сети снова важны",
		Origin: &ShareOrigin{
			AuthorLabel: "Robert", SourceLabel: "Slow Technology", OriginalAt: 1785400000,
		},
		Card: &SharedPublication{
			Title:     "Почему локальные сети снова важны",
			Summary:   "Короткое описание публикации",
			Reference: "c29tZS1yZWxheQpzcGFjZTphYmMxMjM",
		},
	}
	enc, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeTextMessage(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Card == nil {
		t.Fatal("the card did not survive the trip")
	}
	if *got.Card != *m.Card {
		t.Fatalf("card changed in transit: %+v != %+v", got.Card, m.Card)
	}
	// And the provenance beside it is untouched.
	if got.Origin == nil || got.Origin.AuthorLabel != "Robert" {
		t.Fatalf("origin lost beside the card: %+v", got.Origin)
	}
}

// A card without a reference is a legal card: a readable snapshot with no
// door. The sender declined, or no relay was known — either way the card
// itself still travels.
func TestCardWithoutReferenceIsStillACard(t *testing.T) {
	m := &TextMessage{
		Text: "> title",
		Card: &SharedPublication{Title: "title only"},
	}
	enc, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeTextMessage(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Card == nil || got.Card.Title != "title only" {
		t.Fatalf("snapshot card lost: %+v", got.Card)
	}
	if got.Card.Reference != "" {
		t.Fatalf("a reference appeared from nowhere: %q", got.Card.Reference)
	}
}

// Yesterday's wire: a message with no card must encode to bytes that carry
// no key 6 at all — not an empty map, nothing.
func TestMessageWithoutCardIsByteIdenticalToOldWire(t *testing.T) {
	m := &TextMessage{Text: "plain"}
	enc, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Re-encode through a struct that never had the field conceptually:
	// the exact bytes a pre-card build would produce.
	old := codec.AppendMap(nil, 1)
	old = codec.AppendUint(old, 1)
	old = codec.AppendText(old, "plain")
	if string(enc) != string(old) {
		t.Fatalf("a card-less message changed on the wire:\n new %x\n old %x", enc, old)
	}
}

// The forward-compat tail: a decoder that has never heard of key 6 (or any
// later key) skips it and keeps the text. This is the property that made
// key 4 safe and it must keep holding as keys are appended.
func TestCardIsSkippedByAnOlderDecoder(t *testing.T) {
	// Hand-build a message carrying key 6 AND an unknown key 9, then
	// decode with the current decoder — which handles 6 but not 7. Key 7
	// surviving proves the tail; the same tail is what a pre-card build
	// runs when it meets key 6.
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, "hello")
	buf = codec.AppendUint(buf, 6)
	buf = codec.AppendMap(buf, 1)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, "a title")
	buf = codec.AppendUint(buf, 9)
	buf = codec.AppendText(buf, "from the future")
	got, err := DecodeTextMessage(buf)
	if err != nil {
		t.Fatalf("a future key broke the decoder: %v", err)
	}
	if got.Text != "hello" || got.Card == nil || got.Card.Title != "a title" {
		t.Fatalf("known keys mangled beside an unknown one: %+v", got)
	}
	// And a card whose INNER map carries a future key decodes too.
	buf = codec.AppendMap(nil, 2)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, "hello")
	buf = codec.AppendUint(buf, 6)
	buf = codec.AppendMap(buf, 2)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, "a title")
	buf = codec.AppendUint(buf, 9)
	buf = codec.AppendText(buf, "inner future")
	if got, err = DecodeTextMessage(buf); err != nil || got.Card.Title != "a title" {
		t.Fatalf("inner future key broke the card: %v %+v", err, got)
	}
}

// Every bound refused on BOTH sides. The decode half is the one that
// matters: an encode-only bound binds honest senders and nobody else.
func TestCardBounds(t *testing.T) {
	long := strings.Repeat("x", MaxCardTitle+1)
	if _, err := (&TextMessage{Text: "t", Card: &SharedPublication{Title: long}}).Encode(); err == nil {
		t.Fatal("oversized title encoded")
	}
	if _, err := (&TextMessage{Text: "t",
		Card: &SharedPublication{Summary: strings.Repeat("x", MaxCardSummary+1)}}).Encode(); err == nil {
		t.Fatal("oversized summary encoded")
	}
	if _, err := (&TextMessage{Text: "t",
		Card: &SharedPublication{Reference: strings.Repeat("x", MaxShareRef+1)}}).Encode(); err == nil {
		t.Fatal("oversized reference encoded")
	}
	// Decode side: hand-build the oversized bytes an honest encoder refuses.
	build := func(key uint64, val string) []byte {
		buf := codec.AppendMap(nil, 2)
		buf = codec.AppendUint(buf, 1)
		buf = codec.AppendText(buf, "t")
		buf = codec.AppendUint(buf, 6)
		buf = codec.AppendMap(buf, 1)
		buf = codec.AppendUint(buf, key)
		buf = codec.AppendText(buf, val)
		return buf
	}
	if _, err := DecodeTextMessage(build(1, strings.Repeat("x", MaxCardTitle+1))); err == nil {
		t.Fatal("oversized title decoded")
	}
	if _, err := DecodeTextMessage(build(2, strings.Repeat("x", MaxCardSummary+1))); err == nil {
		t.Fatal("oversized summary decoded")
	}
	if _, err := DecodeTextMessage(build(3, strings.Repeat("x", MaxShareRef+1))); err == nil {
		t.Fatal("oversized reference decoded")
	}
}
