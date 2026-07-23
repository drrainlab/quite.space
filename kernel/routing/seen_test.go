package routing

import (
	"path/filepath"
	"testing"
	"time"
)

func key(b byte) SeenKey {
	var k SeenKey
	k[0] = b
	return k
}

func TestSeenDupAndTTL(t *testing.T) {
	c := NewSeenCache(100, time.Hour)
	now := time.Unix(1_784_000_000, 0)
	if c.Seen(key(1), now) {
		t.Fatal("first sight must not report seen")
	}
	if !c.Seen(key(1), now.Add(time.Minute)) {
		t.Fatal("second sight must report seen")
	}
	// After TTL the entry no longer counts as seen (re-forward allowed;
	// ingest dedup remains the final authority).
	if c.Seen(key(1), now.Add(2*time.Hour)) {
		t.Fatal("expired entry must not report seen")
	}
}

func TestSeenCapEviction(t *testing.T) {
	c := NewSeenCache(64, 0)
	now := time.Unix(1_784_000_000, 0)
	for i := 0; i < 200; i++ {
		var k SeenKey
		k[0], k[1] = byte(i>>8), byte(i)
		c.Seen(k, now.Add(time.Duration(i)*time.Second))
	}
	if c.Len() > 64 {
		t.Fatalf("cap not enforced: %d", c.Len())
	}
	// The most recent keys survive.
	var last SeenKey
	last[0], last[1] = 0, 199
	if !c.Seen(last, now.Add(300*time.Second)) {
		t.Fatal("most recent entry evicted")
	}
}

func TestSeenSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seen.snap")
	now := time.Unix(1_784_000_000, 0)

	c := NewSeenCache(100, time.Hour)
	c.Seen(key(1), now)
	c.Seen(key(2), now)
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}

	c2 := NewSeenCache(100, time.Hour)
	if err := c2.Load(path, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !c2.Seen(key(1), now.Add(time.Minute)) || !c2.Seen(key(2), now.Add(time.Minute)) {
		t.Fatal("snapshot lost entries")
	}
	// Entries expired while down are dropped on load.
	c3 := NewSeenCache(100, time.Hour)
	if err := c3.Load(path, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c3.Len() != 0 {
		t.Fatalf("expired-while-down entries must drop: %d", c3.Len())
	}
	// Corrupt snapshot → clean start, no error.
	if err := c3.Load(filepath.Join(dir, "missing.snap"), now); err != nil {
		t.Fatal("missing snapshot must be a clean start")
	}
}

// Split-horizon: same link always suppressed; same loop domain suppressed;
// same CLASS allowed (radio-A → bridge → radio-B is legal, ADR-015 §4).
func TestSplitHorizonByLinkAndDomain(t *testing.T) {
	radioA := LinkID("mesh:serial:/dev/ttyUSB0")
	radioB := LinkID("mesh:tcp:hub-east:4403")
	domA := LoopDomainID("meshtastic-quiet@usb0")
	domB := LoopDomainID("meshtastic-quiet@east")

	if !SuppressForward(radioA, radioA, domA, domA) {
		t.Fatal("same link must suppress")
	}
	if !SuppressForward(radioA, radioB, domA, domA) {
		t.Fatal("same loop domain must suppress")
	}
	if SuppressForward(radioA, radioB, domA, domB) {
		t.Fatal("different radio segments must be allowed (radio-A → radio-B)")
	}
}
