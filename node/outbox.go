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

import "time"

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

// kickOutbox asks for a push pass now. Buffered one deep: a kick while
// one is pending is the same kick, and a kick during a pass runs one
// more pass after it — which is exactly the word said meanwhile.
func (r *Runtime) kickOutbox() {
	select {
	case r.outboxKick <- struct{}{}:
	default:
	}
}

func (r *Runtime) runOutbox(addr string, stop chan struct{}) {
	defer r.wg.Done()
	var retry <-chan time.Time
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
		_, failed, _ := r.pushSpaces(addr, r.snapshotSyncSpaces())
		if failed == "" {
			streak = 0
			continue
		}
		streak++
		retry = time.After(outboxBackoff(streak))
	}
}
