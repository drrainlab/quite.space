// LAN hello envelope (T6-LAN). Like the gateway beacon next door, a hello is
// about the LINK, not about any space: it names no terminal, so it cannot be
// routed to an engine and is handled at link level. The payload itself — a
// device naming its key on one TLS session — is built and verified by the
// node; this file only owns the wire envelope, in the same append-only
// message grammar every carrier already speaks (an old peer's decoder skips
// the unknown key and the unknown type falls through its switch).
package sync

import "github.com/drrainlab/quiet_places/protocol/codec"

// msgHello carries a device's link-session introduction; keyHello the
// payload bytes (append-only tables, after msgBeacon=6 / keyBeacon=9).
const (
	msgHello = 7
	keyHello = 10
)

// EncodeHelloMessage wraps a hello payload for the carrier.
func EncodeHelloMessage(hello []byte) []byte {
	buf := codec.AppendMap(nil, 2)
	buf = codec.AppendUint(buf, keyType)
	buf = codec.AppendUint(buf, msgHello)
	buf = codec.AppendUint(buf, keyHello)
	buf = codec.AppendBytes(buf, hello)
	return buf
}

// ExtractHello returns the hello payload if raw is a hello message.
func ExtractHello(raw []byte) ([]byte, bool) {
	msg, err := decodeMessage(raw)
	if err != nil || msg.msgType != msgHello || len(msg.hello) == 0 {
		return nil, false
	}
	return msg.hello, true
}
