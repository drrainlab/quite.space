// Package sync implements Sync Protocol v0 (plan §17, M0.6): summary
// exchange, push of missing ranges, chunking, and fragmentation down to
// radio-scale MTUs. The protocol is idempotent and resumable by
// construction: any message can be lost, duplicated, or reordered and the
// state machine only ever converges (dedup lives in the event log).
package sync

import (
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports"
)

// Message types.
const (
	msgSummary = 1
	msgFrames  = 2
)

// Message key table.
const (
	keyType     = 1
	keyTerminal = 2
	keyChains   = 3
	keyFrames   = 4
)

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

	nextStream uint64
	reasm      map[uint64]*reassembly
}

type reassembly struct {
	total  int
	chunks map[int][]byte
}

// NewEngine wraps a log for syncing.
func NewEngine(log *eventlog.Log) *Engine {
	return &Engine{Log: log, reasm: map[uint64]*reassembly{}}
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

type summary struct {
	terminal id.TerminalID
	chains   map[id.DeviceID]uint64
}

func decodeMessage(data []byte) (msgType uint64, term id.TerminalID, sum *summary, frames [][]byte, err error) {
	d := codec.NewDecoder(data)
	m, err := d.ReadMapHeader()
	if err != nil {
		return 0, term, nil, nil, err
	}
	chains := map[id.DeviceID]uint64{}
	for {
		k, ok, e := m.Next()
		if e != nil {
			return 0, term, nil, nil, e
		}
		if !ok {
			break
		}
		switch k {
		case keyType:
			msgType, err = d.ReadUint()
		case keyTerminal:
			var b []byte
			b, err = d.ReadBytes()
			if err == nil {
				if len(b) != id.Size {
					return 0, term, nil, nil, errors.New("sync: bad terminal id")
				}
				copy(term[:], b)
			}
		case keyChains:
			var n int
			n, err = d.ReadArray()
			if err != nil {
				return 0, term, nil, nil, err
			}
			for range n {
				if _, err = d.ReadArray(); err != nil {
					return 0, term, nil, nil, err
				}
				var devBytes []byte
				devBytes, err = d.ReadBytes()
				if err != nil || len(devBytes) != id.Size {
					return 0, term, nil, nil, errors.New("sync: bad device id")
				}
				var until uint64
				until, err = d.ReadUint()
				if err != nil {
					return 0, term, nil, nil, err
				}
				var dev id.DeviceID
				copy(dev[:], devBytes)
				chains[dev] = until
			}
		case keyFrames:
			var n int
			n, err = d.ReadArray()
			if err != nil {
				return 0, term, nil, nil, err
			}
			for range n {
				var f []byte
				f, err = d.ReadBytes()
				if err != nil {
					return 0, term, nil, nil, err
				}
				frames = append(frames, append([]byte(nil), f...))
			}
		default:
			err = d.SkipItem()
		}
		if err != nil {
			return 0, term, nil, nil, err
		}
	}
	if err := d.Done(); err != nil {
		return 0, term, nil, nil, err
	}
	if msgType == msgSummary {
		sum = &summary{terminal: term, chains: chains}
	}
	return msgType, term, sum, frames, nil
}

// ---- Fragmentation (radio-scale MTUs, plan §19 T4) ----

func (e *Engine) fragment(msg []byte, mtu int) ([][]byte, error) {
	e.nextStream++
	stream := e.nextStream
	if mtu <= 0 {
		pkt := fragHeader(stream, 0, 1)
		pkt = codec.AppendUint(pkt, fragKeyChunk)
		pkt = codec.AppendBytes(pkt, msg)
		return [][]byte{pkt}, nil
	}
	if mtu < minMTU {
		return nil, fmt.Errorf("sync: MTU %d below minimum %d", mtu, minMTU)
	}
	overhead := len(fragHeader(stream, 0, 1)) + 4 // header + bytes-length head
	chunkSize := mtu - overhead
	total := (len(msg) + chunkSize - 1) / chunkSize
	var out [][]byte
	for i := 0; i < total; i++ {
		start := i * chunkSize
		end := min(start+chunkSize, len(msg))
		pkt := fragHeader(stream, i, total)
		pkt = codec.AppendUint(pkt, fragKeyChunk)
		pkt = codec.AppendBytes(pkt, msg[start:end])
		out = append(out, pkt)
	}
	return out, nil
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
	d := codec.NewDecoder(pkt)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	var stream, index, total uint64
	var chunk []byte
	for {
		k, ok, er := m.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case fragKeyStream:
			stream, er = d.ReadUint()
		case fragKeyIndex:
			index, er = d.ReadUint()
		case fragKeyTotal:
			total, er = d.ReadUint()
		case fragKeyChunk:
			chunk, er = d.ReadBytes()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if total == 0 || index >= total || chunk == nil {
		return nil, errors.New("sync: malformed fragment")
	}
	if total == 1 {
		return append([]byte(nil), chunk...), nil
	}
	r, ok := e.reasm[stream]
	if !ok {
		r = &reassembly{total: int(total), chunks: map[int][]byte{}}
		e.reasm[stream] = r
	}
	if _, dup := r.chunks[int(index)]; !dup {
		r.chunks[int(index)] = append([]byte(nil), chunk...)
	}
	if len(r.chunks) < r.total {
		return nil, nil // incomplete
	}
	delete(e.reasm, stream)
	var msg []byte
	for i := 0; i < r.total; i++ {
		part, ok := r.chunks[i]
		if !ok {
			return nil, errors.New("sync: reassembly hole")
		}
		msg = append(msg, part...)
	}
	return msg, nil
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
		msg, err := e.reassemble(pkt)
		if err != nil || msg == nil {
			if err != nil {
				rejected++
			}
			continue
		}
		msgType, term, sum, frames, err := decodeMessage(msg)
		if err != nil {
			rejected++
			continue
		}
		if term != e.Log.Terminal {
			rejected++ // not our terminal; a router would dispatch here
			continue
		}
		switch msgType {
		case msgSummary:
			if err := e.pushMissing(ep, sum); err != nil {
				return applied, rejected, err
			}
		case msgFrames:
			for _, f := range frames {
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
		}
	}
	return applied, rejected, nil
}

// pushMissing sends the peer everything our log has beyond their summary,
// chunked to the message budget.
func (e *Engine) pushMissing(ep transports.Endpoint, sum *summary) error {
	// On tiny MTUs keep messages a few fragments long: losing one fragment
	// loses the whole message, so smaller messages converge faster on lossy
	// links.
	budget := framesBudget
	if mtu := ep.Capabilities().MaxPayload; mtu > 0 {
		budget = min(framesBudget, max(mtu*4, 512))
	}
	var batch [][]byte
	batchSize := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		msg := e.encodeFrames(batch)
		batch = nil
		batchSize = 0
		return e.sendMsg(ep, msg)
	}
	for _, c := range e.Log.Summary() {
		theirs := sum.chains[c.Device]
		if c.ContiguousUntil <= theirs {
			continue
		}
		for _, f := range e.Log.FramesInRange(c.Device, theirs+1, c.ContiguousUntil) {
			if batchSize+len(f) > budget && len(batch) > 0 {
				if err := flush(); err != nil {
					return err
				}
			}
			batch = append(batch, f)
			batchSize += len(f)
		}
	}
	return flush()
}
