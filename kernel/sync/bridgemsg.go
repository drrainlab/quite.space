// Bridge-facing message structure (TN-B). A blind bridge understands sync
// message STRUCTURE — never payloads: it extracts frames from msgFrames to
// take custody, re-emits msgFrames on the far segment, and answers with a
// signed custody receipt. The receipt is a TRANSPORT message, not an event:
// it never enters any log (a bridge has no membership and cannot author
// events, ADR-015 §8).
package sync

import (
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// msgCustody carries a custodian-signed receipt for accepted frames
// (append-only message-type table; raw peers skip unknown types).
const msgCustody = 5

// keyReceipt is the receipt bytes key (append-only key table).
const keyReceipt = 7

// EncodeFramesMessage builds a frames message for a terminal — the bridge
// re-emission path (identical wire to an engine's own msgFrames).
func EncodeFramesMessage(terminal id.TerminalID, frames [][]byte) []byte {
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, keyType)
	buf = codec.AppendUint(buf, msgFrames)
	buf = codec.AppendUint(buf, keyTerminal)
	buf = codec.AppendBytes(buf, terminal[:])
	buf = codec.AppendUint(buf, keyFrames)
	buf = codec.AppendArray(buf, len(frames))
	for _, f := range frames {
		buf = codec.AppendBytes(buf, f)
	}
	return buf
}

// ExtractFramesMessage parses a frames message; ok=false for any other
// message type (summaries, blobs, receipts — the bridge ignores them).
func ExtractFramesMessage(raw []byte) (id.TerminalID, [][]byte, bool) {
	msg, err := decodeMessage(raw)
	if err != nil || msg.msgType != msgFrames {
		return id.TerminalID{}, nil, false
	}
	return msg.term, msg.frames, true
}

// EncodeCustodyMessage wraps signed receipt bytes for the uplink peer.
func EncodeCustodyMessage(terminal id.TerminalID, receipt []byte) []byte {
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, keyType)
	buf = codec.AppendUint(buf, msgCustody)
	buf = codec.AppendUint(buf, keyTerminal)
	buf = codec.AppendBytes(buf, terminal[:])
	buf = codec.AppendUint(buf, keyReceipt)
	buf = codec.AppendBytes(buf, receipt)
	return buf
}
