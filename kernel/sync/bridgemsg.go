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

// msgCustody carries a custodian-signed receipt for accepted frames;
// msgBeacon carries a gateway's signed announcement of itself (append-only
// message-type table; raw peers skip unknown types).
const (
	msgCustody = 5
	msgBeacon  = 6
)

// keyReceipt is the receipt bytes key; keyBeacon the beacon bytes key
// (append-only key table).
const (
	keyReceipt = 7
	keyBeacon  = 9
)

// EncodeBeaconMessage wraps a signed beacon for the carrier.
//
// A beacon names no terminal — it is about the SEGMENT, not about any space.
// That is why it cannot ride a per-space engine and is handled at link level:
// putting a terminal id on it would tell every listener on the carrier which
// spaces this segment serves.
func EncodeBeaconMessage(beacon []byte) []byte {
	buf := codec.AppendMap(nil, 2)
	buf = codec.AppendUint(buf, keyType)
	buf = codec.AppendUint(buf, msgBeacon)
	buf = codec.AppendUint(buf, keyBeacon)
	buf = codec.AppendBytes(buf, beacon)
	return buf
}

// ExtractBeacon returns the signed beacon bytes; ok=false for anything else.
func ExtractBeacon(raw []byte) ([]byte, bool) {
	msg, err := decodeMessage(raw)
	if err != nil || msg.msgType != msgBeacon || len(msg.beacon) == 0 {
		return nil, false
	}
	return msg.beacon, true
}

// EncodeFramesMessage builds a frames message for a terminal — the bridge
// re-emission path (identical wire to an engine's own msgFrames).
func EncodeFramesMessage(terminal id.TerminalID, frames [][]byte) []byte {
	return encodeFramesWithAttempt(terminal, frames, nil)
}

// EncodeFramesMessageWithAttempt stamps the sender's responsibility token on
// the message. A gateway must persist that token with the custody it takes
// and echo it in every receipt about that custody, so the sender can tell
// which of its own hand-offs a receipt is answering.
func EncodeFramesMessageWithAttempt(terminal id.TerminalID, frames [][]byte,
	attempt []byte) []byte {
	return encodeFramesWithAttempt(terminal, frames, attempt)
}

func encodeFramesWithAttempt(terminal id.TerminalID, frames [][]byte,
	attempt []byte) []byte {

	n := 3
	if len(attempt) > 0 {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, keyType)
	buf = codec.AppendUint(buf, msgFrames)
	buf = codec.AppendUint(buf, keyTerminal)
	buf = codec.AppendBytes(buf, terminal[:])
	buf = codec.AppendUint(buf, keyFrames)
	buf = codec.AppendArray(buf, len(frames))
	for _, f := range frames {
		buf = codec.AppendBytes(buf, f)
	}
	if len(attempt) > 0 {
		buf = codec.AppendUint(buf, keyAttempt)
		buf = codec.AppendBytes(buf, attempt)
	}
	return buf
}

// ExtractFramesMessage parses a frames message; ok=false for any other
// message type (summaries, blobs, receipts — the bridge ignores them).
func ExtractFramesMessage(raw []byte) (id.TerminalID, [][]byte, bool) {
	term, frames, _, ok := ExtractFramesWithAttempt(raw)
	return term, frames, ok
}

// ExtractFramesWithAttempt also returns the sender's responsibility token,
// nil when the sender stamped none. A gateway that takes custody must store
// the token with the record BEFORE acknowledging, so a withdrawal issued
// hours later still names the hand-off it belongs to.
func ExtractFramesWithAttempt(raw []byte) (id.TerminalID, [][]byte, []byte, bool) {
	msg, err := decodeMessage(raw)
	if err != nil || msg.msgType != msgFrames {
		return id.TerminalID{}, nil, nil, false
	}
	return msg.term, msg.frames, msg.attempt, true
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

// ExtractCustodyReceipt pulls the raw receipt bytes out of a custody
// message. The verification — signature, and above all whether this
// custodian is PINNED for the link it arrived on — belongs to the node.
func ExtractCustodyReceipt(raw []byte) ([]byte, bool) {
	msg, err := decodeMessage(raw)
	if err != nil || msg.msgType != msgCustody || len(msg.receipt) == 0 {
		return nil, false
	}
	return msg.receipt, true
}

// ExtractSummaryChains parses a summary into the chains it announces. Used
// by tests that need to see exactly what a summary discloses on a shared
// carrier, and by anything that must compare two summaries field by field.
func ExtractSummaryChains(raw []byte) (id.TerminalID, []ChainAdvert, bool) {
	msg, err := decodeMessage(raw)
	if err != nil || msg.msgType != msgSummary || msg.sum == nil {
		return id.TerminalID{}, nil, false
	}
	out := make([]ChainAdvert, 0, len(msg.sum.chains))
	for dev, until := range msg.sum.chains {
		out = append(out, ChainAdvert{Device: dev, ContiguousUntil: until})
	}
	return msg.term, out, true
}

// PeekTerminal reports which terminal a sync message belongs to, without
// interpreting it further. This is what a link shared by several terminals
// routes on.
func PeekTerminal(raw []byte) (id.TerminalID, bool) {
	msg, err := decodeMessage(raw)
	if err != nil {
		return id.TerminalID{}, false
	}
	return msg.term, true
}
