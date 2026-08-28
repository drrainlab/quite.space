package schemas

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/geo"
)

func validSweep() *CompletedSweep {
	pmin, _ := geo.FromDegrees(46.61, 8.02)
	pmax, _ := geo.FromDegrees(46.63, 8.05)
	c := &CompletedSweep{
		Fallback: "✓ sweep · 2.7 km · 42 min", ObjectID: [16]byte{1},
		StartedAt: 1_790_000_000, EndedAt: 1_790_002_520, DistanceM: 2700,
		Result: SweepNothingFound, BBoxMin: pmin, BBoxMax: pmax,
	}
	c.TrackAsset[0] = 0xAB
	return c
}

func TestSweepCompletedRoundTrip(t *testing.T) {
	c := validSweep()
	enc, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeCompletedSweep(enc)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *c {
		t.Fatalf("round trip diverged: %+v vs %+v", got, c)
	}
}

func TestSweepCompletedRefusals(t *testing.T) {
	try := func(name string, mut func(*CompletedSweep)) {
		c := validSweep()
		mut(c)
		if _, err := c.Encode(); err == nil {
			t.Fatalf("%s encoded", name)
		}
	}
	// The vocabulary is CLOSED: the wire never parses prose, so prose
	// must never be able to arrive dressed as a result.
	try("an open-vocabulary result", func(c *CompletedSweep) { c.Result = "нашли рюкзак" })
	try("an empty result", func(c *CompletedSweep) { c.Result = "" })
	try("a missing fallback", func(c *CompletedSweep) { c.Fallback = "" })
	try("an oversized fallback", func(c *CompletedSweep) {
		c.Fallback = strings.Repeat("я", MaxSweepFallbackRunes+1)
	})
	try("reversed times", func(c *CompletedSweep) { c.EndedAt = c.StartedAt - 1 })
	try("a zero asset id", func(c *CompletedSweep) { c.TrackAsset = [32]byte{} })
	try("a zero object id", func(c *CompletedSweep) { c.ObjectID = [16]byte{} })
}

// The asset id is 32 RAW bytes — half the hex spelling, and the biggest
// single item in a payload that has to fit a radio frame.
func TestSweepAssetIDIsRawBytes(t *testing.T) {
	enc, err := validSweep().Encode()
	if err != nil {
		t.Fatal(err)
	}
	// 64 ASCII hex chars in sequence would betray a hex encoding.
	hexy := 0
	for _, b := range enc {
		if b >= '0' && b <= '9' || b >= 'a' && b <= 'f' {
			hexy++
		} else {
			hexy = 0
		}
		if hexy >= 64 {
			t.Fatal("the asset id appears to be hex-encoded on the wire")
		}
	}
}
