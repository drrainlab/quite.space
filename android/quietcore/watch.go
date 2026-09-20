package quietcore

// AN-2 — the keyless watch, across the gomobile seam. See node/watch.go for
// what it is and what it must never be. Two halves:
//
//   WriteWatchPlan  while the node is OPEN: write down where it will listen.
//   StartWatch      while the node is CLOSED: park there with no key, and
//                   say one thing — "something is waiting".
//
// The two never run together: a node that is open has its own listener, which
// does everything this does and then actually collects the mail.

import (
	"errors"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/node"
)

// WatchSink is implemented by the host. Called from Go goroutines.
type WatchSink interface {
	// OnMail: something is waiting or has just arrived. No space, no sender,
	// no count — the watcher does not know them.
	OnMail()
	// OnExpired: the plan has no addresses left. The watch has ended.
	OnExpired()
	// OnState: "parked" / "retrying" / "expired", for a diagnostics line.
	OnState(addr, state string)
}

var (
	watchMu   sync.Mutex
	watchStop chan struct{}
)

// WriteWatchPlan writes the plan for the next week. It fails when the node
// is closed — only an open node knows its own mailboxes.
func WriteWatchPlan(path string) error {
	stateMu.Lock()
	r := rt
	stateMu.Unlock()
	if r == nil {
		return errors.New("quietcore: the node is not open")
	}
	return node.WriteWatchPlan(path, r.BuildWatchPlan(0))
}

// StartWatch begins watching from the plan at path. It returns false when
// there is nothing to watch — no file, a plan from another version, or one
// that has already run out — and the host says so to the person.
func StartWatch(path string, sink WatchSink) bool {
	plan, err := node.ReadWatchPlan(path, time.Now())
	if err != nil || len(plan.Endpoints) == 0 || time.Now().Unix() >= plan.ExpiresAt {
		return false
	}
	watchMu.Lock()
	defer watchMu.Unlock()
	if watchStop != nil {
		return true // already watching
	}
	stop := make(chan struct{})
	watchStop = stop
	go func() {
		node.RunWatch(plan, node.WatchEvents{
			Mail:    func() { safely(sink.OnMail) },
			Expired: func() { safely(sink.OnExpired) },
			State:   func(a, s string) { safely(func() { sink.OnState(a, s) }) },
		}, stop)
		watchMu.Lock()
		if watchStop == stop {
			watchStop = nil
		}
		watchMu.Unlock()
	}()
	return true
}

// StopWatch ends the watch. Safe to call when none is running.
func StopWatch() {
	watchMu.Lock()
	defer watchMu.Unlock()
	if watchStop != nil {
		close(watchStop)
		watchStop = nil
	}
}

// WatchRunning reports whether a watch is parked or trying to park.
func WatchRunning() bool {
	watchMu.Lock()
	defer watchMu.Unlock()
	return watchStop != nil
}

// safely: a panic thrown by the host's callback must not take a Go goroutine
// — and with it the process — down.
func safely(f func()) {
	defer func() { _ = recover() }()
	f()
}
