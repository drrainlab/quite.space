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
	"github.com/drrainlab/quiet_places/kernel/storage"
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

func (r *Runtime) sendReceipts() {
	r.rejudgePendingReceipts()
	if !r.receiptsEnabled() {
		return
	}
	type outItem struct {
		dev  id.DeviceID
		tid  id.TerminalID
		body []byte
		pos  uint64
		eps  []storage.Route
	}
	var out []outItem
	conn := r.connectivity() // read BEFORE r.mu: settings take the same lock
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
		st, ok := r.spaces[tid]
		if !ok || st.space == nil || st.space.Log == nil {
			continue
		}
		if !conn.allows(TransportRelay, tid) {
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
			out = append(out, outItem{
				dev: ch.Device, tid: tid,
				body: bundle.EncodeReceipts(tid, [][]byte{encodeSignedReceipt(rc)}),
				pos:  ch.ContiguousUntil,
				eps:  append([]storage.Route(nil), r.ks.PeerRoutes[ch.Device]...),
			})
		}
	}
	r.mu.Unlock()
	if len(out) == 0 {
		return
	}
	own := r.ResolvePersonalRelay()
	now := uint64(time.Now().Unix())
	expires := now + uint64(DefaultRelayTTL/time.Second)
	for _, o := range out {
		// The author's stated relay when the book holds one; this node's
		// own as the courtesy otherwise — the grants plane's exact rule.
		ep := own
		for _, rt := range o.eps {
			if rt.Transport == "relay" && rt.Endpoint != "" {
				ep = rt.Endpoint
				break
			}
		}
		if ep == "" {
			continue
		}
		if _, yes := r.relayThrottled(ep); yes {
			continue
		}
		hint := relay.HintFor(o.tid, o.dev, relay.Bucket(now))
		body := o.body
		err := r.withRelayControl(ep, func(client *relay.Client) error {
			_, err := client.Put(hint, expires, body)
			return err
		})
		if err != nil {
			continue // the chain will still be ahead next tick; retried then
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
		if r.ks.Delivered[tid][rc.Receiptor] < pos {
			r.ks.Delivered[tid][rc.Receiptor] = pos
			changed = true
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
	if r.ledger != nil {
		if _, live := r.ledger.Get(eid); live {
			return "sent"
		}
	}
	return "relayed"
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
