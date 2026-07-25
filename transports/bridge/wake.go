// Radio wake-up (RB-0A blocker 2). A node on a carrier hands over frames
// only when something asks for them: its sync engine answers a summary, it
// does not volunteer. Before this, a bridge listened to a radio segment
// without ever speaking on it, so Bob announced his chains into silence and
// his messages stayed on his device — the loop was open in one direction
// and nothing in the test suite could see it.
//
// The bridge therefore announces a summary of its own. Two rules keep that
// from becoming a storm:
//
//   - It never answers a summary with a summary. A peer's summary only
//     marks that peer alive; the announcement itself runs on the bridge's
//     own cooldown. Otherwise two elements would volley until the batteries
//     ran out, and two bridges on one carrier would volley twice as fast.
//   - It announces what it has ACTUALLY CARRIED, not emptiness. A bridge
//     claiming to know nothing would pull the entire log across LoRa on
//     every wake-up. The carriage ledger below is built from cleartext
//     envelope headers only — author device and sequence, the same two
//     fields a summary is made of — so honesty here costs no blindness.
package bridge

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/drrainlab/quiet_places/kernel/routing"
	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

const (
	// wakeCooldown bounds announcements for ONE destination on the carrier.
	wakeCooldown = 30 * time.Second
	// wakeJitterFrac spreads announcements so that two bridges sharing a
	// segment do not transmit in lockstep.
	wakeJitterFrac = 4
	// wakeOutstanding is how long an unanswered announcement suppresses the
	// next one: at most one question per destination is in the air.
	wakeOutstandingFor = 3
	// peerFresh is how long a heard node keeps the carrier "worth talking
	// to" after its last message.
	peerFresh = 10 * time.Minute
	// aheadCap bounds out-of-order sequences held per chain.
	aheadCap = 256
	// maxAdverts bounds the chains announced in one summary (airtime).
	maxAdverts = 32
)

// chainCursor tracks contiguous carriage of one author chain.
type chainCursor struct {
	until uint64          // highest contiguous sequence carried
	ahead map[uint64]bool // carried but not yet contiguous
}

// wakeState is the announcement schedule for one destination.
type wakeState struct {
	lastPeer    time.Time // last time a node was heard for this destination
	lastWake    time.Time // last announcement
	outstanding bool      // an announcement is awaiting an answer
	seq         uint64    // announcement counter, feeds the jitter
}

// noteRadioPeer records that a node spoke for dest on the carrier.
func (b *Bridge) noteRadioPeer(dest id.TerminalID, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[dest]; !ok {
		return // not ours to answer for
	}
	w := b.wake[dest]
	if w == nil {
		w = &wakeState{}
		b.wake[dest] = w
	}
	w.lastPeer = now
}

// noteCarried advances the carriage ledger from one frame's header.
func (b *Bridge) noteCarried(meta routing.FrameMeta) {
	if meta.Sequence == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	chains := b.carried[meta.Destination]
	if chains == nil {
		chains = map[id.DeviceID]*chainCursor{}
		b.carried[meta.Destination] = chains
	}
	c := chains[meta.Author]
	if c == nil {
		c = &chainCursor{ahead: map[uint64]bool{}}
		chains[meta.Author] = c
	}
	if meta.Sequence <= c.until {
		return
	}
	if meta.Sequence == c.until+1 {
		c.until = meta.Sequence
		for c.ahead[c.until+1] {
			delete(c.ahead, c.until+1)
			c.until++
		}
		return
	}
	if len(c.ahead) < aheadCap {
		c.ahead[meta.Sequence] = true
	}
	// Over the cap the gap simply stays a gap: the summary under-claims,
	// the node re-sends, and nothing is lost. Under-claiming is the safe
	// direction of error — over-claiming would silently skip frames.
}

// advertsFor renders the carriage ledger as summary chains.
func (b *Bridge) advertsFor(dest id.TerminalID) []kernelsync.ChainAdvert {
	b.mu.Lock()
	defer b.mu.Unlock()
	chains := b.carried[dest]
	out := make([]kernelsync.ChainAdvert, 0, len(chains))
	for dev, c := range chains {
		out = append(out, kernelsync.ChainAdvert{Device: dev, ContiguousUntil: c.until})
	}
	sort.Slice(out, func(i, j int) bool {
		return string(out[i].Device[:]) < string(out[j].Device[:])
	})
	if len(out) > maxAdverts {
		out = out[:maxAdverts]
	}
	return out
}

// eligibleDownlink reports whether custody is waiting for the carrier —
// a reason to speak up even before any node has been heard.
func (b *Bridge) eligibleDownlink(dest id.TerminalID, now time.Time) bool {
	rec, ok := b.queue.Next(b.cfg.RadioLink, b.cfg.RadioDomain,
		func(d id.TerminalID) bool { return d == dest }, now)
	return ok && rec != nil
}

// WakeRadio announces the bridge's carriage on the carrier for every
// destination that is due. Returns the number of summaries sent.
//
// A summary goes out when the carrier is worth talking to — a node has been
// heard for this destination, or custody is waiting to go out to one — and
// the cooldown plus jitter have elapsed, and no previous announcement is
// still outstanding. Announcements ride the control lane by construction:
// they are tiny and they are sent before PushRadio drains the data queue,
// so a backed-up queue can never hide the bridge's presence.
func (b *Bridge) WakeRadio(now time.Time) int {
	if b.cfg.Radio == nil {
		return 0
	}
	b.mu.Lock()
	dests := make([]id.TerminalID, 0, len(b.subs))
	for d := range b.subs {
		dests = append(dests, d)
	}
	b.mu.Unlock()
	sort.Slice(dests, func(i, j int) bool {
		return string(dests[i][:]) < string(dests[j][:])
	})

	sent := 0
	for _, dest := range dests {
		if !b.dueForWake(dest, now) {
			continue
		}
		msg := kernelsync.EncodeSummaryMessage(dest, b.advertsFor(dest))
		if !b.airtime.Take(len(msg), now) {
			continue
		}
		b.mu.Lock()
		b.nextStream++
		stream := b.nextStream
		b.mu.Unlock()
		frags, err := kernelsync.FragmentStream(stream, msg,
			b.cfg.Radio.Capabilities().MaxPayload)
		if err != nil {
			continue
		}
		failed := false
		for _, f := range frags {
			if err := b.cfg.Radio.Send(f); err != nil {
				failed = true
				break
			}
		}
		if failed {
			continue
		}
		b.mu.Lock()
		w := b.wake[dest]
		w.lastWake = now
		w.outstanding = true
		w.seq++
		b.stats.Wakes++
		b.mu.Unlock()
		sent++
	}
	return sent
}

// dueForWake applies the schedule: liveness OR pending downlink, cooldown
// with jitter, and one outstanding announcement at a time.
func (b *Bridge) dueForWake(dest id.TerminalID, now time.Time) bool {
	pending := b.eligibleDownlink(dest, now)

	b.mu.Lock()
	defer b.mu.Unlock()
	w := b.wake[dest]
	if w == nil {
		w = &wakeState{}
		b.wake[dest] = w
	}
	heard := !w.lastPeer.IsZero() && now.Sub(w.lastPeer) < peerFresh
	if !heard && !pending {
		return false // nobody there, nothing to deliver: stay quiet
	}
	gap := b.cfg.WakeEvery + b.jitterLocked(dest, w.seq)
	if w.outstanding {
		// The previous question has not been answered. Wait several
		// cooldowns before repeating it rather than talking over a node
		// that may simply be slow, then let it go so a lost frame does
		// not silence the bridge forever.
		if now.Sub(w.lastWake) < time.Duration(wakeOutstandingFor)*gap {
			return false
		}
		w.outstanding = false
	}
	return w.lastWake.IsZero() || now.Sub(w.lastWake) >= gap
}

// jitterLocked is jitter() without taking b.mu (caller holds it).
func (b *Bridge) jitterLocked(dest id.TerminalID, seq uint64) time.Duration {
	h := sha256.New()
	h.Write(b.custodian.Public().(ed25519.PublicKey))
	h.Write(dest[:])
	var s [8]byte
	binary.BigEndian.PutUint64(s[:], seq)
	h.Write(s[:])
	sum := h.Sum(nil)
	span := b.cfg.WakeEvery / wakeJitterFrac
	if span <= 0 {
		return 0
	}
	return time.Duration(binary.BigEndian.Uint64(sum[:8]) % uint64(span))
}

// answeredWake clears the outstanding flag once frames arrive for dest.
func (b *Bridge) answeredWake(dest id.TerminalID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if w := b.wake[dest]; w != nil {
		w.outstanding = false
	}
}

// ---- carriage ledger persistence ----
//
// Losing the ledger is not a correctness failure — the bridge would simply
// under-claim and a node would re-send what it already carried, which dedup
// absorbs. It is an AIRTIME failure, and on LoRa that is the expensive kind,
// so the ledger survives restarts.

const (
	carKeyDest   = 1
	carKeyChains = 2
	carKeyDevice = 3
	carKeyUntil  = 4
)

func (b *Bridge) carriagePath() string {
	return filepath.Join(b.cfg.DataDir, "carriage.snap")
}

func (b *Bridge) saveCarriage() error {
	b.mu.Lock()
	type entry struct {
		dest   id.TerminalID
		chains []kernelsync.ChainAdvert
	}
	var entries []entry
	for dest, chains := range b.carried {
		e := entry{dest: dest}
		for dev, c := range chains {
			e.chains = append(e.chains, kernelsync.ChainAdvert{
				Device: dev, ContiguousUntil: c.until})
		}
		sort.Slice(e.chains, func(i, j int) bool {
			return string(e.chains[i].Device[:]) < string(e.chains[j].Device[:])
		})
		entries = append(entries, e)
	}
	b.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool {
		return string(entries[i].dest[:]) < string(entries[j].dest[:])
	})

	buf := codec.AppendArray(nil, len(entries))
	for _, e := range entries {
		buf = codec.AppendMap(buf, 2)
		buf = codec.AppendUint(buf, carKeyDest)
		buf = codec.AppendBytes(buf, e.dest[:])
		buf = codec.AppendUint(buf, carKeyChains)
		buf = codec.AppendArray(buf, len(e.chains))
		for _, c := range e.chains {
			buf = codec.AppendMap(buf, 2)
			buf = codec.AppendUint(buf, carKeyDevice)
			buf = codec.AppendBytes(buf, c.Device[:])
			buf = codec.AppendUint(buf, carKeyUntil)
			buf = codec.AppendUint(buf, c.ContiguousUntil)
		}
	}
	tmp := b.carriagePath() + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, b.carriagePath())
}

func (b *Bridge) loadCarriage() error {
	data, err := os.ReadFile(b.carriagePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	d := codec.NewDecoder(data)
	n, err := d.ReadArray()
	if err != nil {
		return nil // an unreadable ledger costs airtime, never correctness
	}
	for range n {
		m, err := d.ReadMapHeader()
		if err != nil {
			return nil
		}
		var dest id.TerminalID
		chains := map[id.DeviceID]*chainCursor{}
		for {
			k, ok, err := m.Next()
			if err != nil || !ok {
				break
			}
			switch k {
			case carKeyDest:
				raw, err := d.ReadBytes()
				if err != nil || len(raw) != id.Size {
					return nil
				}
				copy(dest[:], raw)
			case carKeyChains:
				cn, err := d.ReadArray()
				if err != nil {
					return nil
				}
				for range cn {
					cm, err := d.ReadMapHeader()
					if err != nil {
						return nil
					}
					var dev id.DeviceID
					var until uint64
					for {
						ck, ok, err := cm.Next()
						if err != nil || !ok {
							break
						}
						switch ck {
						case carKeyDevice:
							raw, err := d.ReadBytes()
							if err != nil || len(raw) != id.Size {
								return nil
							}
							copy(dev[:], raw)
						case carKeyUntil:
							until, err = d.ReadUint()
							if err != nil {
								return nil
							}
						default:
							if err := d.SkipItem(); err != nil {
								return nil
							}
						}
					}
					chains[dev] = &chainCursor{until: until, ahead: map[uint64]bool{}}
				}
			default:
				if err := d.SkipItem(); err != nil {
					return nil
				}
			}
		}
		if len(chains) > 0 {
			b.carried[dest] = chains
		}
	}
	return nil
}
