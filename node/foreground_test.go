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

// LT-3: for a shell that asked for it, attention is the window's focus OR
// the interface having asked the node something lately — a window read
// on a second monitor stays live; a window hidden in the tray, polling
// nothing, goes to the background one window later.
func TestAttentionIsTheWindowOrTheAPI(t *testing.T) {
	old := attentionWindow
	attentionWindow = 60 * time.Millisecond
	defer func() { attentionWindow = old }()

	r := &Runtime{syncKick: make(chan struct{}, 1)}
	r.EnableAttentionFromAPI()
	if r.foregrounded() {
		t.Fatal("with attention from the API, a node nobody asked starts in the background")
	}
	r.NoteAttention()
	if !r.foregrounded() {
		t.Fatal("the API was just used — somebody is looking")
	}
	r.SetForeground(true) // the window took the focus
	time.Sleep(3 * attentionWindow)
	if !r.foregrounded() {
		t.Fatal("the API window closed but the window still has the focus")
	}
	r.SetForeground(false) // clicked into an editor; the page still polls
	if r.foregrounded() {
		t.Fatal("no focus and no recent API use is nobody looking")
	}
	r.NoteAttention()
	if !r.foregrounded() {
		t.Fatal("the unfocused window polled — it is being read")
	}
	time.Sleep(3 * attentionWindow)
	if r.foregrounded() {
		t.Fatal("the polls stopped and the focus is elsewhere: the background minute")
	}
}

// A media answer keeps the node awake for the next ask: the background
// heartbeat shortens to servingCadence for servingWindow, and only then.
func TestAnsweringKeepsTheNodeAwakeForTheNextAsk(t *testing.T) {
	r := &Runtime{syncKick: make(chan struct{}, 1)}
	base := 2 * time.Second
	r.SetForeground(false)
	if got := r.syncInterval(base); got != base*backgroundMultiplier {
		t.Fatalf("background interval %v before any answer", got)
	}
	r.noteServing()
	if got := r.syncInterval(base); got != servingCadence {
		t.Fatalf("with an answer in flight the interval is %v, want %v", got, servingCadence)
	}
	r.SetForeground(true)
	if got := r.syncInterval(base); got != base {
		t.Fatalf("foreground is never slowed by serving: %v", got)
	}
	r.SetForeground(false)
	r.servingUntil.Store(time.Now().Add(-time.Second).UnixNano())
	if got := r.syncInterval(base); got != base*backgroundMultiplier {
		t.Fatalf("after the window the background minute is back, got %v", got)
	}
}
