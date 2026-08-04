// The receiving half: collecting fragments into a message, and saying which
// ones are missing.
//
// Three properties this has that the reassembler it replaces does not, each
// of which is a way a broadcast segment fills memory that a LAN does not:
//
//   - a transfer that stops making progress is EVICTED, so one lost fragment
//     does not pin a buffer for the life of the process
//   - transfers are CAPPED per peer and in total bytes, so one neighbour
//     cannot hold as much of our memory as they care to send
//   - a completed transfer is REMEMBERED for a while, so a re-delivery is
//     recognised instead of reassembled a second time
//
// And one it has that no per-fragment check can provide: the reassembled
// message is checked against the digest the sender put in every DATA frame.
// Fragments that were each individually fine can still assemble into
// something that was never sent.
package radiotransfer

import (
	"errors"
	"fmt"
	"time"
)

// Reason codes carried by CANCEL, so a sender learns WHY rather than only
// that something stopped.
const (
	ReasonUnspecified uint64 = 0
	ReasonTooLarge    uint64 = 1
	ReasonBusy        uint64 = 2
	ReasonDigest      uint64 = 3
	ReasonTimeout     uint64 = 4
)

// inbound is one transfer being reassembled.
type inbound struct {
	id       TransferID
	peer     string
	count    int
	total    int
	digest   [DigestLen]byte
	stream   uint64
	chunks   map[int][]byte
	bytes    int
	lastSeen time.Time
	// sackDue is when this transfer's SACK may be sent. Zero means none is
	// pending. A receiver that answers the instant a window fills makes every
	// receiver on the segment answer at once.
	sackDue time.Time
	// gen is the highest burst number heard on this transfer's DATA, echoed
	// in every SACK so the sender can tell a report from a history.
	gen uint64
	// sackBase is the window a pending SACK describes.
	sackBase int
}

// Receiver reassembles transfers from one segment.
//
// It is not safe for concurrent use; Session owns one and drives it from a
// single goroutine, which is what keeps the eviction and the accounting
// consistent without a lock in every path.
type Receiver struct {
	lim  Limits
	open map[TransferID]*inbound
	// done holds completed transfers for DedupTTL, WITH the digest and
	// count, because the tombstone answers questions now: a duplicate DATA
	// or a POLL is met with a complete-SACK, and the claim needs its proof.
	done  map[TransferID]doneRec
	bytes int
}

// NewReceiver creates one.
func NewReceiver(lim Limits) *Receiver {
	return &Receiver{
		lim:  lim.withDefaults(),
		open: map[TransferID]*inbound{},
		done: map[TransferID]doneRec{},
	}
}

// Delivered is a completed message, with what to say back about it.
type Delivered struct {
	Transfer TransferID
	Stream   uint64
	Message  []byte
	// From is the peer whose fragments assembled into this message.
	//
	// It is the carrier's own name for a neighbour and nothing more: it says
	// which radio the bytes arrived from, never who is holding it. Anything
	// that needs to know WHO must check a signature. What it does buy is the
	// ability to answer the right radio — without it a reply can only be
	// broadcast, and a peer link cannot exist at all.
	From RadioAddress
}

// doneRec is what a completed transfer leaves behind.
type doneRec struct {
	until  time.Time
	digest [DigestLen]byte
	count  uint64
}

// completeSACK is the tombstone's answer: the whole claim of a COMMIT, in a
// frame that can be repeated as often as it is asked for.
func (d doneRec) completeSACK(id TransferID, gen uint64) *Frame {
	width := int(d.count)
	bitmap := make([]byte, (width+7)/8)
	for i := range width {
		SetBit(bitmap, i)
	}
	return &Frame{Kind: KindSACK, Transfer: id, Count: d.count, Base: 0,
		Bitmap: bitmap, Generation: gen, Digest: d.digest, Reassembled: true}
}

// ErrAlreadyDelivered marks a transfer this receiver has already completed.
// It is not a failure — a sender that missed our COMMIT is behaving
// correctly by offering the message again — but the message must not be
// delivered upward twice.
var ErrAlreadyDelivered = errors.New("radiotransfer: already delivered")

// Accept folds one DATA frame in.
//
// It returns the message when the transfer completes and its digest matches,
// and nil while it is still incomplete. A frame that cannot be accepted at
// all yields a CANCEL to send back, so the sender stops spending airtime on
// something this receiver will never take.
func (r *Receiver) Accept(peer string, f *Frame, now time.Time) (*Delivered, *Frame, error) {
	if f.Kind != KindData {
		return nil, nil, fmt.Errorf("radiotransfer: %s is not a DATA frame", f.Kind)
	}
	r.evict(now)

	if rec, already := r.done[f.Transfer]; already {
		// A duplicate of a COMPLETED transfer buys a complete-SACK, not
		// another COMMIT. The COMMIT is one frame sent once; when it is
		// lost, every duplicate the sender pays for must buy back a claim
		// that can be repeated — measured live, 73 COMMITs went out and one
		// was heard, while the sender resent the last fragment for minutes.
		return nil, rec.completeSACK(f.Transfer, f.Generation), ErrAlreadyDelivered
	}

	in, open := r.open[f.Transfer]
	if !open {
		if int(f.Count) > r.lim.MaxFragmentsPerTransfer ||
			int(f.Total) > r.lim.MaxMessageBytes {
			return nil, cancelFrame(f.Transfer, ReasonTooLarge), nil
		}
		if r.peerCount(peer) >= r.lim.MaxInflightTransfersPerPeer ||
			r.bytes+int(f.Total) > r.lim.MaxInflightBytesPerSegment {
			// Refused for room, not for content. The sender may try later,
			// and saying so is cheaper for both than letting it retry into
			// silence for the whole round budget.
			return nil, cancelFrame(f.Transfer, ReasonBusy), nil
		}
		in = &inbound{
			id: f.Transfer, peer: peer, count: int(f.Count), total: int(f.Total),
			digest: f.Digest, stream: f.Stream, chunks: map[int][]byte{},
		}
		r.open[f.Transfer] = in
	}
	// A transfer's shape is fixed by its first frame. A later frame claiming
	// a different count or digest under the same id is either a collision or
	// somebody splicing; either way it is not part of this transfer.
	if int(f.Count) != in.count || f.Digest != in.digest || f.Stream != in.stream {
		return nil, nil, fmt.Errorf("radiotransfer: transfer %s changed shape "+
			"mid-flight", f.Transfer.Short())
	}

	in.lastSeen = now
	if f.Generation > in.gen {
		in.gen = f.Generation
	}
	if _, dup := in.chunks[int(f.Index)]; !dup {
		in.chunks[int(f.Index)] = append([]byte(nil), f.Chunk...)
		in.bytes += len(f.Chunk)
		r.bytes += len(f.Chunk)
	}
	// Arm a SACK for the window this fragment belongs to — and RE-ARM it on
	// every frame, pushing the timer back, so it fires after the SENDER
	// FALLS SILENT rather than once per frame. The arm-once rule produced
	// twenty-nine reports of one sixteen-fragment window, because with
	// frames spaced wider than the delay every fragment restarted the drip.
	// An EOB overrides the wait entirely: the sender has said it is
	// listening NOW, so the answer goes out now (the overheard-SACK
	// suppression still keeps a segment of many receivers from answering in
	// chorus).
	base := (int(f.Index) / r.lim.Window) * r.lim.Window
	if true {
		in.sackBase = base
		if f.EOB {
			in.sackDue = now
		} else {
			in.sackDue = now.Add(jitter(r.lim.SACKDelay, f.Transfer))
		}
	}

	if len(in.chunks) < in.count {
		return nil, nil, nil
	}
	msg, err := in.assemble()
	if err != nil {
		return nil, cancelFrame(f.Transfer, ReasonDigest), err
	}
	r.forget(f.Transfer)
	r.done[f.Transfer] = doneRec{until: now.Add(r.lim.DedupTTL),
		digest: in.digest, count: uint64(in.count)}
	return &Delivered{Transfer: f.Transfer, Stream: in.stream, Message: msg,
			From: RadioAddress(in.peer)},
		r.commitFrame(f.Transfer, in.digest), nil
}

// assemble joins the fragments and checks the whole against the digest.
func (in *inbound) assemble() ([]byte, error) {
	msg := make([]byte, 0, in.bytes)
	for i := range in.count {
		part, ok := in.chunks[i]
		if !ok {
			return nil, fmt.Errorf("radiotransfer: fragment %d missing at assembly", i)
		}
		msg = append(msg, part...)
	}
	if len(msg) != in.total {
		return nil, fmt.Errorf("radiotransfer: assembled %d bytes, the sender "+
			"said %d", len(msg), in.total)
	}
	if MessageDigest(msg) != in.digest {
		// Every fragment passed its own checksum and the whole is still not
		// what was sent. That is what the digest is for, and it is the one
		// failure a per-fragment check cannot reach.
		return nil, errors.New("radiotransfer: the reassembled message is not " +
			"the one that was sent")
	}
	return msg, nil
}

// DueSACKs returns the SACK frames whose delay has elapsed, and clears them.
// SACKDue is a SACK that is ready to send, WITH the peer it belongs to.
//
// The peer travels with the frame because a SACK is an answer to one sender,
// and the caller has no other way to know which. While the carrier discarded
// the destination this did not matter; now that it does not, sending every due
// SACK to whoever happened to speak last would answer the wrong radio.
type SACKDue struct {
	Peer  string
	Frame *Frame
}

func (r *Receiver) DueSACKs(now time.Time) []SACKDue {
	var out []SACKDue
	for _, in := range r.open {
		if in.sackDue.IsZero() || now.Before(in.sackDue) {
			continue
		}
		out = append(out, SACKDue{Peer: in.peer, Frame: in.sack()})
		in.sackDue = time.Time{}
	}
	return out
}

// Poll answers "what do you hold of this transfer, right now".
//
// An open transfer arms its SACK for immediate sending; a completed one
// answers from the tombstone with the full claim; an unknown one is met with
// silence — there is nothing true to say about it, and the sender's next
// DATA will re-establish it.
func (r *Receiver) Poll(id TransferID, gen uint64, now time.Time) *Frame {
	if rec, done := r.done[id]; done {
		return rec.completeSACK(id, gen)
	}
	if in, open := r.open[id]; open {
		in.sackDue = now
	}
	return nil
}

// NoteOverheard suppresses our own pending SACK when another receiver has
// already asked for the same thing.
//
// On a broadcast segment every receiver hears every DATA frame, so without
// this they all answer at once — an ACK storm on a carrier where one packet
// costs two seconds. The pattern is the one bridge/wake.go already uses: one
// outstanding question per destination, and whoever asks first asks for
// everybody.
//
// The suppression is deliberately narrow. It applies only when the overheard
// SACK covers the SAME WINDOW and reports no fewer holes than ours would — a
// SACK asking for less is not our question, and staying silent behind it
// would lose the fragments only we are missing.
func (r *Receiver) NoteOverheard(f *Frame) {
	if f.Kind != KindSACK {
		return
	}
	in, ok := r.open[f.Transfer]
	if !ok || in.sackDue.IsZero() || int(f.Base) != in.sackBase {
		return
	}
	for i := in.sackBase; i < in.count && i < in.sackBase+r.lim.Window; i++ {
		_, weHave := in.chunks[i]
		theyHave := HasBit(f.Bitmap, i-in.sackBase)
		if !weHave && theyHave {
			return // they are not asking for something we still need
		}
	}
	in.sackDue = time.Time{}
}

// sack describes which fragments of the current window arrived.
func (in *inbound) sack() *Frame {
	width := min(in.count-in.sackBase, 8*MaxBitmapBytes)
	bitmap := make([]byte, (width+7)/8)
	for i := range width {
		if _, ok := in.chunks[in.sackBase+i]; ok {
			SetBit(bitmap, i)
		}
	}
	return &Frame{
		Kind: KindSACK, Transfer: in.id,
		Count: uint64(in.count), Base: uint64(in.sackBase), Bitmap: bitmap,
		Generation: in.gen,
	}
}

func (r *Receiver) commitFrame(id TransferID, digest [DigestLen]byte) *Frame {
	return &Frame{Kind: KindCommit, Transfer: id, Digest: digest}
}

func cancelFrame(id TransferID, reason uint64) *Frame {
	return &Frame{Kind: KindCancel, Transfer: id, Reason: reason}
}

// evict drops transfers that have stopped making progress, and forgets
// completed ids past their TTL.
//
// "No progress" rather than "old": every fragment that arrives pushes a
// transfer forward, so a slow transfer that is still arriving is never thrown away. Only
// silence ages one out.
func (r *Receiver) evict(now time.Time) {
	for id, in := range r.open {
		if now.Sub(in.lastSeen) > r.lim.ReassemblyTimeout {
			r.forget(id)
		}
	}
	for id, rec := range r.done {
		if now.After(rec.until) {
			delete(r.done, id)
		}
	}
}

func (r *Receiver) forget(id TransferID) {
	if in, ok := r.open[id]; ok {
		r.bytes -= in.bytes
		delete(r.open, id)
	}
}

// Cancel drops a transfer a sender abandoned.
func (r *Receiver) Cancel(id TransferID) { r.forget(id) }

func (r *Receiver) peerCount(peer string) int {
	n := 0
	for _, in := range r.open {
		if in.peer == peer {
			n++
		}
	}
	return n
}

// Inflight reports what is being held, for a status line that can say why a
// segment stopped accepting rather than only that it did.
func (r *Receiver) Inflight() (transfers, bytes int) { return len(r.open), r.bytes }

// jitter spreads answers deterministically from the transfer id, so a test
// sees the same schedule twice and two receivers holding different transfers
// still differ.
func jitter(d time.Duration, id TransferID) time.Duration {
	if d <= 0 {
		return 0
	}
	var v uint64
	for _, b := range id[:8] {
		v = v<<8 | uint64(b)
	}
	return time.Duration(v % uint64(d))
}
