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
	// sackBase is the window a pending SACK describes.
	sackBase int
}

// Receiver reassembles transfers from one segment.
//
// It is not safe for concurrent use; Session owns one and drives it from a
// single goroutine, which is what keeps the eviction and the accounting
// consistent without a lock in every path.
type Receiver struct {
	lim   Limits
	open  map[TransferID]*inbound
	done  map[TransferID]time.Time // completed, kept for DedupTTL
	bytes int
}

// NewReceiver creates one.
func NewReceiver(lim Limits) *Receiver {
	return &Receiver{
		lim:  lim.withDefaults(),
		open: map[TransferID]*inbound{},
		done: map[TransferID]time.Time{},
	}
}

// Delivered is a completed message, with what to say back about it.
type Delivered struct {
	Transfer TransferID
	Stream   uint64
	Message  []byte
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

	if _, already := r.done[f.Transfer]; already {
		// Re-delivery. Say COMMIT again rather than staying silent: the
		// sender is here because it did not hear the first one, and silence
		// is what made it retry.
		return nil, r.commitFrame(f.Transfer, f.Digest), ErrAlreadyDelivered
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
	if _, dup := in.chunks[int(f.Index)]; !dup {
		in.chunks[int(f.Index)] = append([]byte(nil), f.Chunk...)
		in.bytes += len(f.Chunk)
		r.bytes += len(f.Chunk)
	}
	// Arm a SACK for the window this fragment belongs to, once. The delay is
	// what keeps several receivers from answering in the same instant, and
	// the pending base is what an overheard SACK is compared against.
	base := (int(f.Index) / r.lim.Window) * r.lim.Window
	if in.sackDue.IsZero() || base != in.sackBase {
		in.sackBase, in.sackDue = base, now.Add(jitter(r.lim.SACKDelay, f.Transfer))
	}

	if len(in.chunks) < in.count {
		return nil, nil, nil
	}
	msg, err := in.assemble()
	if err != nil {
		return nil, cancelFrame(f.Transfer, ReasonDigest), err
	}
	r.forget(f.Transfer)
	r.done[f.Transfer] = now.Add(r.lim.DedupTTL)
	return &Delivered{Transfer: f.Transfer, Stream: in.stream, Message: msg},
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
func (r *Receiver) DueSACKs(now time.Time) []*Frame {
	var out []*Frame
	for _, in := range r.open {
		if in.sackDue.IsZero() || now.Before(in.sackDue) {
			continue
		}
		out = append(out, in.sack())
		in.sackDue = time.Time{}
	}
	return out
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
	for id, until := range r.done {
		if now.After(until) {
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
