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

// ChainAdvert is one author chain's carried height, as announced by an
// element that has no log of its own. Deliberately NOT eventlog.ChainState:
// a bridge must be able to speak this without ever depending on a log
// implementation.
type ChainAdvert struct {
	Device          id.DeviceID
	ContiguousUntil uint64
}

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

// EncodeSummaryMessage builds a summary message on behalf of an element
// that holds no log — a bridge announcing HOW FAR IT HAS CARRIED each
// author chain, which is exactly what a node needs in order to know what to
// hand over. The wire is identical to an engine's own summary: a node on
// the far side cannot tell (and must not need to tell) whether it is
// talking to a peer or to a gateway.
//
// A bridge that announced nothing would be asking for the whole log on
// every wake-up; announcing what it actually carried keeps the answer to
// the delta, which on LoRa is the difference between a message and a
// morning of airtime.
func EncodeSummaryMessage(terminal id.TerminalID, chains []ChainAdvert) []byte {
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, keyType)
	buf = codec.AppendUint(buf, msgSummary)
	buf = codec.AppendUint(buf, keyTerminal)
	buf = codec.AppendBytes(buf, terminal[:])
	buf = codec.AppendUint(buf, keyChains)
	buf = codec.AppendArray(buf, len(chains))
	for _, c := range chains {
		buf = codec.AppendArray(buf, 2)
		buf = codec.AppendBytes(buf, c.Device[:])
		buf = codec.AppendUint(buf, c.ContiguousUntil)
	}
	return buf
}

// ExtractSummaryTerminal reports the terminal of a summary message. A
// bridge uses it for one purpose only: to learn that a node is alive on
// this carrier. It never answers a summary with a summary — that is how
// two elements talk each other's battery flat.
func ExtractSummaryTerminal(raw []byte) (id.TerminalID, bool) {
	msg, err := decodeMessage(raw)
	if err != nil || msg.msgType != msgSummary {
		return id.TerminalID{}, false
	}
	return msg.term, true
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
