package objects

import (
	"bytes"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

func sampleRecord() *Record {
	r := &Record{
		Kind:    "machine",
		Name:    "CNC-01",
		Status:  "operational",
		Summary: "Фрезер в углу мастерской",
		Props: []Prop{
			{Key: "location", Value: "corner bench"},
			{Key: "spindle", Value: "2.2kW"},
		},
		Cover: "a1b2c3",
	}
	copy(r.ObjectID[:], []byte("0123456789abcdef"))
	return r
}

func TestRecordRoundTrip(t *testing.T) {
	r := sampleRecord()
	enc, err := r.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Decode(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ObjectID != r.ObjectID || got.Kind != r.Kind || got.Name != r.Name ||
		got.Status != r.Status || got.Summary != r.Summary || got.Cover != r.Cover {
		t.Fatalf("round-trip mismatch: %+v vs %+v", got, r)
	}
	if len(got.Props) != 2 || got.Props[0] != r.Props[0] || got.Props[1] != r.Props[1] {
		t.Fatalf("props mismatch: %+v", got.Props)
	}
	// Minimal record: only required keys.
	min := &Record{Kind: "part", Name: "втулка"}
	copy(min.ObjectID[:], []byte("fedcba9876543210"))
	enc2, err := min.Encode()
	if err != nil {
		t.Fatalf("minimal encode: %v", err)
	}
	if _, err := Decode(enc2); err != nil {
		t.Fatalf("minimal decode: %v", err)
	}
}

func TestRecordBounds(t *testing.T) {
	base := func() *Record { return sampleRecord() }
	cases := []struct {
		name string
		mut  func(*Record)
	}{
		{"zero id", func(r *Record) { r.ObjectID = [16]byte{} }},
		{"empty kind", func(r *Record) { r.Kind = "" }},
		{"kind not slug", func(r *Record) { r.Kind = "Machine Tool" }},
		{"kind too long", func(r *Record) { r.Kind = strings.Repeat("k", MaxKindLen+1) }},
		{"empty name", func(r *Record) { r.Name = "" }},
		{"name too long", func(r *Record) { r.Name = strings.Repeat("n", MaxName+1) }},
		{"status not slug", func(r *Record) { r.Status = "IN REPAIR" }},
		{"summary too long", func(r *Record) { r.Summary = strings.Repeat("s", MaxSummary+1) }},
		{"unsorted props", func(r *Record) {
			r.Props = []Prop{{Key: "z", Value: "1"}, {Key: "a", Value: "2"}}
		}},
		{"duplicate props", func(r *Record) {
			r.Props = []Prop{{Key: "a", Value: "1"}, {Key: "a", Value: "2"}}
		}},
		{"prop key not slug", func(r *Record) { r.Props = []Prop{{Key: "Bad Key", Value: "1"}} }},
		{"prop value too long", func(r *Record) {
			r.Props = []Prop{{Key: "a", Value: strings.Repeat("v", MaxPropValLen+1)}}
		}},
	}
	for _, c := range cases {
		r := base()
		c.mut(r)
		if _, err := r.Encode(); err == nil {
			t.Errorf("%s: encode accepted invalid record", c.name)
		}
	}
	// too many props needs sorted keys to reach the count check honestly
	r := base()
	r.Props = nil
	for i := 0; i < MaxProps+1; i++ {
		r.Props = append(r.Props, Prop{Key: string(rune('a'+i/10)) + string(rune('a'+i%10)), Value: "v"})
	}
	if _, err := r.Encode(); err == nil {
		t.Error("sorted too-many props accepted")
	}
}

func TestRecordRawExtraSurvivesResave(t *testing.T) {
	r := sampleRecord()
	r.RawExtra = []Extra{
		{Key: 9, Raw: codec.AppendText(nil, "future field")},
		{Key: 12, Raw: codec.AppendUint(nil, 42)},
	}
	enc, err := r.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Decode(enc)
	if err != nil {
		t.Fatalf("decode with unknown keys: %v", err)
	}
	if len(got.RawExtra) != 2 || got.RawExtra[0].Key != 9 || got.RawExtra[1].Key != 12 {
		t.Fatalf("raw extra lost: %+v", got.RawExtra)
	}
	// The re-save round-trip: an older editor re-encoding must emit
	// byte-identical wire, or a newer field silently churns.
	re, err := got.Encode()
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if !bytes.Equal(enc, re) {
		t.Fatal("re-save is not byte-identical with unknown keys")
	}
}

func TestRecordTooLarge(t *testing.T) {
	r := sampleRecord()
	r.RawExtra = []Extra{{Key: 9, Raw: codec.AppendBytes(nil, make([]byte, MaxRecordBytes))}}
	// retainExtra drops over-budget extras rather than failing; the
	// record bound still holds because the passenger was shed.
	enc, err := r.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(enc) > MaxRecordBytes {
		t.Fatal("encode exceeded record bound")
	}
	got, err := Decode(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.RawExtra) != 0 {
		t.Fatal("over-budget extra should have been shed")
	}
	if _, err := Decode(make([]byte, MaxRecordBytes+1)); err == nil {
		t.Fatal("oversized payload accepted")
	}
}

func TestTargetStableAndDomainSeparated(t *testing.T) {
	r := sampleRecord()
	a := Target(r.ObjectID)
	b := Target(r.ObjectID)
	if a != b {
		t.Fatal("target not deterministic")
	}
	var other [16]byte
	copy(other[:], []byte("fedcba9876543210"))
	if Target(other) == a {
		t.Fatal("distinct objects share a target")
	}
	if a == (id.EventID{}) {
		t.Fatal("target is zero")
	}
}

func TestRevisionPayloadRoundTrip(t *testing.T) {
	rec, err := sampleRecord().Encode()
	if err != nil {
		t.Fatal(err)
	}
	base := id.EventID{1}
	prev := id.EventID{2}
	p := &RevisionPayload{Fallback: "CNC-01", Record: rec, BaseRevision: &base, PrevRevision: &prev}
	got, err := DecodeRevisionPayload(p.Encode())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Fallback != "CNC-01" || !bytes.Equal(got.Record, rec) ||
		*got.BaseRevision != base || *got.PrevRevision != prev {
		t.Fatalf("mismatch: %+v", got)
	}
	// A revision payload with a corrupt embedded record must be refused.
	bad := &RevisionPayload{Fallback: "x", Record: []byte{0xff}}
	if _, err := DecodeRevisionPayload(bad.Encode()); err == nil {
		t.Fatal("corrupt embedded record accepted")
	}
	if _, err := DecodeRevisionPayload((&RevisionPayload{Fallback: "x"}).Encode()); err == nil {
		t.Fatal("record-less revision accepted")
	}
}

func TestLifecyclePayloadRoundTrip(t *testing.T) {
	var oid [16]byte
	copy(oid[:], []byte("0123456789abcdef"))
	arch := id.EventID{7}
	p := &LifecyclePayload{Fallback: "CNC-01", ObjectID: oid, ArchivedRevision: &arch}
	got, err := DecodeLifecyclePayload(p.Encode())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ObjectID != oid || *got.ArchivedRevision != arch || got.Fallback != "CNC-01" {
		t.Fatalf("mismatch: %+v", got)
	}
	// Object id is mandatory.
	if _, err := DecodeLifecyclePayload((&LifecyclePayload{Fallback: "x"}).Encode()); err == nil {
		t.Fatal("lifecycle without object accepted")
	}
}

func TestSchemasRegistered(t *testing.T) {
	for _, s := range []string{SchemaCreated, SchemaRevised, SchemaArchived, SchemaRestored} {
		if !schemas.Known(s) {
			t.Errorf("%s not registered", s)
		}
	}
}
