// Package codec implements the deterministic CBOR subset used on the wire
// (ADR-003): shortest-form integers, definite lengths only, integer map keys
// in ascending order, no floats, no tags, no indefinite items.
//
// Encoders append to a caller-provided buffer. The Decoder is strict: any
// deviation from the deterministic form (non-shortest integer, indefinite
// length, float, unordered map keys via MapReader) is a hard error, because
// signed structures must have exactly one valid byte representation.
package codec

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// MaxItemLen bounds any single byte/text string, array, or map declared
// length. Frames larger than this are rejected before allocation.
const MaxItemLen = 1 << 20 // 1 MiB

const (
	majorUint  = 0
	majorNeg   = 1
	majorBytes = 2
	majorText  = 3
	majorArray = 4
	majorMap   = 5
	majorTag   = 6
	majorOther = 7
)

const (
	byteFalse = 0xf4
	byteTrue  = 0xf5
	byteNull  = 0xf6
)

// ErrTruncated is returned when the input ends inside an item.
var ErrTruncated = errors.New("codec: truncated input")

func errAt(pos int, format string, args ...any) error {
	return fmt.Errorf("codec: at byte %d: %s", pos, fmt.Sprintf(format, args...))
}

// ---- Encoding ----

func appendHead(dst []byte, major byte, v uint64) []byte {
	switch {
	case v < 24:
		return append(dst, major<<5|byte(v))
	case v <= 0xff:
		return append(dst, major<<5|24, byte(v))
	case v <= 0xffff:
		return append(dst, major<<5|25, byte(v>>8), byte(v))
	case v <= 0xffffffff:
		return append(dst, major<<5|26, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	default:
		return append(dst, major<<5|27,
			byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32),
			byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	}
}

// AppendUint appends an unsigned integer.
func AppendUint(dst []byte, v uint64) []byte { return appendHead(dst, majorUint, v) }

// AppendBytes appends a definite-length byte string.
func AppendBytes(dst []byte, b []byte) []byte {
	dst = appendHead(dst, majorBytes, uint64(len(b)))
	return append(dst, b...)
}

// AppendText appends a definite-length UTF-8 text string.
func AppendText(dst []byte, s string) []byte {
	dst = appendHead(dst, majorText, uint64(len(s)))
	return append(dst, s...)
}

// AppendArray appends an array header for n items.
func AppendArray(dst []byte, n int) []byte { return appendHead(dst, majorArray, uint64(n)) }

// AppendMap appends a map header for n pairs. Callers must emit keys in
// strictly ascending integer order to stay canonical.
func AppendMap(dst []byte, n int) []byte { return appendHead(dst, majorMap, uint64(n)) }

// AppendBool appends a boolean.
func AppendBool(dst []byte, v bool) []byte {
	if v {
		return append(dst, byteTrue)
	}
	return append(dst, byteFalse)
}

// ---- Decoding ----

// Decoder reads the deterministic subset from a byte slice. Byte-string and
// text reads return subslices of the input; callers that retain them beyond
// the input's lifetime must copy.
type Decoder struct {
	data []byte
	pos  int
}

func NewDecoder(data []byte) *Decoder { return &Decoder{data: data} }

// Pos returns the current offset, e.g. for slicing raw items.
func (d *Decoder) Pos() int { return d.pos }

// Done errors unless the input is fully consumed.
func (d *Decoder) Done() error {
	if d.pos != len(d.data) {
		return errAt(d.pos, "%d trailing bytes", len(d.data)-d.pos)
	}
	return nil
}

// readHead decodes a head for major types 0..5, enforcing shortest form and
// definite lengths.
func (d *Decoder) readHead() (major byte, value uint64, err error) {
	if d.pos >= len(d.data) {
		return 0, 0, ErrTruncated
	}
	b := d.data[d.pos]
	major = b >> 5
	ai := b & 0x1f
	start := d.pos
	d.pos++
	var n int
	switch {
	case ai < 24:
		return major, uint64(ai), nil
	case ai == 24:
		n = 1
	case ai == 25:
		n = 2
	case ai == 26:
		n = 4
	case ai == 27:
		n = 8
	case ai == 31:
		return 0, 0, errAt(start, "indefinite length forbidden")
	default:
		return 0, 0, errAt(start, "reserved additional info %d", ai)
	}
	if d.pos+n > len(d.data) {
		return 0, 0, ErrTruncated
	}
	for i := 0; i < n; i++ {
		value = value<<8 | uint64(d.data[d.pos+i])
	}
	d.pos += n
	// Shortest-form enforcement (RFC 8949 §4.2.1).
	min := uint64(24)
	switch n {
	case 2:
		min = 0x100
	case 4:
		min = 0x10000
	case 8:
		min = 0x100000000
	}
	if value < min {
		return 0, 0, errAt(start, "non-shortest integer encoding")
	}
	return major, value, nil
}

func (d *Decoder) expect(major byte) (uint64, error) {
	start := d.pos
	m, v, err := d.readHead()
	if err != nil {
		return 0, err
	}
	if m != major {
		return 0, errAt(start, "expected major type %d, got %d", major, m)
	}
	return v, nil
}

// ReadUint reads an unsigned integer.
func (d *Decoder) ReadUint() (uint64, error) { return d.expect(majorUint) }

// ReadBytes reads a byte string (subslice of input).
func (d *Decoder) ReadBytes() ([]byte, error) {
	start := d.pos
	n, err := d.expect(majorBytes)
	if err != nil {
		return nil, err
	}
	if n > MaxItemLen {
		return nil, errAt(start, "byte string of %d exceeds limit", n)
	}
	if d.pos+int(n) > len(d.data) {
		return nil, ErrTruncated
	}
	b := d.data[d.pos : d.pos+int(n)]
	d.pos += int(n)
	return b, nil
}

// ReadText reads a UTF-8 text string.
func (d *Decoder) ReadText() (string, error) {
	start := d.pos
	n, err := d.expect(majorText)
	if err != nil {
		return "", err
	}
	if n > MaxItemLen {
		return "", errAt(start, "text of %d exceeds limit", n)
	}
	if d.pos+int(n) > len(d.data) {
		return "", ErrTruncated
	}
	b := d.data[d.pos : d.pos+int(n)]
	d.pos += int(n)
	if !utf8.Valid(b) {
		return "", errAt(start, "invalid UTF-8 in text string")
	}
	return string(b), nil
}

// ReadArray reads an array header and returns the element count.
func (d *Decoder) ReadArray() (int, error) {
	start := d.pos
	n, err := d.expect(majorArray)
	if err != nil {
		return 0, err
	}
	if n > MaxItemLen {
		return 0, errAt(start, "array of %d exceeds limit", n)
	}
	return int(n), nil
}

// ReadBool reads a boolean.
func (d *Decoder) ReadBool() (bool, error) {
	if d.pos >= len(d.data) {
		return false, ErrTruncated
	}
	b := d.data[d.pos]
	switch b {
	case byteTrue:
		d.pos++
		return true, nil
	case byteFalse:
		d.pos++
		return false, nil
	default:
		return false, errAt(d.pos, "expected bool, got 0x%02x", b)
	}
}

const maxSkipDepth = 32

// SkipItem consumes exactly one item of any supported type, validating its
// deterministic form. Used for must-ignore of unknown fields (ADR-009).
func (d *Decoder) SkipItem() error { return d.skipItem(0) }

func (d *Decoder) skipItem(depth int) error {
	if depth > maxSkipDepth {
		return errAt(d.pos, "nesting deeper than %d", maxSkipDepth)
	}
	if d.pos >= len(d.data) {
		return ErrTruncated
	}
	b := d.data[d.pos]
	if b>>5 == majorOther {
		switch b {
		case byteTrue, byteFalse, byteNull:
			d.pos++
			return nil
		default:
			return errAt(d.pos, "unsupported simple/float 0x%02x", b)
		}
	}
	start := d.pos
	major, v, err := d.readHead()
	if err != nil {
		return err
	}
	switch major {
	case majorUint, majorNeg:
		return nil
	case majorBytes, majorText:
		if v > MaxItemLen {
			return errAt(start, "item of %d exceeds limit", v)
		}
		if d.pos+int(v) > len(d.data) {
			return ErrTruncated
		}
		d.pos += int(v)
		return nil
	case majorArray, majorMap:
		if v > MaxItemLen {
			return errAt(start, "container of %d exceeds limit", v)
		}
		items := v
		if major == majorMap {
			items *= 2
		}
		for i := uint64(0); i < items; i++ {
			if err := d.skipItem(depth + 1); err != nil {
				return err
			}
		}
		return nil
	default: // majorTag
		return errAt(start, "tags forbidden")
	}
}

// ReadRawItem consumes one item and returns its exact encoded bytes.
func (d *Decoder) ReadRawItem() ([]byte, error) {
	start := d.pos
	if err := d.SkipItem(); err != nil {
		return nil, err
	}
	return d.data[start:d.pos], nil
}

// ---- Canonical map traversal ----

// MapReader iterates a map with unsigned-integer keys, enforcing strictly
// ascending key order (which for uint keys equals canonical bytewise order).
// The caller must consume exactly one value item after each Next.
type MapReader struct {
	d    *Decoder
	n    int
	i    int
	prev uint64
	some bool
}

// ReadMapHeader begins reading a canonical integer-keyed map.
func (d *Decoder) ReadMapHeader() (*MapReader, error) {
	start := d.pos
	n, err := d.expect(majorMap)
	if err != nil {
		return nil, err
	}
	if n > MaxItemLen {
		return nil, errAt(start, "map of %d exceeds limit", n)
	}
	return &MapReader{d: d, n: int(n)}, nil
}

// Len returns the declared number of pairs.
func (m *MapReader) Len() int { return m.n }

// Next returns the next key, or ok=false when the map is exhausted.
func (m *MapReader) Next() (key uint64, ok bool, err error) {
	if m.i >= m.n {
		return 0, false, nil
	}
	start := m.d.pos
	k, err := m.d.ReadUint()
	if err != nil {
		return 0, false, err
	}
	if m.some && k <= m.prev {
		return 0, false, errAt(start, "map keys not strictly ascending (%d after %d)", k, m.prev)
	}
	m.prev, m.some = k, true
	m.i++
	return k, true, nil
}
