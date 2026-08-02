// What names a transfer, and why it is not a counter.
//
// This repository has already learned this lesson once and written it down.
// kernel/sync/frag.go explains at length why fragment stream ids start from a
// RANDOM base: a broadcast radio segment delivers every node's fragments to
// every other node's reassembler, nothing coordinates numbering between
// machines, and counters starting at zero everywhere made collision the
// DEFAULT for the first messages after boot — on exactly the medium where
// messages are large enough to fragment at all.
//
// Then transports/compact and transports/bridge each started their stream ids
// at zero anyway, which is how a lesson written in one file fails to reach the
// two files that needed it.
//
// So the invariant is stated here as a property of the type, and tested:
//
//	unpredictable · non-zero · never reused within dedup_ttl for one
//	segment key · generated afresh after every process restart
//
// Ninety-six random bits, so no persisted counter is needed to hold that at
// this width. A restart draws new ids that cannot collide with what a
// receiver still remembers — which is the case a counter gets wrong, because
// a counter restarts too.
package radiotransfer

import (
	"crypto/rand"
	"encoding/hex"
)

// TransferIDLen is 12 bytes — 96 bits.
const TransferIDLen = 12

// TransferID names one transfer between two radios.
type TransferID [TransferIDLen]byte

// NewTransferID draws a fresh id.
//
// It never returns the zero id: zero is what an uninitialised struct and a
// truncated frame both look like, and a value that means "unset" must not
// also be a legal name for something.
func NewTransferID() (TransferID, error) {
	var t TransferID
	for {
		if _, err := rand.Read(t[:]); err != nil {
			return TransferID{}, err
		}
		if !t.IsZero() {
			return t, nil
		}
	}
}

// IsZero reports the unset id.
func (t TransferID) IsZero() bool {
	for _, b := range t {
		if b != 0 {
			return false
		}
	}
	return true
}

func (t TransferID) String() string { return hex.EncodeToString(t[:]) }

// Short is the id as it appears in a log line: enough to tell two transfers
// apart while reading, never enough to be mistaken for the whole id.
func (t TransferID) Short() string { return hex.EncodeToString(t[:4]) }
