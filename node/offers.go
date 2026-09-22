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
// The book itself is DURABLE and keyed by mailbox since LT-4 S5 — see
// offerbook.go for the three rules (log-index cursor, cursor moves only
// after PutOK, a full re-offer a day after the last one). This file keeps
// the two thin doors the push uses and the legacy-route shelf life.

import (
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
)

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
// offerBase is the log index the next push to this mailbox may start from;
// n is the log's length now. The durable book (offerbook.go) answers.
func (r *Runtime) offerBase(tid id.TerminalID, dev id.DeviceID, ep string, n int) int {
	return r.offerBook.cursor(offerKey{tid, dev, ep}, n)
}

// markOffered records, AFTER the relay accepted every body, that frames
// with index < n are in this mailbox now.
func (r *Runtime) markOffered(tid id.TerminalID, dev id.DeviceID, ep string, guess, legacy bool, from, n int) {
	r.offerBook.mark(offerKey{tid, dev, ep}, from, n, guess, legacy)
}

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
	own := r.ownWorld()
	add := func(ep string) {
		if ep != "" && !seen[ep] && routableFrom(ep, own) {
			seen[ep] = true
			out = append(out, ep)
		}
	}
	add(own)
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
		_, _, _, _, _, _, _ = r.deliverSpaceAnnouncing(tid, AssetsManifests, addr, true)
	}
}

// resetOffers forgets every mark: the next push re-offers everything,
// which EventID dedup makes a no-op wherever the copy was already right.
// resetOffers forgets every mark — a test's way of losing all knowledge of
// what any mailbox holds. Nothing in the product calls it any more: a
// route change starts a fresh cursor by itself, because a mark is keyed by
// the mailbox it names.
func (r *Runtime) resetOffers() {
	r.offerBook.forgetAll()
}
