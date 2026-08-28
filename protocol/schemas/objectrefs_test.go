// Object references on the wire (SP-2.1, key 8) — the object twin of
// Mentions: a signed claim beside the text, append-only, and a message
// without refs stays byte-identical to yesterday's.
package schemas

import "testing"

func TestObjectRefsRoundTrip(t *testing.T) {
	var a, b [16]byte
	copy(a[:], []byte("0123456789abcdef"))
	copy(b[:], []byte("fedcba9876543210"))
	m := &TextMessage{Text: "люфт на CNC-01 ушёл", ObjectRefs: [][16]byte{a, b}}
	enc, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeTextMessage(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ObjectRefs) != 2 || got.ObjectRefs[0] != a || got.ObjectRefs[1] != b {
		t.Fatalf("refs lost: %+v", got.ObjectRefs)
	}
	// No refs → no key 8 → yesterday's bytes.
	plain := &TextMessage{Text: "просто сообщение"}
	e1, _ := plain.Encode()
	e2, _ := (&TextMessage{Text: "просто сообщение", ObjectRefs: nil}).Encode()
	if string(e1) != string(e2) {
		t.Fatal("refless message not byte-identical")
	}
	// Bound holds on both sides.
	over := &TextMessage{Text: "x"}
	for i := 0; i < MaxObjectRefs+1; i++ {
		var o [16]byte
		o[0] = byte(i + 1)
		over.ObjectRefs = append(over.ObjectRefs, o)
	}
	if _, err := over.Encode(); err == nil {
		t.Fatal("over-bound refs accepted")
	}
}
