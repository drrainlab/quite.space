package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
)

func testHold(t *testing.T, dir string, max int) *IngressHold {
	t.Helper()
	root, err := Open(dir, []byte("a decent passphrase"))
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	h, err := root.OpenIngressHold(max)
	if err != nil {
		t.Fatalf("open hold: %v", err)
	}
	return h
}

// A destructive transport already forgot this frame, so losing it across a
// restart is losing it for good. The proof is not "a round trip works" but
// "a SECOND process finds the exact bytes the first one took custody of".
func TestHeldIngressSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	raw := []byte("an envelope, opaque at this layer, byte for byte")

	first := testHold(t, dir, 16)
	if _, err := first.Put(raw, HeldIngressMeta{ReceivedAt: 1755400000, Source: IngressRelay}); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Nothing is carried over in memory: a different Root, a different hold.
	second := testHold(t, dir, 16)
	got, err := second.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("held frames after restart = %d, want 1", len(got))
	}
	if !bytes.Equal(got[0].Raw, raw) {
		t.Fatalf("raw bytes changed across the restart:\n got %q\nwant %q", got[0].Raw, raw)
	}
	if got[0].ID != id.HashOf(raw) {
		t.Fatalf("hold id is not the hash of the raw bytes")
	}
	if got[0].Meta.ReceivedAt != 1755400000 || got[0].Meta.Source != IngressRelay {
		t.Fatalf("diagnostics lost: %+v", got[0].Meta)
	}
}

// Relay and LAN may both yield the same bytes, and a repeat must not eat a
// second slot: capacity is what stops us collecting more than we can keep,
// so double-counting it would throttle ingress for no reason.
func TestHoldingTheSameRawFrameTwiceIsIdempotent(t *testing.T) {
	h := testHold(t, t.TempDir(), 16)
	raw := []byte("the very same frame, arriving twice")

	firstID, err := h.Put(raw, HeldIngressMeta{ReceivedAt: 100, Source: IngressRelay})
	if err != nil {
		t.Fatalf("first put: %v", err)
	}
	secondID, err := h.Put(raw, HeldIngressMeta{ReceivedAt: 200, Source: IngressLAN})
	if err != nil {
		t.Fatalf("second put: %v", err)
	}
	if firstID != secondID {
		t.Fatalf("the same bytes got two ids")
	}
	if h.Count() != 1 {
		t.Fatalf("count = %d after holding one frame twice, want 1", h.Count())
	}
	got, err := h.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("held = %d, want 1", len(got))
	}
	// FIRST custody is the true one: that is when the bytes became ours, and
	// held_too_long is measured from it. A later duplicate must not refresh
	// the clock, or a peer that keeps re-sending could hide its own age.
	if got[0].Meta.ReceivedAt != 100 {
		t.Fatalf("ReceivedAt = %d, want the first custody (100)", got[0].Meta.ReceivedAt)
	}
}

func TestDeleteRemovesTheFrameAndItsDiagnostics(t *testing.T) {
	dir := t.TempDir()
	h := testHold(t, dir, 16)
	raw := []byte("applied, therefore no longer held")
	hid, err := h.Put(raw, HeldIngressMeta{ReceivedAt: 1, Source: IngressRadio})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := h.Delete(hid); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if h.Count() != 0 {
		t.Fatalf("count = %d after delete, want 0", h.Count())
	}
	ents, err := os.ReadDir(filepath.Join(dir, "ingress-hold"))
	if err != nil {
		t.Fatalf("read hold dir: %v", err)
	}
	if len(ents) != 0 {
		t.Fatalf("delete left %d file(s) behind: %v", len(ents), ents)
	}
	// Deleting what is already gone is the desired state, not a failure —
	// the crash boundary replays a delete after a restart.
	if err := h.Delete(hid); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

// Capacity is a question asked BEFORE the destructive take. A relay drain
// addresses a whole mailbox and the byte budget is the server's, so one reply
// can overshoot — and at that moment the bytes are already ours. Refusing
// them here would be the very loss the hold exists to prevent.
func TestAFullHoldStillKeepsWhatWasAlreadyTakenFromTheRelay(t *testing.T) {
	h := testHold(t, t.TempDir(), 2)
	for i, raw := range [][]byte{[]byte("one"), []byte("two")} {
		if _, err := h.Put(raw, HeldIngressMeta{ReceivedAt: int64(i)}); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	// This is what the sync tick must consult before it collects at all.
	if got := h.RemainingItems(); got != 0 {
		t.Fatalf("RemainingItems = %d on a full hold, want 0", got)
	}
	if h.OverCapacity() {
		t.Fatalf("a hold at exactly its bound is full, not over capacity")
	}

	// A drain happened anyway (or overshot): the bytes are kept.
	if _, err := h.Put([]byte("three"), HeldIngressMeta{ReceivedAt: 3}); err != nil {
		t.Fatalf("a frame already taken from the relay was refused: %v", err)
	}
	got, err := h.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("held = %d, want 3 — the overshoot was dropped", len(got))
	}
	if !h.OverCapacity() {
		t.Fatalf("the overshoot is not being reported")
	}
	if got := h.RemainingItems(); got != 0 {
		t.Fatalf("RemainingItems = %d past the bound, want 0 (never negative)", got)
	}
}

// Meta is diagnostics, never authority. A crash between the two writes must
// therefore cost the diagnostics and never the bytes.
func TestAFrameWithoutItsMetaIsStillHeld(t *testing.T) {
	dir := t.TempDir()
	h := testHold(t, dir, 16)
	raw := []byte("bytes whose diagnostics did not make it to disk")
	hid, err := h.Put(raw, HeldIngressMeta{ReceivedAt: 42, Source: IngressRelay})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "ingress-hold", hid.Hex()+".meta")); err != nil {
		t.Fatalf("remove meta: %v", err)
	}
	got, err := h.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || !bytes.Equal(got[0].Raw, raw) {
		t.Fatalf("a frame was dropped because its diagnostics were missing")
	}
}

// Corruption must fail closed rather than hand up bytes that are no longer
// the ones a transport yielded — the replay's whole promise is exact bytes.
func TestCorruptedHeldBytesAreNotServed(t *testing.T) {
	dir := t.TempDir()
	h := testHold(t, dir, 16)
	raw := []byte("bytes that will be tampered with on disk")
	hid, err := h.Put(raw, HeldIngressMeta{ReceivedAt: 7})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	path := filepath.Join(dir, "ingress-hold", hid.Hex()+".frame")
	if err := os.WriteFile(path, []byte("not what was stored"), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if _, err := h.List(); err == nil {
		t.Fatalf("List served bytes that do not match their hash")
	}
}
