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

// Subscription is one operator-provisioned routing capability (RB-0A, D1).
//
// The bridge never derives WHO these ids belong to and never asks: they are
// opaque bytes handed to it by the operator, exactly like the destination
// hint it already reads off the wire. What the operator does tell it is
// which SIDE of the boundary each mailbox lives on, because that is a fact
// about topology, not about people:
//
//	RadioDevices    — mailboxes whose owner is reachable only over the
//	                  carrier. The bridge FETCHES these (non-destructively:
//	                  the owner may come online and must still find its mail)
//	                  and puts what it finds on the air.
//	InternetDevices — mailboxes whose owner is reachable only over the
//	                  relay. The bridge PUTS into these what it took off the
//	                  air, using the very same per-recipient hint any node
//	                  would use. Nothing changes on the internet side.
//
// A shared per-terminal mailbox would have been simpler and wrong: it would
// have weakened the per-device addressing the relay already has, and made
// "who drained this" ambiguous.
type Subscription struct {
	NetworkID       string // radio segment label; scopes the loop domain
	Terminal        id.TerminalID
	RadioDevices    []id.DeviceID
	InternetDevices []id.DeviceID
}

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

	// Subscriptions: the destinations this bridge serves (operator file).
	Subscriptions []Subscription

	// WakeEvery bounds how often the bridge may announce itself on the
	// carrier for one destination (default wakeCooldown).
	WakeEvery time.Duration

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
	subs       map[id.TerminalID]Subscription
	learned    map[id.TerminalID]time.Time
	learnBkt   map[id.TerminalID]*routing.TokenBucket
	nextStream uint64
	stats      Stats

	// carried is the carriage ledger: per destination, per author chain,
	// how far this bridge has taken frames into custody. It is built from
	// cleartext headers only and is the honest content of the summary the
	// bridge announces on the carrier.
	carried map[id.TerminalID]map[id.DeviceID]*chainCursor
	// wake tracks the announcement schedule per destination.
	wake map[id.TerminalID]*wakeState
}

// Stats are diagnostics (printed by the daemon, asserted by tests).
type Stats struct {
	RadioIn, RadioOut int
	RelayIn, RelayOut int
	Deduped, Refused  int
	CustodyAcks       int
	// Wakes counts summaries announced on the carrier; NoMailbox counts
	// frames refused because no operator mailbox could receive them.
	Wakes, NoMailbox int
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
	if cfg.WakeEvery <= 0 {
		cfg.WakeEvery = wakeCooldown
	}
	b := &Bridge{
		cfg:       cfg,
		queue:     q,
		seen:      routing.NewSeenCache(0, cfg.QueueCaps.OperatorTTL),
		routes:    routing.NewRoutes(0),
		custodian: key,
		reasm:     kernelsync.NewReassembler(),
		airtime:   routing.NewTokenBucket(cfg.AirtimePerMin, 2048, time.Now()),
		subs:      map[id.TerminalID]Subscription{},
		learned:   map[id.TerminalID]time.Time{},
		learnBkt:  map[id.TerminalID]*routing.TokenBucket{},
		carried:   map[id.TerminalID]map[id.DeviceID]*chainCursor{},
		wake:      map[id.TerminalID]*wakeState{},
	}
	if cfg.AirtimePerMin <= 0 {
		b.airtime = routing.DefaultAirtime(time.Now())
	}
	for _, s := range cfg.Subscriptions {
		b.subs[s.Terminal] = s
	}
	if err := b.loadCarriage(); err != nil {
		return nil, err
	}
	_ = b.seen.Load(cfg.DataDir+"/seen.snap", time.Now())
	return b, nil
}

// Close snapshots the seen-cache and carriage ledger, releases the queue.
func (b *Bridge) Close() error {
	_ = b.seen.Save(b.cfg.DataDir + "/seen.snap")
	_ = b.saveCarriage()
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
// It also enforces the RB-0A deliverability rule: custody is a promise, and
// a destination with no operator-provisioned internet mailbox is one this
// bridge cannot keep. Taking such frames would mean holding them until they
// expired and telling nobody. They are refused at the door instead, and
// counted separately so the operator sees the reason.
func (b *Bridge) subscribed(dest id.TerminalID, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sub, ok := b.subs[dest]; ok {
		if len(sub.InternetDevices) == 0 {
			b.stats.NoMailbox++
			return false
		}
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
	_, ok := b.subs[dest]
	return ok
}

// subscription returns the operator capability for a destination.
func (b *Bridge) subscription(dest id.TerminalID) (Subscription, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.subs[dest]
	return s, ok
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
			// Not frames. A summary still tells the bridge something it
			// needs: a node is alive on this carrier and worth announcing
			// to. It is never answered directly — see WakeRadio.
			if st, ok := kernelsync.ExtractSummaryTerminal(raw); ok {
				b.noteRadioPeer(st, now)
			}
			continue // blobs and receipts remain node↔node business
		}
		b.mu.Lock()
		b.stats.RadioIn += len(frames)
		b.mu.Unlock()
		if !b.subscribed(term, now) {
			b.bumpRefused(len(frames))
			continue
		}
		// The node answered our announcement: the question is no longer
		// outstanding, so the next one may go out on schedule.
		b.answeredWake(term)
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
	b.noteCarried(meta)
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
	// One batch of distinct eligible records, grouped per destination. No
	// requeue dance: NextBatch does not mutate, so a pass can look at the
	// whole queue instead of poking one record at a time.
	batch := b.queue.NextBatch(routing.LinkID("relay:"+b.cfg.RelayAddr),
		b.cfg.RelayDomain, nil, now, relayBatch)
	if len(batch) == 0 {
		return 0, nil
	}
	perDest := map[id.TerminalID][]*routing.CustodyRecord{}
	for _, rec := range batch {
		perDest[rec.Destination] = append(perDest[rec.Destination], rec)
	}
	client, err := relay.DialClient(b.cfg.RelayAddr)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	pushed := 0
	nowU := uint64(now.Unix())
	for dest, recs := range perDest {
		sub, ok := b.subscription(dest)
		if !ok || len(sub.InternetDevices) == 0 {
			// Nowhere to deliver. Admission should have caught this; if a
			// subscription was withdrawn mid-flight, release custody rather
			// than hold frames that can never move.
			for _, r := range recs {
				b.queue.Ack(r.ID)
			}
			b.mu.Lock()
			b.stats.NoMailbox += len(recs)
			b.mu.Unlock()
			continue
		}
		frames := make([][]byte, 0, len(recs))
		for _, r := range recs {
			frames = append(frames, r.Frame)
		}
		body := bundle.Encode(dest, frames)
		// D1 uplink: the SAME per-recipient inbox any node would write to.
		// Alice collects with her ordinary Collect and cannot tell that this
		// copy came off a radio — which is the point.
		delivered := 0
		for _, dev := range sub.InternetDevices {
			hint := relay.HintFor(dest, dev, relay.Bucket(nowU))
			if _, err := client.Put(hint, nowU+uint64(b.cfg.RelayTTL/time.Second), body); err != nil {
				continue
			}
			delivered++
		}
		if delivered == 0 {
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
	subs := make([]Subscription, 0, len(b.subs))
	for _, s := range b.subs {
		if len(s.RadioDevices) > 0 {
			subs = append(subs, s)
		}
	}
	b.mu.Unlock()
	if len(subs) == 0 {
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
	for _, s := range subs {
		for _, dev := range s.RadioDevices {
			cur := relay.Bucket(nowU)
			for i := 0; i < buckets; i++ {
				if cur < uint64(i) {
					break
				}
				hints = append(hints, relay.HintFor(s.Terminal, dev, cur-uint64(i)))
			}
		}
	}
	// D1 downlink: FETCH, never Collect. The mailbox belongs to a node that
	// is merely unreachable right now, not absent — if the bridge drained it,
	// a Bob who later found internet would discover his own mail already
	// eaten. Duplicates are the price, and the seen-cache pays it.
	items, err := client.Fetch(hints)
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

// radioBatch and relayBatch bound one drain pass.
const (
	radioBatch = 8
	relayBatch = 256
)

// PushRadio schedules custody toward the radio carrier under the airtime
// bucket and delivery classes. Returns frames broadcast this pass.
//
// Records already broadcast are held back by NextEligibleAt, so a pass sees
// only what is genuinely ready and works through it. The earlier version
// asked the queue for its single best record, found it was in its resend
// gap, and stopped the entire pass — which is how a bridge with four frames
// waiting for four different people delivered one frame every 45 seconds.
func (b *Bridge) PushRadio(now time.Time) int {
	batch := b.queue.NextBatch(b.cfg.RadioLink, b.cfg.RadioDomain,
		b.downlinkAllowed, now, radioBatch)
	sent := 0
	for _, rec := range batch {
		meta, err := routing.MetaOf(rec.Frame, rec.IngressLink)
		if err != nil || !routing.RadioAdmits(meta.Schema, meta.Size) {
			b.queue.Ack(rec.ID) // never airtime-worthy — release custody
			continue
		}
		msg := kernelsync.EncodeFramesMessage(rec.Destination, [][]byte{rec.Frame})
		if !b.airtime.Take(len(msg), now) {
			break // budget exhausted this pass; the rest keep their turn
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
			b.queue.Requeue(rec.ID, now.Add(resendGap))
			continue
		}
		sendErr := false
		for _, fp := range frags {
			if err := b.cfg.Radio.Send(fp); err != nil {
				sendErr = true
				break
			}
		}
		if sendErr {
			// The carrier is down, not this record's fault: hold it back and
			// stop the pass rather than burning attempts on a dead link.
			b.queue.Requeue(rec.ID, now.Add(resendGap))
			break
		}
		b.mu.Lock()
		b.stats.RadioOut++
		b.mu.Unlock()
		b.routes.Observe(rec.Destination, b.cfg.RadioLink, now)
		if rec.Attempts+1 >= maxRadioAttempts {
			b.queue.Ack(rec.ID)
		} else {
			b.queue.Requeue(rec.ID, now.Add(resendGap))
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
