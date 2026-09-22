package sync

// DELIVERY RECEIPTS OVER A LIVE LINK (LT-4 S4). DR-1's signed receipt —
// "device D holds author A's chain up to P" — used to travel only in a
// relay bundle, so a peer reached over the LAN never lit the sender's ✓✓
// at all. The same signed bytes now ride the sync protocol as a message of
// their own. Verification stays where it was: the node's installReceipts
// checks the signature and the claim; this layer only carries.
//
// Append-only like the rest of the wire: a new message type and a new
// key. An older peer's Handle has no default arm and skips what it does
// not know, so a receipt sent to a 1.0.25 node is silently dropped there —
// that node's ✓✓ arrives the old way, with the relay cycle.

import (
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports"
)

const (
	// msgReceipts carries one or more signed delivery receipts.
	msgReceipts = 10
	// keyReceipts is an array of receipt byte strings.
	keyReceipts = 13
)

// EncodeReceiptsMessage wraps signed receipt bytes for a live peer.
func EncodeReceiptsMessage(terminal id.TerminalID, receipts [][]byte) []byte {
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, keyType)
	buf = codec.AppendUint(buf, msgReceipts)
	buf = codec.AppendUint(buf, keyTerminal)
	buf = codec.AppendBytes(buf, terminal[:])
	buf = codec.AppendUint(buf, keyReceipts)
	buf = codec.AppendArray(buf, len(receipts))
	for _, rc := range receipts {
		buf = codec.AppendBytes(buf, rc)
	}
	return buf
}

// SendReceipts hands signed receipts to a live peer on ep.
func (e *Engine) SendReceipts(ep transports.Endpoint, receipts [][]byte) error {
	if len(receipts) == 0 {
		return nil
	}
	return e.sendMsg(ep, EncodeReceiptsMessage(e.Log.Terminal, receipts))
}
