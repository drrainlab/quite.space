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

	// pending holds requested wire ids awaiting delivery. The engine is not
	// safe for concurrent use; callers serialize (the node runtime holds
	// its own lock around every engine call).
	pending map[id.Hash]bool

	nextStream uint64
	reasmSt    *Reassembler
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
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, keyType)
	buf = codec.AppendUint(buf, msgFrames)
	buf = codec.AppendUint(buf, keyTerminal)
	buf = codec.AppendBytes(buf, e.Log.Terminal[:])
	buf = codec.AppendUint(buf, keyFrames)
	buf = codec.AppendArray(buf, len(frames))
	for _, f := range frames {
		buf = codec.AppendBytes(buf, f)
	}
	return buf
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
	e.nextStream++
	return FragmentStream(e.nextStream, msg, mtu)
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

func (e *Engine) sendMsg(ep transports.Endpoint, msg []byte) error {
	pkts, err := e.fragment(msg, ep.Capabilities().MaxPayload)
	if err != nil {
		return err
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
		msg, err := decodeMessage(raw)
		if err != nil {
			rejected++
			continue
		}
		if msg.term != e.Log.Terminal {
			rejected++ // not our terminal; a router would dispatch here
			continue
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

// serveBlobs answers a request with everything this space legitimately
// publishes and has locally, batched under the transport budget.
func (e *Engine) serveBlobs(ep transports.Endpoint, hashes []id.Hash) error {
	if e.Blobs == nil || e.BlobAllowed == nil {
		return nil // this node does not serve assets; silence, not oracle
	}
	budget := framesBudget
	if mtu := ep.Capabilities().MaxPayload; mtu > 0 {
		budget = min(framesBudget, max(mtu*4, 512))
	}
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
	// On tiny MTUs keep messages a few fragments long: losing one fragment
	// loses the whole message, so smaller messages converge faster on lossy
	// links.
	budget := framesBudget
	if mtu := ep.Capabilities().MaxPayload; mtu > 0 {
		budget = min(framesBudget, max(mtu*4, 512))
	}
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
