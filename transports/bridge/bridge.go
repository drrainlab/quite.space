// Package bridge is the blind boundary element (TN-B, ADR-015 §6): a
// store-and-forward daemon between a radio carrier and the internet relay.
// It holds no identity, no epoch keys, and never reads payloads —
// blindness is structural (this package must never import identity, epoch
// or terminals machinery; an import-boundary test enforces it). All it
// sees is cleartext envelope HEADERS and sync/bundle STRUCTURE. It does
// not know the product concept "Space": everything here is
// per-destination-hint — the opaque 32-byte destination id on the wire.
package bridge

import (
	"crypto/ed25519"
	"fmt"
	"sync"
	"time"

	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/kernel/routing"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports"
	"github.com/drrainlab/quiet_places/transports/bundle"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// Config wires one bridge instance.
type Config struct {
	DataDir  string
	Instance string // human label for receipts/diagnostics

	// Radio side (injected endpoint — cmd wires meshtastic/compact/sim).
	Radio       transports.Endpoint
	RadioLink   routing.LinkID
	RadioDomain routing.LoopDomainID

	// Relay side.
	RelayAddr   string
	RelayDomain routing.LoopDomainID

	// Subscriptions: destination hints this bridge serves (operator file).
	Subscriptions []id.TerminalID

	// Learn: admission-controlled auto-subscribe (OFF by default; gates
	// radio→relay uplink ONLY, never relay→radio).
	Learn         bool
	LearnCap      int
	LearnPerMin   int // per-source token rate
	AirtimePerMin float64
	QueueCaps     routing.QueueCaps
	RelayTTL      time.Duration // retention horizon to poll across buckets
	MaxBuckets    int           // operator cap on bucket sweep
}

// Bridge is one running blind boundary element.
type Bridge struct {
	cfg       Config
	queue     *routing.Queue
	seen      *routing.SeenCache
	routes    *routing.Routes
	custodian ed25519.PrivateKey
	reasm     *kernelsync.Reassembler
	airtime   *routing.TokenBucket

	mu         sync.Mutex
	subs       map[id.TerminalID]bool
	learned    map[id.TerminalID]time.Time
	learnBkt   map[id.TerminalID]*routing.TokenBucket
	lastSent   map[uint64]time.Time
	nextStream uint64
	stats      Stats
}

// Stats are diagnostics (printed by the daemon, asserted by tests).
type Stats struct {
	RadioIn, RadioOut   int
	RelayIn, RelayOut   int
	Deduped, Refused    int
	CustodyAcks         int
}

// New opens the bridge: durable queue, seen-cache snapshot, custodian key.
func New(cfg Config) (*Bridge, error) {
	if cfg.QueueCaps == (routing.QueueCaps{}) {
		cfg.QueueCaps = routing.DefaultQueueCaps()
	}
	if cfg.RelayTTL <= 0 {
		cfg.RelayTTL = 48 * time.Hour
	}
	if cfg.MaxBuckets <= 0 {
		cfg.MaxBuckets = 9 // ceil(48h/6h)+1
	}
	if cfg.LearnCap <= 0 {
		cfg.LearnCap = 64
	}
	q, err := routing.OpenQueue(cfg.DataDir+"/queue", cfg.QueueCaps)
	if err != nil {
		return nil, err
	}
	key, err := LoadCustodianKey(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	b := &Bridge{
		cfg:       cfg,
		queue:     q,
		seen:      routing.NewSeenCache(0, cfg.QueueCaps.OperatorTTL),
		routes:    routing.NewRoutes(0),
		custodian: key,
		reasm:     kernelsync.NewReassembler(),
		airtime:   routing.NewTokenBucket(cfg.AirtimePerMin, 2048, time.Now()),
		subs:      map[id.TerminalID]bool{},
		learned:   map[id.TerminalID]time.Time{},
		learnBkt:  map[id.TerminalID]*routing.TokenBucket{},
		lastSent:  map[uint64]time.Time{},
	}
	if cfg.AirtimePerMin <= 0 {
		b.airtime = routing.DefaultAirtime(time.Now())
	}
	for _, s := range cfg.Subscriptions {
		b.subs[s] = true
	}
	_ = b.seen.Load(cfg.DataDir+"/seen.snap", time.Now())
	return b, nil
}

// Close snapshots the seen-cache and releases the queue.
func (b *Bridge) Close() error {
	_ = b.seen.Save(b.cfg.DataDir + "/seen.snap")
	return b.queue.Close()
}

// CustodianPub exposes the custodian public key for operator pinning.
func (b *Bridge) CustodianPub() ed25519.PublicKey {
	return b.custodian.Public().(ed25519.PublicKey)
}

// Stats returns a copy of the counters.
func (b *Bridge) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

// subscribed answers "does this bridge serve dest for radio→relay uplink"
// with learn-mode admission control (rev 2.1, correction 5): a valid
// signature is NOT permission — unknown sources enter probation with tiny
// token buckets, bounded count, and never widen relay→radio.
func (b *Bridge) subscribed(dest id.TerminalID, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs[dest] {
		return true
	}
	if !b.cfg.Learn {
		return false
	}
	if _, ok := b.learned[dest]; ok {
		bkt := b.learnBkt[dest]
		return bkt == nil || bkt.Take(1, now)
	}
	if len(b.learned) >= b.cfg.LearnCap {
		return false
	}
	rate := float64(b.cfg.LearnPerMin)
	if rate <= 0 {
		rate = 4 // probation: a few frames a minute until the operator approves
	}
	b.learned[dest] = now
	b.learnBkt[dest] = routing.NewTokenBucket(rate, rate, now)
	return true
}

// uplinkAllowed: relay→radio flows only for OPERATOR subscriptions —
// learned destinations never widen the downlink (anti-beacon).
func (b *Bridge) downlinkAllowed(dest id.TerminalID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.subs[dest]
}

// PumpRadio drains the radio endpoint once: reassemble → extract frames →
// meta/policy/seen → custody. Returns frames taken into custody.
func (b *Bridge) PumpRadio(now time.Time) int {
	took := 0
	for _, pkt := range b.cfg.Radio.Poll() {
		raw, err := b.reasm.Feed(pkt)
		if err != nil || raw == nil {
			if err == kernelsync.ErrNotFragment {
				raw = pkt
			} else {
				continue
			}
		}
		term, frames, ok := kernelsync.ExtractFramesMessage(raw)
		if !ok {
			continue // summaries/blobs are node↔node business
		}
		b.mu.Lock()
		b.stats.RadioIn += len(frames)
		b.mu.Unlock()
		if !b.subscribed(term, now) {
			b.bumpRefused(len(frames))
			continue
		}
		for _, f := range frames {
			b.takeCustody(f, b.cfg.RadioLink, b.cfg.RadioDomain, now)
		}
		took += len(frames)
	}
	return took
}

func (b *Bridge) bumpRefused(n int) {
	b.mu.Lock()
	b.stats.Refused += n
	b.mu.Unlock()
}

// takeCustody applies meta/seen/policy and enqueues durably.
func (b *Bridge) takeCustody(frame []byte, link routing.LinkID,
	domain routing.LoopDomainID, now time.Time) bool {

	meta, err := routing.MetaOf(frame, link)
	if err != nil {
		return false
	}
	if b.seen.Seen(routing.KeyOf(frame), now) {
		b.mu.Lock()
		b.stats.Deduped++
		b.mu.Unlock()
		return false
	}
	if _, err := b.queue.Enqueue(meta, frame, domain, now); err != nil {
		b.bumpRefused(1)
		return false
	}
	b.routes.Observe(meta.Destination, link, now)
	return true
}

// AcceptUplink takes frames handed over a direct link (a node feeding its
// local bridge) and returns the SIGNED custody receipt — emitted strictly
// after the durable enqueue (Enqueue fsyncs before returning).
func (b *Bridge) AcceptUplink(term id.TerminalID, frames [][]byte,
	link routing.LinkID, domain routing.LoopDomainID, now time.Time) []byte {

	var held []id.EventID
	for _, f := range frames {
		if b.takeCustody(f, link, domain, now) {
			held = append(held, id.EventIDOf(f))
		}
	}
	if len(held) == 0 {
		return nil
	}
	b.mu.Lock()
	b.stats.CustodyAcks++
	b.mu.Unlock()
	r := &CustodyReceipt{
		FrameIDs:   held,
		StoreID:    b.cfg.DataDir,
		AcceptedAt: uint64(now.Unix()),
		ExpiresAt:  uint64(now.Add(b.cfg.QueueCaps.OperatorTTL).Unix()),
		Instance:   b.cfg.Instance,
	}
	return r.Sign(b.custodian)
}

// PushRelay drains custody toward the relay: frames grouped
// per-destination-hint into bundles, Put under the rotating hint, Ack on
// success. Returns pushed frame count.
func (b *Bridge) PushRelay(now time.Time) (int, error) {
	// Collect sendable records per destination.
	perDest := map[id.TerminalID][]*routing.CustodyRecord{}
	seenIDs := map[uint64]bool{}
	for {
		rec, ok := b.queue.Next(routing.LinkID("relay:"+b.cfg.RelayAddr),
			b.cfg.RelayDomain, nil, now)
		if !ok || seenIDs[rec.ID] {
			break
		}
		seenIDs[rec.ID] = true
		perDest[rec.Destination] = append(perDest[rec.Destination], rec)
		if len(seenIDs) >= 256 {
			break
		}
		// Temporarily mark to avoid re-selection within this pass.
		b.mu.Lock()
		b.lastSent[rec.ID] = now
		b.mu.Unlock()
		b.queue.Requeue(rec.ID)
	}
	if len(perDest) == 0 {
		return 0, nil
	}
	client, err := relay.DialClient(b.cfg.RelayAddr)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	pushed := 0
	nowU := uint64(now.Unix())
	for dest, recs := range perDest {
		frames := make([][]byte, 0, len(recs))
		for _, r := range recs {
			frames = append(frames, r.Frame)
		}
		body := bundle.Encode(dest, frames)
		hint := relay.Hint(dest, relay.Bucket(nowU))
		if _, err := client.Put(hint, nowU+uint64(b.cfg.RelayTTL/time.Second), body); err != nil {
			continue // records stay in custody for the next pass
		}
		for _, r := range recs {
			b.queue.Ack(r.ID)
		}
		b.routes.Observe(dest, routing.LinkID("relay:"+b.cfg.RelayAddr), now)
		pushed += len(frames)
	}
	b.mu.Lock()
	b.stats.RelayOut += pushed
	b.mu.Unlock()
	return pushed, nil
}

// PullRelay collects bundles for OPERATOR-subscribed destinations across
// every hint bucket within the retention horizon (rev 2.1: cur+prev only
// covered ~12h of bridge downtime) and takes custody of their frames.
func (b *Bridge) PullRelay(now time.Time) (int, error) {
	b.mu.Lock()
	dests := make([]id.TerminalID, 0, len(b.subs))
	for d := range b.subs {
		dests = append(dests, d)
	}
	b.mu.Unlock()
	if len(dests) == 0 {
		return 0, nil
	}
	client, err := relay.DialClient(b.cfg.RelayAddr)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	nowU := uint64(now.Unix())
	buckets := int(b.cfg.RelayTTL/(6*3600*time.Second)) + 1
	if buckets > b.cfg.MaxBuckets {
		buckets = b.cfg.MaxBuckets
	}
	var hints [][]byte
	for _, d := range dests {
		cur := relay.Bucket(nowU)
		for i := 0; i < buckets; i++ {
			if cur < uint64(i) {
				break
			}
			hints = append(hints, relay.Hint(d, cur-uint64(i)))
		}
	}
	items, err := client.Collect(hints)
	if err != nil {
		return 0, err
	}
	took := 0
	for _, item := range items {
		term, frames, err := bundle.Decode(item)
		if err != nil {
			continue
		}
		if !b.downlinkAllowed(term) {
			b.bumpRefused(len(frames))
			continue
		}
		for _, f := range frames {
			if b.takeCustody(f, routing.LinkID("relay:"+b.cfg.RelayAddr),
				b.cfg.RelayDomain, now) {
				took++
			}
		}
	}
	b.mu.Lock()
	b.stats.RelayIn += took
	b.mu.Unlock()
	return took, nil
}

// resendGap spaces re-broadcasts of the same custody record on radio.
const resendGap = 45 * time.Second

// maxRadioAttempts bounds broadcasts per record before custody is released
// (AckNone: no receiver feedback — repetition plus node↔node sync heal).
const maxRadioAttempts = 3

// PushRadio schedules custody toward the radio carrier under the airtime
// bucket and delivery classes. Returns frames broadcast this pass.
func (b *Bridge) PushRadio(now time.Time) int {
	sent := 0
	for i := 0; i < 8; i++ {
		rec, ok := b.queue.Next(b.cfg.RadioLink, b.cfg.RadioDomain,
			b.downlinkAllowed, now)
		if !ok {
			break
		}
		b.mu.Lock()
		last := b.lastSent[rec.ID]
		b.mu.Unlock()
		if now.Sub(last) < resendGap {
			break // youngest eligible already sent recently; wait
		}
		meta, err := routing.MetaOf(rec.Frame, rec.IngressLink)
		if err != nil || !routing.RadioAdmits(meta.Schema, meta.Size) {
			b.queue.Ack(rec.ID) // never airtime-worthy — release custody
			continue
		}
		msg := kernelsync.EncodeFramesMessage(rec.Destination, [][]byte{rec.Frame})
		if !b.airtime.Take(len(msg), now) {
			break // budget exhausted this pass
		}
		// The wire grammar is ALWAYS fragment-wrapped (single fragment for
		// small messages) — identical to what node engines emit.
		b.mu.Lock()
		b.nextStream++
		stream := b.nextStream
		b.mu.Unlock()
		frags, err := kernelsync.FragmentStream(stream, msg,
			b.cfg.Radio.Capabilities().MaxPayload)
		if err != nil {
			b.queue.Requeue(rec.ID)
			break
		}
		sendErr := false
		for _, fp := range frags {
			if err := b.cfg.Radio.Send(fp); err != nil {
				sendErr = true
				break
			}
		}
		if sendErr {
			b.queue.Requeue(rec.ID)
			break
		}
		b.mu.Lock()
		b.lastSent[rec.ID] = now
		b.stats.RadioOut++
		b.mu.Unlock()
		b.routes.Observe(rec.Destination, b.cfg.RadioLink, now)
		if rec.Attempts+1 >= maxRadioAttempts {
			b.queue.Ack(rec.ID)
		} else {
			b.queue.Requeue(rec.ID)
		}
		sent++
	}
	return sent
}

// Sweep expires custody and prunes learn probation.
func (b *Bridge) Sweep(now time.Time) int {
	b.mu.Lock()
	for d, at := range b.learned {
		if now.Sub(at) > 24*time.Hour {
			delete(b.learned, d)
			delete(b.learnBkt, d)
		}
	}
	b.mu.Unlock()
	return b.queue.Sweep(now)
}

// QueueLen exposes custody depth for diagnostics.
func (b *Bridge) QueueLen() int { return b.queue.Len() }

// String describes the bridge (daemon banner).
func (b *Bridge) String() string {
	return fmt.Sprintf("quiet-bridge %s · custodian %x… · %d subscriptions",
		b.cfg.Instance, b.CustodianPub()[:6], len(b.cfg.Subscriptions))
}
