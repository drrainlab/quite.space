package bridge

import (
	"bytes"
	"testing"
	"time"

	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/loopback"
)

// wireOf drains a carrier end and returns the raw bytes it saw, plus every
// reassembled message.
func wireOf(t *testing.T, ep *loopback.End) (raw []byte, msgs [][]byte) {
	t.Helper()
	reasm := kernelsync.NewReassembler()
	for _, pkt := range ep.Poll() {
		raw = append(raw, pkt...)
		m, err := reasm.Feed(pkt)
		if err != nil || m == nil {
			continue
		}
		msgs = append(msgs, m)
	}
	return raw, msgs
}

// What a bridge announces must be exactly what a node would announce about
// the same chains — no richer. The bridge sees more than any single node
// does (it watches a whole segment), so the temptation to put more in the
// advert is real, and the cost would be paid by everyone listening.
//
// This test pins the field set. It deliberately does NOT claim the summary
// is anonymous: see TestSummaryExposureIsKnownAndBounded below for what is
// actually on the air, and ADR-015 for why that is a protocol-level
// decision rather than something the bridge can fix alone.
func TestBridgeSummaryAnnouncesNoMoreThanANodeWould(t *testing.T) {
	var dest id.TerminalID
	dest[0] = 0xF1
	pair := loopback.NewPair(loopback.Faults{Seed: 31})
	b := testBridge(t, pair.B, "127.0.0.1:1", []Subscription{serving(dest, 0x91, 0x92)})
	now := time.Now()

	// Carry two frames from one author so the ledger has something to say.
	var prev *id.EventID
	for seq := uint64(1); seq <= 2; seq++ {
		f := mkFrame(t, dest, 0x91, seq, prev, "carried")
		e := id.EventIDOf(f)
		prev = &e
		if err := sendWrapped(t, pair.A, kernelsync.EncodeFramesMessage(dest, [][]byte{f})); err != nil {
			t.Fatal(err)
		}
		b.PumpRadio(now)
	}
	pair.A.Poll() // discard the ACKs; we want the summary alone
	if sent := b.WakeRadio(now); sent != 1 {
		t.Fatalf("no announcement to inspect: %d", sent)
	}
	_, msgs := wireOf(t, pair.A)

	var summaries int
	for _, m := range msgs {
		term, chains, ok := kernelsync.ExtractSummaryChains(m)
		if !ok {
			continue
		}
		summaries++
		if term != dest {
			t.Fatalf("summary announces a terminal the bridge was not asked about: %x", term)
		}
		// Exactly the authors it actually carried, and nothing else. A
		// principal id, a second device, or a destination the bridge merely
		// overheard would all show up here.
		if len(chains) != 1 {
			t.Fatalf("summary announces %d chains, carried 1", len(chains))
		}
		if chains[0].Device != deviceOf(0x91) {
			t.Fatalf("summary names a device that authored nothing it carried")
		}
		if chains[0].ContiguousUntil != 2 {
			t.Fatalf("summary claims height %d, carried 2", chains[0].ContiguousUntil)
		}
	}
	if summaries != 1 {
		t.Fatalf("expected one summary on the air, saw %d", summaries)
	}
}

// The exposure that IS on the air, pinned deliberately so nobody has to
// rediscover it.
//
// A sync summary — from a node or from a bridge, the wire is identical —
// carries the raw 32-byte terminal id and the raw device public key of
// every author chain it announces. That is how the format has worked since
// M1.7; the bridge did not introduce it and cannot remove it alone, because
// a summary a node cannot parse is a summary that wakes nobody.
//
// Two consequences worth stating plainly:
//
//   - Content stays encrypted, but a listener on the segment learns WHICH
//     space is active, WHICH devices author in it, and how fast each chain
//     grows. That is an activity graph.
//   - TN-2B's id-table does NOT help here. It interns ids found in signed
//     frames; scanIDs returns nothing for a summary, so summaries take the
//     stateless path and the ids go out raw even under the table profile.
//
// The bridge makes this MORE regular, not different in kind: it announces
// on a schedule where before only nodes did. Fixing it means changing the
// summary message for every peer — keyed or table-scoped hints — which is a
// protocol decision for a later gate, not a bridge change.
func TestSummaryExposureIsKnownAndBounded(t *testing.T) {
	var dest id.TerminalID
	dest[0] = 0xF2
	pair := loopback.NewPair(loopback.Faults{Seed: 32})
	b := testBridge(t, pair.B, "127.0.0.1:1", []Subscription{serving(dest, 0x93, 0x94)})
	now := time.Now()

	f := mkFrame(t, dest, 0x93, 1, nil, "carried")
	if err := sendWrapped(t, pair.A, kernelsync.EncodeFramesMessage(dest, [][]byte{f})); err != nil {
		t.Fatal(err)
	}
	b.PumpRadio(now)
	pair.A.Poll()
	b.WakeRadio(now)
	raw, _ := wireOf(t, pair.A)

	author := deviceOf(0x93)
	if !bytes.Contains(raw, dest[:]) {
		t.Fatal("the terminal id is no longer on the wire in a summary — if " +
			"that is intentional, this test and ADR-015 both need updating, " +
			"and every peer's summary parser with them")
	}
	if !bytes.Contains(raw, author[:]) {
		t.Fatal("the author device key is no longer on the wire in a summary " +
			"— same: a deliberate change here is a protocol change")
	}

	// What must NOT be there: anything the bridge learned but was never
	// asked to announce. The internet-side mailbox is the clearest case —
	// it is operator-provisioned routing capability, it appears in no sync
	// message, and leaking it would hand a listener the far side of the
	// boundary for free.
	internet := deviceOf(0x94)
	if bytes.Contains(raw, internet[:]) {
		t.Fatal("the internet-side mailbox leaked onto the radio segment")
	}
	if bytes.Contains(raw, []byte(b.cfg.DataDir)) {
		t.Fatal("the custody store path leaked onto the radio segment")
	}
	if bytes.Contains(raw, b.CustodianPub()) {
		t.Fatal("an announcement carried the custodian key: presence belongs " +
			"in the RB-2 beacon, where it is signed and rate-limited")
	}
}
