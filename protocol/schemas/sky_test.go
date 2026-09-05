package schemas

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
)

func TestSkyBlockRoundTripAndFallback(t *testing.T) {
	p, err := (&SkyBlock{Title: "nebula"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	b, err := DecodeSkyBlock(p)
	if err != nil || b.Title != "nebula" {
		t.Fatalf("round trip: %+v %v", b, err)
	}
	// A client that has never heard of skies reads the fallback line.
	fb, err := DecodeBlockFallback(p)
	if err != nil || fb != "✦ nebula" {
		t.Fatalf("fallback: %q %v", fb, err)
	}
}

func TestSkyStrokeLimitsAreTheWire(t *testing.T) {
	sky := id.EventID{7}
	ok := &SkyStrokeEvent{Sky: sky, Points: []byte{1, 2, 3, 4}, Bright: 3}
	p, err := ok.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeSkyStroke(p)
	if err != nil || back.Sky != sky || len(back.Points) != 4 || back.Bright != 3 || back.IsErase() {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	for _, bad := range []*SkyStrokeEvent{
		{Sky: sky},                                            // draws nothing, erases nothing
		{Sky: sky, Points: []byte{1}},                         // odd
		{Sky: sky, Points: []byte{1, SkyGrid}},                // off the grid
		{Sky: sky, Points: make([]byte, 2*MaxStrokePoints+2)}, // too long
		{Sky: sky, Points: []byte{1, 1}, Bright: SkyBrightMax + 1},
		{Sky: sky, Erase: make([]id.EventID, MaxEraseTargets+1)},
	} {
		if _, err := bad.Encode(); err == nil {
			t.Fatalf("encoded a stroke that should have been refused: %+v", bad)
		}
	}
	er := &SkyStrokeEvent{Sky: sky, Erase: []id.EventID{{1}, {2}}}
	p, err = er.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err = DecodeSkyStroke(p)
	if err != nil || !back.IsErase() || len(back.Erase) != 2 {
		t.Fatalf("erase round trip: %+v %v", back, err)
	}
}
