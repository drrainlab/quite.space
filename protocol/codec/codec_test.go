package codec

import (
	"bytes"
	"testing"
)

func TestUintShortestForm(t *testing.T) {
	cases := []struct {
		v    uint64
		want []byte
	}{
		{0, []byte{0x00}},
		{23, []byte{0x17}},
		{24, []byte{0x18, 24}},
		{255, []byte{0x18, 0xff}},
		{256, []byte{0x19, 0x01, 0x00}},
		{65535, []byte{0x19, 0xff, 0xff}},
		{65536, []byte{0x1a, 0x00, 0x01, 0x00, 0x00}},
		{1<<32 - 1, []byte{0x1a, 0xff, 0xff, 0xff, 0xff}},
		{1 << 32, []byte{0x1b, 0, 0, 0, 1, 0, 0, 0, 0}},
	}
	for _, c := range cases {
		got := AppendUint(nil, c.v)
		if !bytes.Equal(got, c.want) {
			t.Errorf("AppendUint(%d) = %x, want %x", c.v, got, c.want)
		}
		d := NewDecoder(got)
		back, err := d.ReadUint()
		if err != nil || back != c.v || d.Done() != nil {
			t.Errorf("round-trip %d failed: %v %v", c.v, back, err)
		}
	}
}

func TestRejectNonShortest(t *testing.T) {
	bad := [][]byte{
		{0x18, 0x00},                   // 0 in 1-byte form
		{0x18, 0x17},                   // 23 in 1-byte form
		{0x19, 0x00, 0xff},             // 255 in 2-byte form
		{0x1a, 0x00, 0x00, 0xff, 0xff}, // 65535 in 4-byte form
	}
	for _, b := range bad {
		if _, err := NewDecoder(b).ReadUint(); err == nil {
			t.Errorf("accepted non-shortest encoding %x", b)
		}
	}
}

func TestRejectIndefiniteAndFloats(t *testing.T) {
	bad := [][]byte{
		{0x5f, 0x41, 0x01, 0xff}, // indefinite bytes
		{0x9f, 0xff},             // indefinite array
		{0xbf, 0xff},             // indefinite map
		{0xf9, 0x00, 0x00},       // float16
		{0xfa, 0, 0, 0, 0},       // float32
		{0xc2, 0x41, 0x01},       // tag
	}
	for _, b := range bad {
		if err := NewDecoder(b).SkipItem(); err == nil {
			t.Errorf("accepted forbidden item %x", b)
		}
	}
}

func TestBytesTextRoundTrip(t *testing.T) {
	buf := AppendBytes(nil, []byte{1, 2, 3})
	buf = AppendText(buf, "quiet")
	d := NewDecoder(buf)
	b, err := d.ReadBytes()
	if err != nil || !bytes.Equal(b, []byte{1, 2, 3}) {
		t.Fatalf("bytes: %x %v", b, err)
	}
	s, err := d.ReadText()
	if err != nil || s != "quiet" {
		t.Fatalf("text: %q %v", s, err)
	}
	if err := d.Done(); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidUTF8Rejected(t *testing.T) {
	buf := appendHead(nil, majorText, 2)
	buf = append(buf, 0xff, 0xfe)
	if _, err := NewDecoder(buf).ReadText(); err == nil {
		t.Fatal("accepted invalid UTF-8")
	}
}

func TestTruncated(t *testing.T) {
	full := AppendBytes(nil, make([]byte, 100))
	for cut := 0; cut < len(full); cut++ {
		if _, err := NewDecoder(full[:cut]).ReadBytes(); err == nil {
			t.Fatalf("accepted truncation at %d", cut)
		}
	}
}

func TestMapReaderOrdering(t *testing.T) {
	buf := AppendMap(nil, 2)
	buf = AppendUint(buf, 1)
	buf = AppendUint(buf, 10)
	buf = AppendUint(buf, 5)
	buf = AppendUint(buf, 50)
	m, err := NewDecoder(buf).ReadMapHeader()
	if err != nil {
		t.Fatal(err)
	}
	for {
		k, ok, err := m.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		if _, err := m.d.ReadUint(); err != nil {
			t.Fatal(err)
		}
		_ = k
	}

	// Descending keys must be rejected.
	buf = AppendMap(nil, 2)
	buf = AppendUint(buf, 5)
	buf = AppendUint(buf, 50)
	buf = AppendUint(buf, 1)
	buf = AppendUint(buf, 10)
	m, err = NewDecoder(buf).ReadMapHeader()
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = m.Next()
	if _, err := m.d.ReadUint(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Next(); err == nil {
		t.Fatal("accepted descending map keys")
	}

	// Duplicate keys must be rejected.
	buf = AppendMap(nil, 2)
	buf = AppendUint(buf, 5)
	buf = AppendUint(buf, 50)
	buf = AppendUint(buf, 5)
	buf = AppendUint(buf, 51)
	m, err = NewDecoder(buf).ReadMapHeader()
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = m.Next()
	if _, err := m.d.ReadUint(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Next(); err == nil {
		t.Fatal("accepted duplicate map keys")
	}
}

func TestRawItemPreservesBytes(t *testing.T) {
	inner := AppendMap(nil, 1)
	inner = AppendUint(inner, 99)
	inner = AppendText(inner, "future field")
	buf := AppendArray(nil, 2)
	buf = append(buf, inner...)
	buf = AppendUint(buf, 7)

	d := NewDecoder(buf)
	if _, err := d.ReadArray(); err != nil {
		t.Fatal(err)
	}
	raw, err := d.ReadRawItem()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, inner) {
		t.Fatalf("raw item %x != original %x", raw, inner)
	}
}

func TestOversizeDeclaredLength(t *testing.T) {
	buf := appendHead(nil, majorBytes, MaxItemLen+1)
	if _, err := NewDecoder(buf).ReadBytes(); err == nil {
		t.Fatal("accepted oversize declared length")
	}
}
