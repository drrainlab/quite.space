package node

// DR-1 — DELIVERY RECEIPTS: the neutral middle between silence and
// surveillance. A receipt is a machine's statement about a machine:
//
//	"device D holds author A's chain in space T up to position P",
//	signed by D, put into A's own per-space mailbox.
//
// It never says a person LOOKED — only that a device has the bytes,
// which is exactly what the sender's own retry loop wants to know and
// nothing more. Read-state stays where ADR-017 put it: on the reader's
// device, never on the wire. The receipt rides the bundle key the old
// decoders skip (ADR-009), so a pre-DR-1 node drains it as an empty
// bundle and loses nothing.
//
// SEND is gated by the person's switch (Settings.DeliveryReceipts,
// default on — it is machine state, and the switch exists for the
// person who wants their devices mute anyway). RECEIVE is always on:
// refusing to LEARN what somebody signed for holding protects nobody.
//
// The table this fills — ks.Delivered — is max-merged and clamped: a
// peer cannot "confirm" frames that do not exist, and a replayed old
// receipt cannot lower what a newer one proved.

import (
	"errors"
	"log"
	"time"

	"crypto/ed25519"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/bundle"
	"github.com/drrainlab/quiet_places/transports/relay"
)

const (
	receiptVersion = 1
	receiptFields  = 5
	receiptSigCtx  = "qp-delivery-receipt-v0:"
)

type deliveryReceipt struct {
	Terminal  id.TerminalID
	Author    id.DeviceID // whose chain the statement is about
	Pos       uint64      // ContiguousUntil the receiptor holds
	Receiptor id.DeviceID
	Signature []byte
}

func encodeReceiptBody(rc *deliveryReceipt) []byte {
	var buf []byte
	buf = codec.AppendArray(buf, receiptFields+1)
	buf = codec.AppendUint(buf, receiptVersion)
	buf = codec.AppendBytes(buf, rc.Terminal[:])
	buf = codec.AppendBytes(buf, rc.Author[:])
	buf = codec.AppendUint(buf, rc.Pos)
	buf = codec.AppendBytes(buf, rc.Receiptor[:])
	return buf
}

func signReceipt(rc *deliveryReceipt, key ed25519.PrivateKey) {
	rc.Signature = ed25519.Sign(key, append([]byte(receiptSigCtx), encodeReceiptBody(rc)...))
}

func encodeSignedReceipt(rc *deliveryReceipt) []byte {
	buf := encodeReceiptBody(rc)
	return codec.AppendBytes(buf, rc.Signature)
}

func decodeReceipt(data []byte) (*deliveryReceipt, error) {
	bad := errors.New("node: malformed delivery receipt")
	d := codec.NewDecoder(data)
	n, err := d.ReadArray()
	if err != nil || n < receiptFields+1 {
		return nil, bad
	}
	v, err := d.ReadUint()
	if err != nil || v != receiptVersion {
		return nil, bad
	}
	rc := &deliveryReceipt{}
	raw, err := d.ReadBytes()
	if err != nil || len(raw) != len(rc.Terminal) {
		return nil, bad
	}
	copy(rc.Terminal[:], raw)
	if raw, err = d.ReadBytes(); err != nil || len(raw) != len(rc.Author) {
		return nil, bad
	}
	copy(rc.Author[:], raw)
	if rc.Pos, err = d.ReadUint(); err != nil {
		return nil, bad
	}
	if raw, err = d.ReadBytes(); err != nil || len(raw) != len(rc.Receiptor) {
		return nil, bad
	}
	copy(rc.Receiptor[:], raw)
	if rc.Signature, err = d.ReadBytes(); err != nil {
		return nil, bad
	}
	for k := receiptFields + 1; k < n; k++ {
		if err := d.SkipItem(); err != nil {
			return nil, bad
		}
	}
	return rc, nil
}

func (r *Runtime) receiptsEnabled() bool {
	s := r.GetSettings()
	return s.DeliveryReceipts == nil || *s.DeliveryReceipts
}

// receiptState tracks, in memory only, the highest position already
// receipted per (space, author device). Losing it on restart merely
// resends one receipt per chain — max-merge makes the repeat free.
type receiptState struct {
	sent map[id.TerminalID]map[id.DeviceID]uint64
	// pending holds receipts that arrived BEFORE their receiptor's chain
	// did (a joiner's receipt can outrun the joiner's own first frames).
	// Temporary failure is not permanent refusal: they are re-judged on
	// the heartbeat until the chain shows up or the cap pushes them out.
	pending map[id.TerminalID][][]byte
}

const maxPendingReceipts = 64 // per space; oldest falls out first

// sendReceipts runs on the sync heartbeat: for every relay-permitted
// space, tell each author device (not this one, not a sibling of this
// principal's own hand — siblings converge through the log itself and a
// receipt would only echo it) how much of its chain this device now
// holds. One Put per (space, author) that GREW; quiet chains cost zero.
// rejudgePendingReceipts re-runs the parked receipts (see installReceipts):
// by now the chains they were waiting for may have arrived.
func (r *Runtime) rejudgePendingReceipts() {
	r.mu.Lock()
	if r.receipts == nil || len(r.receipts.pending) == 0 {
		r.mu.Unlock()
		return
	}
	parked := r.receipts.pending
	r.receipts.pending = map[id.TerminalID][][]byte{}
	r.mu.Unlock()
	for tid, q := range parked {
		r.installReceipts(tid, q)
	}
}

// receiptItem is one receipt this device owes: "I hold author dev's chain
// in space tid up to pos", signed, in the bytes installReceipts reads.
type receiptItem struct {
	tid    id.TerminalID
	dev    id.DeviceID
	pos    uint64
	signed []byte
}

// receiptsOwed is the pure half of sending receipts (LT-4 S4): every
// chain that grew past what this device last receipted, signed. With only
// set, just those spaces — the arrival path asks for what a pull touched.
func (r *Runtime) receiptsOwed(only map[id.TerminalID]struct{}) []receiptItem {
	r.rejudgePendingReceipts()
	if !r.receiptsEnabled() {
		return nil
	}
	var out []receiptItem
	r.mu.Lock()
	if r.receipts == nil {
		r.receipts = &receiptState{
			sent:    map[id.TerminalID]map[id.DeviceID]uint64{},
			pending: map[id.TerminalID][][]byte{},
		}
	}
	self := r.Device.ID
	for tid, meta := range r.ks.Spaces {
		if meta.LocalOnly {
			continue
		}
		if only != nil {
			if _, ok := only[tid]; !ok {
				continue
			}
		}
		st, ok := r.spaces[tid]
		if !ok || st.space == nil || st.space.Log == nil {
			continue
		}
		for _, ch := range st.space.Log.Summary() {
			if ch.Device == self || ch.ContiguousUntil == 0 {
				continue
			}
			if r.receipts.sent[tid][ch.Device] >= ch.ContiguousUntil {
				continue
			}
			rc := &deliveryReceipt{
				Terminal: tid, Author: ch.Device,
				Pos: ch.ContiguousUntil, Receiptor: self,
			}
			signReceipt(rc, r.Device.SignKey())
			out = append(out, receiptItem{tid: tid, dev: ch.Device, pos: ch.ContiguousUntil,
				signed: encodeSignedReceipt(rc)})
		}
	}
	r.mu.Unlock()
	return out
}

// deliverReceipts hands owed receipts to their authors (LT-4 S4).
//
// ON ARRIVAL (arrival = true — a pull or a LAN batch just applied frames):
// an author live on a local link gets its receipt OVER THAT LINK, never a
// relay copy (t6's promise: the relay carries nothing for the room's local
// members); everybody else gets a Put into their mailbox on the OUTBOX
// lane, so ✓✓ follows the other phone's display by seconds, not by a
// cycle. FROM THE CYCLE (arrival = false): the net under the arrival path,
// on the control lane, exactly as before this slice.
//
// Every relay receipt is one Put into the author's own mailbox — which
// rings their doorbell. That is one wake of the sender's phone a second
// or so after they typed; the arrival debounce (receiptDebounce) keeps a
// burst of messages to one receipt.
func (r *Runtime) deliverReceipts(items []receiptItem, arrival bool) {
	if len(items) == 0 {
		return
	}
	now := uint64(time.Now().Unix())
	expires := now + uint64(DefaultRelayTTL/time.Second)
	conn := r.connectivity()
	// ONLY WHILE THE RELAY IS SWITCHED ON — the doorbell's lesson, again.
	// The arrival path runs whenever frames land, over any transport; a
	// device whose relay is off must not answer a LAN arrival by putting
	// receipts into other members' mailboxes (t6: carol's mailbox grew
	// while she was on the wire, and it was bob's receipt for her chain).
	relayOK := !arrival || r.relaySyncArmed()
	for _, o := range items {
		delivered := false
		if arrival {
			r.mu.Lock()
			if l, ok := r.lanPeers[o.dev]; ok && l != nil {
				if closed, _ := l.Closed(); !closed {
					if st := r.spaces[o.tid]; st != nil && st.eng != nil {
						if err := st.eng.SendReceipts(l, [][]byte{o.signed}); err == nil {
							delivered = true
						}
					}
				}
			}
			r.mu.Unlock()
			if !delivered && r.lanPeerDevice(o.dev) {
				continue // on the wire but the send failed: the cycle's net, never the relay from here
			}
		}
		if !delivered {
			if !relayOK || !conn.allows(TransportRelay, o.tid) {
				continue
			}
			// The author's best dialable stated relay; this node's own as
			// the courtesy otherwise — the one chooser every plane uses.
			ep, _ := r.courtesyRoute(o.dev)
			if ep == "" {
				continue
			}
			if _, yes := r.relayThrottled(ep); yes {
				continue
			}
			hint := relay.HintFor(o.tid, o.dev, relay.Bucket(now))
			body := bundle.EncodeReceipts(o.tid, [][]byte{o.signed})
			lane := r.withRelayControl
			if arrival {
				lane = r.withRelayOutbox
			}
			err := lane(ep, func(client *relay.Client) error {
				_, err := client.Put(hint, expires, body)
				return err
			})
			if err != nil {
				continue // the chain will still be ahead next tick; retried then
			}
			r.receiptPuts.Add(1)
			delivered = true
		}
		r.mu.Lock()
		if r.receipts.sent[o.tid] == nil {
			r.receipts.sent[o.tid] = map[id.DeviceID]uint64{}
		}
		if r.receipts.sent[o.tid][o.dev] < o.pos {
			r.receipts.sent[o.tid][o.dev] = o.pos
		}
		r.mu.Unlock()
	}
}

// sendReceipts is the cycle's net: whatever the arrival path did not
// deliver, on the control lane.
func (r *Runtime) sendReceipts() {
	r.deliverReceipts(r.receiptsOwed(nil), false)
}

// receiptDebounce is how long after the last applied frame the arrival
// receipts leave: a burst of twenty messages is one receipt, not twenty
// Puts and twenty wakes of the author's phone.
const receiptDebounce = time.Second

// noteArrival records that frames were just applied in tid — from a relay
// pull or a LAN batch — and arms the arrival receipts. Cheap and lock-light
// on purpose: callers hold r.mu.
func (r *Runtime) noteArrival(tid id.TerminalID) {
	r.arrivalMu.Lock()
	if r.arrivals == nil {
		r.arrivals = map[id.TerminalID]struct{}{}
	}
	r.arrivals[tid] = struct{}{}
	if r.arrivalTimer == nil {
		r.arrivalTimer = time.AfterFunc(receiptDebounce, r.flushArrivalReceipts)
	}
	r.arrivalMu.Unlock()
}

func (r *Runtime) flushArrivalReceipts() {
	r.arrivalMu.Lock()
	set := r.arrivals
	r.arrivals = nil
	r.arrivalTimer = nil
	r.arrivalMu.Unlock()
	if r.stopped() || len(set) == 0 {
		return
	}
	r.deliverReceipts(r.receiptsOwed(set), true)
}

// installReceipts folds the receipts one drained bundle carried. Each is
// verified on its own signature and gated exactly like knowledge should
// be: only statements about THIS device's chain matter here (the mailbox
// was ours), only from a device that exists in the space's log, never
// from ourselves, and never beyond what the chain actually holds.
func (r *Runtime) installReceipts(tid id.TerminalID, receipts [][]byte) {
	if len(receipts) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.installReceiptsLocked(tid, receipts)
}

// installReceiptsLocked is installReceipts for a caller that holds r.mu —
// the LAN pump, whose engine callbacks run under it.
func (r *Runtime) installReceiptsLocked(tid id.TerminalID, receipts [][]byte) {
	if len(receipts) == 0 {
		return
	}
	st, ok := r.spaces[tid]
	if !ok || st.space == nil || st.space.Log == nil {
		return
	}
	self := r.Device.ID
	myTip, _, _ := st.space.Log.ChainTip(self)
	changed := false
	for _, raw := range receipts {
		rc, err := decodeReceipt(raw)
		if err != nil {
			continue
		}
		if rc.Terminal != tid || rc.Author != self || rc.Receiptor == self {
			continue
		}
		// The receiptor must exist in this space's log — a voice the
		// space has actually heard, not merely a well-formed signature.
		// Not yet is NOT never (a joiner's receipt can outrun its own
		// first frames): park it for the heartbeat to re-judge.
		if !chainKnown(st.space.Log, rc.Receiptor) {
			if r.receipts == nil {
				r.receipts = &receiptState{
					sent:    map[id.TerminalID]map[id.DeviceID]uint64{},
					pending: map[id.TerminalID][][]byte{},
				}
			}
			q := append(r.receipts.pending[tid], append([]byte(nil), raw...))
			if len(q) > maxPendingReceipts {
				q = q[len(q)-maxPendingReceipts:]
			}
			r.receipts.pending[tid] = q
			continue
		}
		pub := ed25519.PublicKey(rc.Receiptor[:])
		unsigned := encodeReceiptBody(rc)
		if !ed25519.Verify(pub, append([]byte(receiptSigCtx), unsigned...), rc.Signature) {
			log.Printf("node: a delivery receipt failed verification")
			continue
		}
		pos := rc.Pos
		if pos > myTip {
			pos = myTip // nobody holds frames that do not exist
		}
		if pos == 0 {
			continue
		}
		if r.ks.Delivered == nil {
			r.ks.Delivered = map[id.TerminalID]map[id.DeviceID]uint64{}
		}
		if r.ks.Delivered[tid] == nil {
			r.ks.Delivered[tid] = map[id.DeviceID]uint64{}
		}
		if had := r.ks.Delivered[tid][rc.Receiptor]; pos < had && r.offerBook != nil {
			// A receipt for LESS than this device once signed for: it was
			// restored from an older backup and holds less than the book
			// says its mailboxes were given. Everything to it is re-laid.
			r.offerBook.forgetDevice(rc.Receiptor)
		}
		if r.ks.Delivered[tid][rc.Receiptor] < pos {
			r.ks.Delivered[tid][rc.Receiptor] = pos
			changed = true
			// The timeline: a device outside this principal holds these.
			if cert, ok := r.ident.certificateFor(rc.Receiptor); !ok || cert.Principal != r.PrincipalID {
				r.lat.delivered(tid, pos, rc.Receiptor)
			}
		}
	}
	if changed {
		if err := r.saveKeystore(); err != nil {
			log.Printf("node: delivered table not persisted: %v", err)
		}
	}
}

func chainKnown(l *eventlog.Log, dev id.DeviceID) bool {
	_, _, ok := l.ChainTip(dev)
	return ok
}

// deliveryStatusLocked is the projection's one-word answer for one own
// frame. Caller holds r.mu. Spaces the relay never carries get "" — a
// checkmark ladder for a room that goes nowhere would be theatre.
func (r *Runtime) deliveryStatusLocked(tid id.TerminalID, eid id.EventID, seq uint64) string {
	if r.ks.Spaces[tid].LocalOnly {
		return ""
	}
	if r.deliveredByForeignLocked(tid, seq) {
		return "delivered"
	}
	// The middle rung comes from the trust engine — the one place that
	// records "a relay accepted these bytes" at the moment it happened.
	// (The custody ledger is the wrong witness here: its intents track
	// radio/gateway responsibility and stay live for ordinary relay
	// mail, which read as "still sending" forever.) Trust records are
	// in-memory, so after a restart un-receipted history drops back to
	// the dot — pessimistic, never false.
	if st, ok := r.spaces[tid]; ok && st.space != nil && st.space.Trust != nil {
		if st.space.Trust.Delivery(eid, tid).Level >= claims.DeliveryAcceptedByRelay {
			return "relayed"
		}
	}
	// The persisted watermark (LT-3): own frames are pushed in chain
	// order, so everything up to the highest position a relay took has
	// been taken — and stays taken across a restart.
	if seq > 0 && r.ks.Relayed[tid] >= seq {
		return "relayed"
	}
	return "sent"
}

// deliveredByForeignLocked answers the UI's question for ONE own frame:
// has any device OUTSIDE this principal signed for holding it? Sibling
// receipts are excluded — "delivered" must mean it left the person's
// own hand, or the mac's checkmark would light up because the person's
// own phone synced. Caller holds r.mu.
func (r *Runtime) deliveredByForeignLocked(tid id.TerminalID, seq uint64) bool {
	for dev, pos := range r.ks.Delivered[tid] {
		if pos < seq {
			continue
		}
		if cert, ok := r.ident.certificateFor(dev); ok && cert.Principal == r.PrincipalID {
			continue // a sibling of this same person
		}
		return true
	}
	return false
}
