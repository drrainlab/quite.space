package node

// The attention seam's two promises: the background heartbeat is a
// multiple worth the radio's while, and coming back is URGENT — the sync
// wakes before the screen finishes doing so.

import (
	"testing"
	"time"
)

func TestBackgroundStretchesTheHeartbeat(t *testing.T) {
	r := &Runtime{}
	base := 2 * time.Second

	if got := r.syncInterval(base); got != base {
		t.Fatalf("a fresh runtime is foregrounded by default, got interval %v", got)
	}
	r.SetForeground(false)
	if got := r.syncInterval(base); got != base*backgroundMultiplier {
		t.Fatalf("backgrounded interval %v, want %v", got, base*backgroundMultiplier)
	}
	if r.foregrounded() {
		t.Fatal("backgrounded runtime claims to be watched")
	}
	r.SetForeground(true)
	if got := r.syncInterval(base); got != base {
		t.Fatalf("foreground did not restore the shipped heartbeat: %v", got)
	}
}

func TestReturningToForegroundKicksTheSync(t *testing.T) {
	r := &Runtime{syncKick: make(chan struct{}, 1)}
	r.SetForeground(false)
	select {
	case <-r.syncKick:
		t.Fatal("leaving the foreground kicked the sync — leaving is not urgent")
	default:
	}
	r.SetForeground(true)
	select {
	case <-r.syncKick:
	default:
		t.Fatal("returning to the foreground did not kick the sync")
	}
	// Idempotent: saying the same state twice is not two events.
	r.SetForeground(true)
	select {
	case <-r.syncKick:
		t.Fatal("a repeated foreground kicked again")
	default:
	}
}
