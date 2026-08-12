package schemas

import (
	"bytes"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func principal(b byte) id.PrincipalID {
	var p id.PrincipalID
	p[0] = b
	return p
}

// Mentions ride as a signed structural field (key 3) and survive a
// round-trip byte-exactly.
func TestTextMessageMentionsRoundTrip(t *testing.T) {
	reply := id.EventID{9: 0xAA}
	m := &TextMessage{
		Text:     "@bob посмотришь конфиг реле до вечера?",
		ReplyTo:  &reply,
		Mentions: []id.PrincipalID{principal(1), principal(2)},
	}
	enc, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeTextMessage(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != m.Text {
		t.Fatalf("text mismatch: %q", got.Text)
	}
	if got.ReplyTo == nil || *got.ReplyTo != reply {
		t.Fatal("reply_to lost")
	}
	if len(got.Mentions) != 2 || got.Mentions[0] != principal(1) || got.Mentions[1] != principal(2) {
		t.Fatalf("mentions lost: %+v", got.Mentions)
	}
	re, err := got.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(re, enc) {
		t.Fatal("re-encode is not canonical")
	}
}

// A message without mentions must encode EXACTLY as before the wave — the
// new key is only present when used (no silent format churn).
func TestTextMessageWithoutMentionsUnchanged(t *testing.T) {
	m := &TextMessage{Text: "hello"}
	enc, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Hand-build the pre-wave encoding: {1: "hello"}.
	want := codec.AppendMap(nil, 1)
	want = codec.AppendUint(want, 1)
	want = codec.AppendText(want, "hello")
	if !bytes.Equal(enc, want) {
		t.Fatalf("mention-free message changed on the wire:\n got %x\nwant %x", enc, want)
	}
}

// ADR-009 must-ignore: a decoder that predates key 3 skips it and still
// reads the message. We simulate the old decoder by asserting that an
// unknown-to-it key does not break parsing — the shipped decoder's
// `default: SkipItem` arm is the same mechanism key 4+ would hit.
func TestUnknownKeySkippedLikeOldDecoder(t *testing.T) {
	// {1: text, 3: mentions, 7: <future field>} — key 9 is unknown today.
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, "future-proof")
	buf = codec.AppendUint(buf, 3)
	buf = codec.AppendArray(buf, 1)
	p := principal(5)
	buf = codec.AppendBytes(buf, p[:])
	buf = codec.AppendUint(buf, 9)
	buf = codec.AppendText(buf, "a field from a later version")

	got, err := DecodeTextMessage(buf)
	if err != nil {
		t.Fatalf("unknown key broke decoding: %v", err)
	}
	if got.Text != "future-proof" || len(got.Mentions) != 1 || got.Mentions[0] != p {
		t.Fatalf("payload lost around the unknown key: %+v", got)
	}
}

func TestMentionBounds(t *testing.T) {
	// Too many mentions: refused on encode.
	many := make([]id.PrincipalID, MaxMentions+1)
	if _, err := (&TextMessage{Text: "x", Mentions: many}).Encode(); err == nil {
		t.Fatal("over-long mention list accepted")
	}
	// Exactly at the cap: fine.
	ok := make([]id.PrincipalID, MaxMentions)
	if _, err := (&TextMessage{Text: "x", Mentions: ok}).Encode(); err != nil {
		t.Fatalf("cap-sized mention list refused: %v", err)
	}
	// Too many on the wire: refused on decode.
	buf := codec.AppendMap(nil, 2)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, "x")
	buf = codec.AppendUint(buf, 3)
	buf = codec.AppendArray(buf, MaxMentions+1)
	for range MaxMentions + 1 {
		var p id.PrincipalID
		buf = codec.AppendBytes(buf, p[:])
	}
	if _, err := DecodeTextMessage(buf); err == nil ||
		!strings.Contains(err.Error(), "too many mentions") {
		t.Fatalf("wire flood accepted: %v", err)
	}
	// Wrong-sized mention: refused.
	bad := codec.AppendMap(nil, 2)
	bad = codec.AppendUint(bad, 1)
	bad = codec.AppendText(bad, "x")
	bad = codec.AppendUint(bad, 3)
	bad = codec.AppendArray(bad, 1)
	bad = codec.AppendBytes(bad, []byte{1, 2, 3})
	if _, err := DecodeTextMessage(bad); err == nil {
		t.Fatal("short mention id accepted")
	}
}
