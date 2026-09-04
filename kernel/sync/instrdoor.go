// Instrument door envelopes (QI-B1). Like the hello and the beacon next
// door, both are LINK-level messages: an epoch request is a device asking
// to be let in on THIS session, and the epochs reply is freight addressed
// to that session — neither routes to an engine. The payloads (who is
// asking, what proves it, which frames answer) are built and verified by
// the node; this file owns only the wire envelopes, in the same
// append-only message grammar every carrier already speaks.
//
// The plan reserved types 5/6 and keys 7-10 for these; by the time the
// wave landed, the TN wave had spent all four (custody, beacon, hello).
// Append-only means append: types 8/9, keys 11/12.
package sync

import "github.com/drrainlab/quiet_places/protocol/codec"

const (
	msgEpochReq = 8
	msgEpochs   = 9
	keyEpochReq = 11
	keyEpochs   = 12
)

// EncodeEpochReqMessage wraps an instrument's door knock for the carrier.
func EncodeEpochReqMessage(req []byte) []byte {
	buf := codec.AppendMap(nil, 2)
	buf = codec.AppendUint(buf, keyType)
	buf = codec.AppendUint(buf, msgEpochReq)
	buf = codec.AppendUint(buf, keyEpochReq)
	buf = codec.AppendBytes(buf, req)
	return buf
}

// ExtractEpochReq returns the door-knock payload if raw is one.
func ExtractEpochReq(raw []byte) ([]byte, bool) {
	msg, err := decodeMessage(raw)
	if err != nil || msg.msgType != msgEpochReq || len(msg.epochReq) == 0 {
		return nil, false
	}
	return msg.epochReq, true
}

// EncodeEpochsMessage wraps the epochs reply/push for the carrier.
func EncodeEpochsMessage(payload []byte) []byte {
	buf := codec.AppendMap(nil, 2)
	buf = codec.AppendUint(buf, keyType)
	buf = codec.AppendUint(buf, msgEpochs)
	buf = codec.AppendUint(buf, keyEpochs)
	buf = codec.AppendBytes(buf, payload)
	return buf
}

// ExtractEpochs returns the epochs payload if raw is an epochs message.
func ExtractEpochs(raw []byte) ([]byte, bool) {
	msg, err := decodeMessage(raw)
	if err != nil || msg.msgType != msgEpochs || len(msg.epochs) == 0 {
		return nil, false
	}
	return msg.epochs, true
}
