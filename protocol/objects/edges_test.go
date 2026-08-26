package objects

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/schemas"
)

const (
	hex64 = "aa11bb22cc33dd44ee55ff660011223344556677889900aabbccddeeff001122"
	hex32 = "0123456789abcdef0123456789abcdef"
)

func sampleEdge() *AttachPayload {
	p := &AttachPayload{
		Fallback: "take 04 · Winter Song",
		Asset:    hex64,
		Role:     "take",
		Label:    "take 04 · Katya",
		Ordinal:  4,
	}
	copy(p.ObjectID[:], []byte("0123456789abcdef"))
	return p
}

func TestAttachPayloadRoundTrip(t *testing.T) {
	p := sampleEdge()
	p.Supersedes = hex32
	p.Candidate = CandidateSet
	p.Detached = false
	enc, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeAttachPayload(enc)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *p {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, p)
	}
	// Minimal edge: object + asset only.
	min := &AttachPayload{Fallback: "x", Asset: hex32}
	copy(min.ObjectID[:], []byte("fedcba9876543210"))
	enc2, err := min.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got2, err := DecodeAttachPayload(enc2)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Candidate != CandidateUntouched || got2.Detached || got2.Role != "" {
		t.Fatalf("phantom fields: %+v", got2)
	}
	// Detached round-trips as a state.
	min.Detached = true
	enc3, _ := min.Encode()
	if got3, err := DecodeAttachPayload(enc3); err != nil || !got3.Detached {
		t.Fatalf("detached lost: %v %+v", err, got3)
	}
}

func TestAttachPayloadRefusals(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*AttachPayload)
	}{
		{"zero object", func(p *AttachPayload) { p.ObjectID = [16]byte{} }},
		{"empty asset", func(p *AttachPayload) { p.Asset = "" }},
		{"uppercase hex", func(p *AttachPayload) { p.Asset = strings.ToUpper(hex64) }},
		{"odd width", func(p *AttachPayload) { p.Asset = hex64[:40] }},
		{"non-hex", func(p *AttachPayload) { p.Asset = strings.Repeat("z", 64) }},
		{"role not slug", func(p *AttachPayload) { p.Role = "Not A Slug" }},
		{"label too long", func(p *AttachPayload) { p.Label = strings.Repeat("l", MaxEdgeLabel+1) }},
		{"supersedes bad hex", func(p *AttachPayload) { p.Supersedes = "beef" }},
		{"supersedes self", func(p *AttachPayload) { p.Supersedes = p.Asset }},
		{"candidate unknown", func(p *AttachPayload) { p.Candidate = 3 }},
	}
	for _, c := range cases {
		p := sampleEdge()
		c.mut(p)
		if _, err := p.Encode(); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

func TestRecordParentKey(t *testing.T) {
	r := sampleRecord()
	var parent [16]byte
	copy(parent[:], []byte("fedcba9876543210"))
	r.Parent = &parent
	enc, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Parent == nil || *got.Parent != parent {
		t.Fatalf("parent lost: %+v", got.Parent)
	}
	// Refusals: self-parent, zero parent.
	r.Parent = &r.ObjectID
	if _, err := r.Encode(); err == nil {
		t.Fatal("self-parent accepted")
	}
	var zero [16]byte
	r.Parent = &zero
	if _, err := r.Encode(); err == nil {
		t.Fatal("zero parent accepted")
	}
	// A parentless record still encodes byte-identically to SP-1 (no
	// phantom key 8 on the wire).
	r.Parent = nil
	enc2, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	base, _ := sampleRecord().Encode()
	if string(enc2) != string(base) {
		t.Fatal("parentless record no longer byte-identical")
	}
}

func TestEdgeSchemaRegistered(t *testing.T) {
	if !schemas.Known(SchemaAttached) {
		t.Fatal("object.attached.v1 not registered")
	}
}
