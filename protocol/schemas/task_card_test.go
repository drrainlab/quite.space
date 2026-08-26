// The task card's wire (SP-1 added key 6): a card may now belong to a
// domain object. The obligations are the usual append-only ones — a card
// without an object is byte-identical to yesterday's card, the id rides
// raw (16 bytes, not a derived target), and a malformed id is refused on
// decode, not just on encode.
package schemas

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func TestTaskCardRoundTripWithObject(t *testing.T) {
	var oid [16]byte
	copy(oid[:], []byte("0123456789abcdef"))
	assignee := id.PrincipalID{3}
	origin := id.EventID{4}
	created := id.EventID{5}
	c := &Card{
		Title:    "Проверить люфт шпинделя",
		Status:   "open",
		Assignee: &assignee,
		Origin:   &origin,
		Card:     &created,
		ObjectID: &oid,
	}
	enc, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeCard(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.ObjectID == nil || *got.ObjectID != oid {
		t.Fatalf("object id lost: %+v", got.ObjectID)
	}
	if got.Title != c.Title || got.Status != c.Status ||
		*got.Assignee != assignee || *got.Origin != origin || *got.Card != created {
		t.Fatalf("card changed in transit: %+v", got)
	}
}

func TestTaskCardWithoutObjectUnchanged(t *testing.T) {
	c := &Card{Title: "chore", Status: "open"}
	enc, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Two keys only — no key 6 on the wire when no object is attached,
	// so pre-SP-1 bytes and post-SP-1 bytes are the same bytes.
	want, _ := codec.AppendText(codec.AppendUint(codec.AppendText(codec.AppendUint(codec.AppendMap(nil, 2), 1), "chore"), 2), "open"), error(nil)
	if string(enc) != string(want) {
		t.Fatalf("objectless card no longer byte-identical: %x vs %x", enc, want)
	}
	got, err := DecodeCard(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.ObjectID != nil {
		t.Fatal("phantom object id")
	}
}

func TestTaskCardBadObjectIDRefused(t *testing.T) {
	// Hand-build a card with a 15-byte object id: honest encoders can't
	// make one, so go straight to the wire.
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, "t")
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendText(buf, "open")
	buf = codec.AppendUint(buf, 6)
	buf = codec.AppendBytes(buf, make([]byte, 15))
	if _, err := DecodeCard(buf); err == nil {
		t.Fatal("15-byte object id accepted")
	}
}

func TestNotedObservationRoundTrip(t *testing.T) {
	var oid [16]byte
	copy(oid[:], []byte("0123456789abcdef"))
	o := &NotedObservation{Text: "заметил люфт шпинделя", ObjectID: &oid, ObservedAt: 1787000000}
	enc, err := o.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeNotedObservation(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != o.Text || got.ObjectID == nil || *got.ObjectID != oid || got.ObservedAt != o.ObservedAt {
		t.Fatalf("mismatch: %+v", got)
	}
	// Journal note: no object, no timestamp — text alone is a whole note.
	solo := &NotedObservation{Text: "в мастерской пахнет канифолью"}
	enc2, err := solo.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got2, err := DecodeNotedObservation(enc2)
	if err != nil {
		t.Fatal(err)
	}
	if got2.ObjectID != nil || got2.ObservedAt != 0 {
		t.Fatalf("phantom fields: %+v", got2)
	}
}

func TestNotedObservationBounds(t *testing.T) {
	if _, err := (&NotedObservation{}).Encode(); err == nil {
		t.Fatal("empty text accepted")
	}
	long := make([]rune, MaxNotedTextRunes+1)
	for i := range long {
		long[i] = 'я'
	}
	if _, err := (&NotedObservation{Text: string(long)}).Encode(); err == nil {
		t.Fatal("over-long text accepted")
	}
	// Exactly at the bound is legal (multi-byte runes: the bound is
	// runes, not bytes).
	if _, err := (&NotedObservation{Text: string(long[:MaxNotedTextRunes])}).Encode(); err != nil {
		t.Fatalf("at-bound text refused: %v", err)
	}
	// Decode side enforces the same bound on hostile bytes.
	buf := codec.AppendMap(nil, 1)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, string(long))
	if _, err := DecodeNotedObservation(buf); err == nil {
		t.Fatal("decoder accepted over-long text")
	}
	if !Known(ObservationNoted) {
		t.Fatal("observation.noted.v1 not registered")
	}
}
