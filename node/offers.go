package node

// The delivery delta book — what this node has already handed to whom.
//
// RR-6's idiom was "the whole log rides every push and EventID dedup makes
// re-delivery idempotent". True, and it kept the cursor logic honest: a
// copy parked on a guessed relay never advanced anything, so the space
// re-offered until a stated route existed. The bill arrived with the demo
// catalog: eleven public spaces held on a guess, one of them a greenhouse
// with 46 000 sensor events, re-mailed to every recipient's mailbox on
// every hot cycle — measured at 1270 puts and 48 MB a minute into one
// relay, almost all of it the same bytes.
//
// This book separates two things the cursor used to conflate:
//
//   what a mailbox HOLDS   — offerMark.base: the first N base frames of the
//                            log were put in device D's mailbox at endpoint
//                            E; the next push sends frames after N
//   what counts DELIVERED  — untouched: rs.lastLen and the held reasons in
//                            relaysync.go keep saying "on a guess, cursor
//                            unmoved" exactly as before
//
// A mark is only as good as the mailbox it names. It is void when the
// recipient's endpoint changes (the mailbox on the other relay is empty),
// when stated knowledge displaces a guess (resetOffers, from relaysync),
// and — for guessed endpoints — after guessReofferAfter, because a relay
// keeps items for 48 h and a recipient who turns up later must still find
// the history. In memory only: a restart re-offers once, deduped.

import (
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
)

type offerMark struct {
	endpoint string
	guess    bool
	base     int
	at       time.Time // when the mailbox last received a FULL offer
}

// guessReofferAfter is how long a copy on a guessed relay is trusted to
// still be there: half the relay TTL, so the history is re-laid before
// the relay forgets it.
const guessReofferAfter = 24 * time.Hour

// legacyRouteMaxAge is the shelf life of a recorded assumption. Nothing a
// peer does refreshes a legacy route (a statement deletes it instead), so
// age since it was written is the only signal there is.
const legacyRouteMaxAge = 30 * 24 * 3600

func legacyRouteExpired(rt storage.Route, nowUnix int64) bool {
	seen := rt.LastSeen
	if rt.LearnedAt > seen {
		seen = rt.LearnedAt
	}
	return seen > 0 && nowUnix-seen > legacyRouteMaxAge
}

// offerBase answers "how many base frames does D's mailbox at E already
// hold?" — the cursor the next push starts from. Zero means a full offer.
func (r *Runtime) offerBase(tid id.TerminalID, dev id.DeviceID, ep string, guess bool, nBase int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.offers[tid][dev]
	if !ok || m.endpoint != ep || m.guess != guess {
		return 0
	}
	if m.guess && time.Since(m.at) > guessReofferAfter {
		return 0
	}
	if m.base > nBase {
		// The log is shorter than the mark (a truncation window aged
		// frames out): nothing new below the mark, offer from the end.
		return nBase
	}
	return m.base
}

// markOffered records that D's mailbox at E now holds the first nBase base
// frames. `from` is the cursor this push started at: zero means the whole
// history was just laid down, which restarts the re-offer clock.
func (r *Runtime) markOffered(tid id.TerminalID, dev id.DeviceID, ep string, guess bool, from, nBase int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.offers == nil {
		r.offers = map[id.TerminalID]map[id.DeviceID]offerMark{}
	}
	if r.offers[tid] == nil {
		r.offers[tid] = map[id.DeviceID]offerMark{}
	}
	prev, had := r.offers[tid][dev]
	at := time.Now()
	if had && from > 0 && prev.endpoint == ep && prev.guess == guess {
		at = prev.at
	}
	r.offers[tid][dev] = offerMark{endpoint: ep, guess: guess, base: nBase, at: at}
}

// aliveWindow is how recently a device must have written for the guess
// to be worth every official relay: a device silent for longer gets the
// single cheap guess.
const aliveWindow = 30 * 24 * time.Hour

// guessRelays is where a live device with no stated route is guessed:
// this node's own relay, the cycle's explicit endpoint, and every
// official relay in the registry — deduplicated, at most a handful.
// Tests override the set; the registry names production machines.
func (r *Runtime) guessRelays(syncingAt string) []string {
	if r.guessRelaysOverride != nil {
		return append([]string(nil), r.guessRelaysOverride...)
	}
	seen := map[string]bool{}
	var out []string
	add := func(ep string) {
		if ep != "" && !seen[ep] {
			seen[ep] = true
			out = append(out, ep)
		}
	}
	add(r.ResolvePersonalRelay())
	add(syncingAt)
	for _, d := range BuiltinRelayRegistry().Relays {
		add(d.Endpoint)
	}
	return out
}

// announceRoutes is "I moved": one frameless bundle carrying this device's
// current ingress into every peer's mailbox in every space it shares —
// so a relay change is known to the people it talks to within a cycle of
// happening, not when this device next has something to say. Routes went
// stale exactly that way in the beta: automatic selection moved a phone,
// the phone only read, and its peers kept writing to the old relay until
// T5. Runs off the caller's goroutine; a failure is a missed courtesy,
// the next content push repeats the statement anyway.
func (r *Runtime) announceRoutes() {
	r.mu.Lock()
	var tids []id.TerminalID
	for tid := range r.spaces {
		tids = append(tids, tid)
	}
	r.mu.Unlock()
	addr := r.ResolvePersonalRelay()
	for _, tid := range tids {
		if !r.TransportAllowed(TransportRelay, tid) {
			continue
		}
		_, _, _, _, _, _ = r.deliverSpaceAnnouncing(tid, AssetsManifests, addr, true)
	}
}

// resetOffers forgets every mark: the next push re-offers everything,
// which EventID dedup makes a no-op wherever the copy was already right.
func (r *Runtime) resetOffers() {
	r.mu.Lock()
	r.offers = nil
	r.mu.Unlock()
}
