package node

// The outbox — the sender's own lane (LT-3).
//
// LT-2 made the receiving side fast and left the sending side where it
// was: inside the one serial sync cycle. A kick from Say is buffered
// one deep and served when the cycle in flight ends, and a cycle on a
// node that reads public spaces ends after every projection it follows
// has been fetched — megabytes, tens of seconds on cellular. The word
// waited behind the reading. And a push that failed (a pooled socket
// dead in silence after a network change, a peer's relay cooling down)
// was retried on the next TICK: two seconds with somebody looking, a
// minute or three without.
//
// The outbox is one goroutine that does nothing but push. A kick runs a
// push pass at once; a pass that failed on transport schedules its own
// retry, doubling from outboxRetryMin to outboxRetryMax while there is
// something unsent, and stops the moment a pass succeeds. The cycle
// keeps pushing too — the two share pushSpaces under one mutex, so a
// pass that starts later sees the cursor the earlier one advanced.
// Holds are not failures: "no route yet" waits for knowledge the cycle
// gathers, and the outbox does not spin on it.

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

const (
	outboxRetryMin = 5 * time.Second
	outboxRetryMax = 60 * time.Second
)

// outboxBackoff is the wait before the nth consecutive retry.
func outboxBackoff(streak int) time.Duration {
	if streak < 1 {
		streak = 1
	}
	w := outboxRetryMin << min(streak-1, 8)
	if w > outboxRetryMax {
		w = outboxRetryMax
	}
	return w
}

// noteSaid marks a space as having a fresh local word: the next outbox pass
// pushes THAT space, first and alone. The pass used to walk every space the
// node has — the same walk the cycle makes — and on a fresh process, where
// nothing remembers what any peer already holds, that walk re-offers whole
// histories to every member of every space; the word waited its turn and
// then waited for its own space's history (owner's phone, 1.0.26-rc11: the
// relay mark seconds to minutes late). The walk is the cycle's job.
func (r *Runtime) noteSaid(tid id.TerminalID) {
	r.saidMu.Lock()
	if r.said == nil {
		r.said = map[id.TerminalID]struct{}{}
	}
	r.said[tid] = struct{}{}
	r.saidMu.Unlock()
}

// takeSaid drains the set.
func (r *Runtime) takeSaid() map[id.TerminalID]struct{} {
	r.saidMu.Lock()
	defer r.saidMu.Unlock()
	out := r.said
	r.said = nil
	return out
}

// outboxSpaces is what one pass pushes: the spaces a word was just said in
// when there are any, every space otherwise (a retry, a kick from media).
func (r *Runtime) outboxSpaces() []syncSpace {
	all := r.snapshotSyncSpaces()
	said := r.takeSaid()
	if len(said) == 0 {
		return all
	}
	out := make([]syncSpace, 0, len(said))
	for _, sp := range all {
		if _, ok := said[sp.tid]; ok {
			out = append(out, sp)
		}
	}
	return out
}

// kickOutbox asks for a push pass now. Buffered one deep: a kick while
// one is pending is the same kick, and a kick during a pass runs one
// more pass after it — which is exactly the word said meanwhile.
func (r *Runtime) kickOutbox() {
	select {
	case r.outboxKick <- struct{}{}:
	default:
	}
}

// outboxPass is one pass of the outbox as the diagnostics screen and the
// log see it (LT-4 S7): counts and a duration, never a space.
type outboxPass struct {
	at      time.Time
	took    time.Duration
	spaces  int
	pushed  int
	held    int
	express int // mailboxes served on the express lane this pass
	bulk    int // mailboxes served on the bulk lane this pass
	streak  int // consecutive failed passes, 0 when the last one went
	lastErr string
}

// OutboxDiag is the outbox's line on the diagnostics screen.
type OutboxDiag struct {
	// LastPassMs is how long the last pass took; LastPassAgoS how long
	// ago it ended. -1 while no pass has run since open.
	LastPassMs   int64  `json:"last_pass_ms"`
	LastPassAgoS int64  `json:"last_pass_ago_s"`
	Spaces       int    `json:"spaces"`
	Pushed       int    `json:"pushed"`
	Held         int    `json:"held"`
	Express      int    `json:"express"`
	Bulk         int    `json:"bulk"`
	Streak       int    `json:"streak,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	// Since open: how many mailboxes rode which lane. A word should show
	// up here as an express count; bulk that keeps climbing in a quiet
	// space is the offer book failing to remember.
	ExpressTotal int64 `json:"express_total"`
	BulkTotal    int64 `json:"bulk_total"`
}

func (r *Runtime) outboxDiag() OutboxDiag {
	r.outboxMu2.Lock()
	p := r.outboxLast
	r.outboxMu2.Unlock()
	d := OutboxDiag{LastPassMs: -1, LastPassAgoS: -1,
		ExpressTotal: r.outboxExpress.Load(), BulkTotal: r.outboxBulk.Load()}
	if !p.at.IsZero() {
		d.LastPassMs = p.took.Milliseconds()
		d.LastPassAgoS = int64(time.Since(p.at) / time.Second)
		d.Spaces, d.Pushed, d.Held = p.spaces, p.pushed, p.held
		d.Express, d.Bulk, d.Streak, d.LastError = p.express, p.bulk, p.streak, p.lastErr
	}
	return d
}

func (r *Runtime) runOutbox(addr string, stop chan struct{}) {
	defer r.wg.Done()
	var retry <-chan time.Time
	var lastHeldLog, lastFailLog time.Time
	streak := 0
	for {
		select {
		case <-r.stop:
			return
		case <-stop:
			return
		case <-r.outboxKick:
		case <-retry:
		}
		retry = nil
		spaces := r.outboxSpaces()
		t0 := time.Now()
		e0, b0 := r.outboxExpress.Load(), r.outboxBulk.Load()
		pushed, failed, held := r.pushSpacesVia(addr, spaces, true)
		pass := outboxPass{at: time.Now(), took: time.Since(t0), spaces: len(spaces), pushed: pushed,
			held: len(held), express: int(r.outboxExpress.Load() - e0), bulk: int(r.outboxBulk.Load() - b0),
			lastErr: failed}
		if failed != "" {
			pass.streak = streak + 1
		}
		r.outboxMu2.Lock()
		r.outboxLast = pass
		r.outboxMu2.Unlock()
		// One line per pass, always: how long a word took to leave is the
		// number this file exists for. Counts and a duration; no space.
		log.Printf("outbox: pass spaces=%d pushed=%d express=%d bulk=%d held=%d failed=%q took=%s",
			len(spaces), pushed, pass.express, pass.bulk, len(held), failed, pass.took.Round(time.Millisecond))
		if failed == "" {
			if len(held) > 0 && time.Since(lastHeldLog) > time.Minute {
				// A hold is not a failure, and it is also not nothing: a
				// message sitting on "sent" for minutes is one of these,
				// and until now no log line said which. Once a minute.
				lastHeldLog = time.Now()
				log.Printf("outbox: %d space(s) held: %s", len(held), heldSummary(held))
			}
			streak = 0
			continue
		}
		if time.Since(lastFailLog) > 30*time.Second {
			lastFailLog = time.Now()
			log.Printf("outbox: push to %s failed: %s", addr, failed)
		}
		streak++
		if streak == 1 {
			// A WORD IS PROOF SOMEBODY IS HERE. The first failure after a
			// quiet spell is almost always the network having been away
			// and come back: sockets Doze left half-open, a breaker that
			// charged "network unreachable" while the phone slept and
			// would refuse to dial for minutes more. Measured on the
			// owner's phone (1.0.26-rc1): a message sat unsent for over
			// four minutes after waking. So the first retry is not a wait
			// — it is the reset a noticed sleep gets, and one more try at
			// once, on a fresh connection. Only then does the ladder start.
			r.onWake()
			_, failed, _ = r.pushSpacesVia(addr, spaces, true)
			if failed == "" {
				r.outboxMu2.Lock()
				r.outboxLast.streak, r.outboxLast.lastErr = 0, ""
				r.outboxMu2.Unlock()
				streak = 0
				continue
			}
		}
		retry = time.After(outboxBackoff(streak))
	}
}

// heldSummary names the hold reasons present, for the log — kinds and
// counts, never a space.
func heldSummary(held map[id.TerminalID]heldReason) string {
	counts := map[string]int{}
	for _, h := range held {
		counts[h.reason]++
	}
	parts := make([]string, 0, len(counts))
	for k, n := range counts {
		parts = append(parts, fmt.Sprintf("%s×%d", k, n))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}
