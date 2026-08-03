// Package sync implements Sync Protocol v0 (plan §17, M0.6): summary
// exchange, push of missing ranges, chunking, and fragmentation down to
// radio-scale MTUs. The protocol is idempotent and resumable by
// construction: any message can be lost, duplicated, or reordered and the
// state machine only ever converges (dedup lives in the event log).
package sync

import (
	"errors"
	"sort"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/transports"
)

// Message types.
const (
	msgSummary  = 1
	msgFrames   = 2
	msgBlobReq  = 3 // request encrypted blobs (asset manifests and chunks)
	msgBlobData = 4
)

// Message key table (append-only).
const (
	keyType     = 1
	keyTerminal = 2
	keyChains   = 3
	keyFrames   = 4
	keyHashes   = 5
	keyBlobs    = 6
	// keyAttempt carries the SENDER's responsibility token (RB-1).
	// Append-only: a decoder that does not know it skips it. It rides on
	// frames messages so a gateway can echo it in every receipt about the
	// custody it grants — see node.AttemptID for why the token has to come
	// from the sender rather than from a gateway or from anyone's clock.
	keyAttempt = 8
)

// Blob exchange limits (plan: resource limits).
const (
	MaxBlobIDsPerRequest = 64
	MaxBlobBytes         = 1 << 20
)

// BlobStore is the optional blob surface for asset exchange. Implemented by
// storage.Root; nil disables blob serving and fetching.
type BlobStore interface {
	PutBlob(data []byte) (id.Hash, error)
	GetBlob(h id.Hash) ([]byte, error)
	HasBlob(h id.Hash) bool
}

// Fragment key table (outer packet layer).
const (
	fragKeyStream = 1
	fragKeyIndex  = 2
	fragKeyTotal  = 3
	fragKeyChunk  = 4
)

// framesBudget bounds one frames-message before fragmentation.
const framesBudget = 32 * 1024

// minMTU is the smallest workable packet size (fragment overhead ~20 bytes).
const minMTU = 32

// Engine drives sync for one terminal's log over one endpoint.
type Engine struct {
	Log *eventlog.Log
	// OnApplied receives every event the sync engine lands in the log.
	OnApplied func(eventlog.Applied)

	// Blobs enables asset exchange. BlobAllowed answers "is this wire id
	// legitimately published in THIS space" — the engine is per-space, so
	// the check plus the terminal-id filter scope every request (plan §5:
	// a node must not become an oracle for arbitrary hashes).
	Blobs       BlobStore
	BlobAllowed func(h id.Hash) bool
	// OnBlobStored fires when a requested blob lands (fetch coordinator).
	OnBlobStored func(h id.Hash)

	// OnSent fires after a frames batch was handed to the endpoint —
	// the delivery-ladder hook for handed_to_transport (ADR-015 §5,
	// never more than transport-level custody: ADR-007).
	OnSent func(eventIDs []id.EventID)

	// AttemptToken is the sender's current responsibility token for this
	// space. When set it is stamped on every frames message the engine
	// emits — unchanged across fragmentation and across re-sends within one
	// attempt, because it identifies a HAND-OFF, not a packet. The node
	// rotates it only when responsibility comes back (custody withdrawn or
	// expired), and makes it durable BEFORE the first send that carries it.
	AttemptToken []byte

	// OnCustodyReceipt receives raw custodian-signed receipt bytes from a
	// bridge on this link. The NODE verifies against its pinned custodian
	// keys — the engine only routes (an unpinned receipt records nothing).
	OnCustodyReceipt func(receipt []byte)

	// pending holds requested wire ids awaiting delivery. The engine is not
	// safe for concurrent use; callers serialize (the node runtime holds
	// its own lock around every engine call).
	pending map[id.Hash]bool

	reasmSt *Reassembler
}

type reassembly struct {
	total  int
	chunks map[int][]byte
}

// NewEngine wraps a log for syncing.
func NewEngine(log *eventlog.Log) *Engine {
	return &Engine{Log: log, reasmSt: NewReassembler(), pending: map[id.Hash]bool{}}
}

// ---- Message encoding ----

func (e *Engine) encodeSummary() []byte {
	chains := e.Log.Summary()
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, keyType)
	buf = codec.AppendUint(buf, msgSummary)
	buf = codec.AppendUint(buf, keyTerminal)
	buf = codec.AppendBytes(buf, e.Log.Terminal[:])
	buf = codec.AppendUint(buf, keyChains)
	buf = codec.AppendArray(buf, len(chains))
	for _, c := range chains {
		buf = codec.AppendArray(buf, 2)
		buf = codec.AppendBytes(buf, c.Device[:])
		buf = codec.AppendUint(buf, c.ContiguousUntil)
	}
	return buf
}

func (e *Engine) encodeFrames(frames [][]byte) []byte {
	return encodeFramesWithAttempt(e.Log.Terminal, frames, e.AttemptToken)
}

func (e *Engine) encodeBlobReq(hashes []id.Hash) []byte {
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, keyType)
	buf = codec.AppendUint(buf, msgBlobReq)
	buf = codec.AppendUint(buf, keyTerminal)
	buf = codec.AppendBytes(buf, e.Log.Terminal[:])
	buf = codec.AppendUint(buf, keyHashes)
	buf = codec.AppendArray(buf, len(hashes))
	for _, h := range hashes {
		buf = codec.AppendBytes(buf, h[:])
	}
	return buf
}

func (e *Engine) encodeBlobData(blobs [][]byte) []byte {
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, keyType)
	buf = codec.AppendUint(buf, msgBlobData)
	buf = codec.AppendUint(buf, keyTerminal)
	buf = codec.AppendBytes(buf, e.Log.Terminal[:])
	buf = codec.AppendUint(buf, keyBlobs)
	buf = codec.AppendArray(buf, len(blobs))
	for _, b := range blobs {
		buf = codec.AppendBytes(buf, b)
	}
	return buf
}

type summary struct {
	terminal id.TerminalID
	chains   map[id.DeviceID]uint64
}

// message is one decoded sync message.
type message struct {
	msgType uint64
	term    id.TerminalID
	sum     *summary
	frames  [][]byte
	hashes  []id.Hash
	blobs   [][]byte
	receipt []byte
	attempt []byte
	beacon  []byte
}

func decodeMessage(data []byte) (*message, error) {
	d := codec.NewDecoder(data)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	msg := &message{}
	chains := map[id.DeviceID]uint64{}
	for {
		k, ok, e := m.Next()
		if e != nil {
			return nil, e
		}
		if !ok {
			break
		}
		switch k {
		case keyType:
			msg.msgType, err = d.ReadUint()
		case keyTerminal:
			var b []byte
			b, err = d.ReadBytes()
			if err == nil {
				if len(b) != id.Size {
					return nil, errors.New("sync: bad terminal id")
				}
				copy(msg.term[:], b)
			}
		case keyChains:
			var n int
			n, err = d.ReadArray()
			if err != nil {
				return nil, err
			}
			for range n {
				if _, err = d.ReadArray(); err != nil {
					return nil, err
				}
				var devBytes []byte
				devBytes, err = d.ReadBytes()
				if err != nil || len(devBytes) != id.Size {
					return nil, errors.New("sync: bad device id")
				}
				var until uint64
				until, err = d.ReadUint()
				if err != nil {
					return nil, err
				}
				var dev id.DeviceID
				copy(dev[:], devBytes)
				chains[dev] = until
			}
		case keyFrames:
			var n int
			n, err = d.ReadArray()
			if err != nil {
				return nil, err
			}
			for range n {
				var f []byte
				f, err = d.ReadBytes()
				if err != nil {
					return nil, err
				}
				msg.frames = append(msg.frames, append([]byte(nil), f...))
			}
		case keyHashes:
			var n int
			n, err = d.ReadArray()
			if err != nil {
				return nil, err
			}
			if n > MaxBlobIDsPerRequest {
				return nil, errors.New("sync: too many blob ids in request")
			}
			for range n {
				var b []byte
				b, err = d.ReadBytes()
				if err != nil || len(b) != id.Size {
					return nil, errors.New("sync: bad blob id")
				}
				var h id.Hash
				copy(h[:], b)
				msg.hashes = append(msg.hashes, h)
			}
		case keyBlobs:
			var n int
			n, err = d.ReadArray()
			if err != nil {
				return nil, err
			}
			for range n {
				var b []byte
				b, err = d.ReadBytes()
				if err != nil {
					return nil, err
				}
				if len(b) > MaxBlobBytes {
					return nil, errors.New("sync: blob over size limit")
				}
				msg.blobs = append(msg.blobs, append([]byte(nil), b...))
			}
		case keyBeacon:
			msg.beacon, err = d.ReadBytes()
		case keyReceipt:
			var b []byte
			b, err = d.ReadBytes()
			if err == nil {
				msg.receipt = append([]byte(nil), b...)
			}
		case keyAttempt:
			var b []byte
			b, err = d.ReadBytes()
			if err == nil {
				msg.attempt = append([]byte(nil), b...)
			}
		default:
			err = d.SkipItem()
		}
		if err != nil {
			return nil, err
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if msg.msgType == msgSummary {
		msg.sum = &summary{terminal: msg.term, chains: chains}
	}
	return msg, nil
}

// ---- Fragmentation (radio-scale MTUs, plan §19 T4) ----

func (e *Engine) fragment(msg []byte, mtu int) ([][]byte, error) {
	return FragmentStream(NextStreamID(), msg, mtu)
}

func fragHeader(stream uint64, index, total int) []byte {
	buf := codec.AppendMap(nil, 4)
	buf = codec.AppendUint(buf, fragKeyStream)
	buf = codec.AppendUint(buf, stream)
	buf = codec.AppendUint(buf, fragKeyIndex)
	buf = codec.AppendUint(buf, uint64(index))
	buf = codec.AppendUint(buf, fragKeyTotal)
	buf = codec.AppendUint(buf, uint64(total))
	return buf
}

// reassemble consumes a fragment packet; returns the whole message when
// complete. Duplicate fragments are no-ops.
func (e *Engine) reassemble(pkt []byte) ([]byte, error) {
	if e.reasmSt == nil {
		e.reasmSt = NewReassembler()
	}
	return e.reasmSt.Feed(pkt)
}

// ---- Protocol driver ----

// SendSummary announces our chain state to the peer.
func (e *Engine) SendSummary(ep transports.Endpoint) error {
	return e.sendMsg(ep, e.encodeSummary())
}

// SummarySize reports how many bytes the next summary will occupy.
//
// A metered link has to decide whether it can AFFORD a summary before it
// spends the air on one, and an estimate that drifts from the encoder is how
// an airtime budget quietly stops describing airtime. This encodes the same
// bytes SendSummary would, so the two cannot disagree.
func (e *Engine) SummarySize() int { return len(e.encodeSummary()) }

// ErrCarrierFull says the endpoint has no room for this message right now.
//
// It is not a failure. The message was NOT started, nothing was spent, and
// the caller should offer it again later — which is the whole point of
// distinguishing it from a send error.
var ErrCarrierFull = errors.New("sync: the carrier has no room for this message yet")

func (e *Engine) sendMsg(ep transports.Endpoint, msg []byte) error {
	pkts, err := e.fragment(msg, ep.Capabilities().MaxPayload)
	if err != nil {
		return err
	}
	// ASK FOR THE WHOLE MESSAGE, OR SEND NONE OF IT.
	//
	// Losing one fragment loses the message (kernel/routing.MaxRadioFragments
	// says so in its own comment), so a message half-handed to a full radio
	// is worse than one not started: the fragments that got through spent
	// real airtime and can never be assembled. On a carrier that meters
	// itself we therefore commit only when the room is there, and otherwise
	// wait — the message keeps for the next pass, and the log is the durable
	// copy either way.
	if !transports.CreditOf(ep).Allows(len(pkts)) {
		return ErrCarrierFull
	}
	for _, p := range pkts {
		if err := ep.Send(p); err != nil {
			return err
		}
	}
	return nil
}

// Pump drains the endpoint, processes complete messages, and replies.
// Returns the number of events newly applied. Errors on individual frames
// (unknown author, fork) are counted, not fatal — one bad author must not
// stop sync for everyone (plan §17.4 partial failure).
//
// Pump owns the endpoint: it polls, so it must be the ONLY reader. When one
// link carries several terminals, poll and reassemble once at the link
// level and dispatch whole messages with Handle instead — two engines both
// calling Pump on one endpoint would take turns eating each other's
// packets, and the loser would simply never sync.
func (e *Engine) Pump(ep transports.Endpoint) (applied int, rejected int, err error) {
	for _, pkt := range ep.Poll() {
		raw, err := e.reassemble(pkt)
		if errors.Is(err, ErrNotFragment) {
			// A wrapper (compact profile) may deliver whole messages.
			raw, err = pkt, nil
		}
		if err != nil || raw == nil {
			if err != nil {
				rejected++
			}
			continue
		}
		a, r, herr := e.Handle(ep, raw)
		applied += a
		rejected += r
		if herr != nil {
			return applied, rejected, herr
		}
	}
	return applied, rejected, nil
}

// Handle processes ONE already-reassembled message and replies on ep. It is
// the seam for a link shared by several terminals: the caller polls and
// reassembles once, then routes by terminal.
func (e *Engine) Handle(ep transports.Endpoint, raw []byte) (applied int, rejected int, err error) {
	msg, derr := decodeMessage(raw)
	if derr != nil {
		return 0, 1, nil
	}
	if msg.term != e.Log.Terminal {
		return 0, 1, nil // not ours; the caller should have routed it
	}
	switch msg.msgType {
	case msgSummary:
		if err := e.pushMissing(ep, msg.sum); err != nil {
			return applied, rejected, err
		}
	case msgFrames:
		for _, f := range msg.frames {
			as, err := e.Log.Ingest(f)
			if err != nil {
				rejected++
				continue
			}
			for _, a := range as {
				applied++
				if e.OnApplied != nil {
					e.OnApplied(a)
				}
			}
		}
	case msgBlobReq:
		if err := e.serveBlobs(ep, msg.hashes); err != nil {
			return applied, rejected, err
		}
	case msgBlobData:
		for _, b := range msg.blobs {
			if !e.acceptBlob(b) {
				rejected++
			}
		}
	case msgCustody:
		if e.OnCustodyReceipt != nil && len(msg.receipt) > 0 {
			e.OnCustodyReceipt(msg.receipt)
		}
	}
	return applied, rejected, nil
}

// RequestBlobs asks the peer for wire ids this replica is missing. Only
// blobs that were requested are accepted back (no opportunistic writes).
func (e *Engine) RequestBlobs(ep transports.Endpoint, hashes []id.Hash) error {
	for start := 0; start < len(hashes); start += MaxBlobIDsPerRequest {
		end := min(start+MaxBlobIDsPerRequest, len(hashes))
		batch := hashes[start:end]
		for _, h := range batch {
			e.pending[h] = true
		}
		if err := e.sendMsg(ep, e.encodeBlobReq(batch)); err != nil {
			return err
		}
	}
	return nil
}

// PendingBlobs reports how many requested blobs have not arrived yet.
func (e *Engine) PendingBlobs() int { return len(e.pending) }

// messageBudget is how large one protocol message may get before it is
// fragmented, given what the carrier will carry in a single packet.
//
// It used to be four packets' worth, described as "keep messages a few
// fragments long". Measurement on a shared LoRa channel says that guess is
// expensive: with roughly 80% of packets lost to other people's airtime, and
// with the rule that losing ONE fragment loses the whole message, a
// five-fragment message lands about once in three thousand attempts. A
// single-fragment message lands about once in five.
//
// So on a radio-scale carrier a message is aimed at ONE packet. The cost is
// more messages, each paying the fragment header again; the benefit is that
// they converge at all. A single event larger than the MTU still fragments —
// this bounds BATCHING, not the size of what must be carried.
func messageBudget(ep transports.Endpoint) int {
	mtu := ep.Capabilities().MaxPayload
	if mtu <= 0 {
		return framesBudget // unmetered carrier: batch freely
	}
	if mtu >= framesBudget {
		return framesBudget
	}
	return mtu
}

// serveBlobs answers a request with everything this space legitimately
// publishes and has locally, batched under the transport budget.
func (e *Engine) serveBlobs(ep transports.Endpoint, hashes []id.Hash) error {
	if e.Blobs == nil || e.BlobAllowed == nil {
		return nil // this node does not serve assets; silence, not oracle
	}
	budget := messageBudget(ep)
	var batch [][]byte
	size := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		msg := e.encodeBlobData(batch)
		batch, size = nil, 0
		return e.sendMsg(ep, msg)
	}
	for _, h := range hashes {
		if !e.BlobAllowed(h) {
			continue // not part of this space: refuse silently
		}
		blob, err := e.Blobs.GetBlob(h)
		if err != nil {
			continue // not local; the peer can try another node
		}
		if size+len(blob) > budget && len(batch) > 0 {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, blob)
		size += len(blob)
	}
	return flush()
}

// acceptBlob verifies and stores one delivered blob: hash must match a
// pending request (plan §5: expected-only, verify before PutBlob).
func (e *Engine) acceptBlob(data []byte) bool {
	if e.Blobs == nil {
		return false
	}
	h := id.HashOf(data)
	if !e.pending[h] {
		return false // unrequested: refuse
	}
	if _, err := e.Blobs.PutBlob(data); err != nil {
		return false
	}
	delete(e.pending, h)
	if e.OnBlobStored != nil {
		e.OnBlobStored(h)
	}
	return true
}

// maxChainBurst bounds how many frames one chain may send before the
// scheduler moves to the next eligible chain (fairness: a high-priority
// chain must not starve the others forever; order INSIDE a chain is
// sacred — contiguity is what convergence rests on).
const maxChainBurst = 16

// pushMissing sends the peer everything our log has beyond their summary,
// chunked to the message budget. Chains are scheduled by the priority lane
// of their pending head (security/control before messages before blobs,
// ADR-015 §5) with a per-chain burst cap and round-robin among the
// eligible heads.
func (e *Engine) pushMissing(ep transports.Endpoint, sum *summary) error {
	budget := messageBudget(ep)
	var batch [][]byte
	var batchIDs []id.EventID
	batchSize := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		msg := e.encodeFrames(batch)
		sent := batchIDs
		batch, batchIDs = nil, nil
		batchSize = 0
		if err := e.sendMsg(ep, msg); err != nil {
			return err
		}
		if e.OnSent != nil {
			e.OnSent(sent)
		}
		return nil
	}

	// Collect pending ranges with the lane of each chain's head frame.
	type pendingChain struct {
		frames [][]byte
		lane   signal.Priority
		next   int
	}
	var chains []*pendingChain
	for _, c := range e.Log.Summary() {
		theirs := sum.chains[c.Device]
		if c.ContiguousUntil <= theirs {
			continue
		}
		frames := e.Log.FramesInRange(c.Device, theirs+1, c.ContiguousUntil)
		if len(frames) == 0 {
			continue
		}
		lane := signal.PriorityMessage
		if env, err := signal.Decode(frames[0]); err == nil && env.Priority != 0 {
			lane = env.Priority
		}
		chains = append(chains, &pendingChain{frames: frames, lane: lane})
	}
	// Lane order (lower value = more urgent), stable across runs.
	sort.SliceStable(chains, func(i, j int) bool { return chains[i].lane < chains[j].lane })

	// Weighted round-robin: bursts of maxChainBurst per chain, cycling over
	// eligible chains in lane order until all drained.
	remaining := len(chains)
	for remaining > 0 {
		for _, c := range chains {
			if c.next >= len(c.frames) {
				continue
			}
			burst := 0
			for c.next < len(c.frames) && burst < maxChainBurst {
				f := c.frames[c.next]
				if batchSize+len(f) > budget && len(batch) > 0 {
					if err := flush(); err != nil {
						return err
					}
				}
				batch = append(batch, f)
				batchIDs = append(batchIDs, id.EventIDOf(f))
				batchSize += len(f)
				c.next++
				burst++
			}
			if c.next >= len(c.frames) {
				remaining--
			}
		}
	}
	return flush()
}
