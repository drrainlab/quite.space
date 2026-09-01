package bundle

// The first tests this codec has ever had, written the day it grew key 8.
// Two properties carry everything: a full bundle survives the round trip
// field for field, and an UNKNOWN key is skipped rather than fatal — the
// append-only contract (ADR-009) that lets a 0.1.4 node receive a bundle
// from a node that has learned new words.

import (
	"bytes"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func TestFullBundleRoundTrip(t *testing.T) {
	tid := id.TerminalID{0xAA, 0xBB}
	frames := [][]byte{[]byte("frame-1"), []byte("frame-2")}
	blobs := [][]byte{[]byte("blob")}
	wants := [][]byte{bytes.Repeat([]byte{0x11}, 32)}
	wanter := bytes.Repeat([]byte{0x22}, 32)
	replyBox := []byte("box-hint-bytes")
	routes := []string{"203.0.113.9:7411", "198.51.100.4:7411"}

	p, err := DecodeParts(EncodeWithReturnRoutes(tid, frames, blobs, wants, wanter, replyBox, routes))
	if err != nil {
		t.Fatal(err)
	}
	if p.Terminal != tid || len(p.Frames) != 2 || string(p.Frames[1]) != "frame-2" ||
		len(p.Blobs) != 1 || len(p.Wants) != 1 || !bytes.Equal(p.Wanter, wanter) ||
		!bytes.Equal(p.ReplyBox, replyBox) {
		t.Fatalf("round trip lost a field: %+v", p)
	}
	if len(p.ReturnRoutes) != 2 || p.ReturnRoutes[0] != routes[0] || p.ReturnRoutes[1] != routes[1] {
		t.Fatalf("return routes did not survive: %v", p.ReturnRoutes)
	}
}

// TestRoutesRideBesidePlainFrames — the rule this replaced was "routes
// only beside wants", and it was too narrow by one beta evening: a
// device that moved relays could not tell anybody until it happened to
// want a file. A content push may state its sender's ingress; the
// receiver's certificate gate is what keeps the claim from being free.
func TestRoutesRideBesidePlainFrames(t *testing.T) {
	tid := id.TerminalID{0x01}
	wanter := bytes.Repeat([]byte{7}, 32)
	p, err := DecodeParts(EncodeWithReturnRoutes(tid, [][]byte{[]byte("f")}, nil, nil, wanter, nil,
		[]string{"203.0.113.9:7411"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ReturnRoutes) != 1 || p.ReturnRoutes[0] != "203.0.113.9:7411" {
		t.Fatalf("a frame push lost its sender's stated ingress: %v", p.ReturnRoutes)
	}
	if !bytes.Equal(p.Wanter, wanter) {
		t.Fatal("the statement lost its author")
	}
}

// TestAnUnknownKeyIsSkippedNotFatal — the compat contract itself. A
// future key (9 when this was written; 9 became receipts, so now 99)
// must decode on today's code with every known field intact, or the
// network cannot ever learn another word.
func TestAnUnknownKeyIsSkippedNotFatal(t *testing.T) {
	tid := id.TerminalID{0x0F}
	// Hand-build a bundle with an extra key 99 carrying a nested array —
	// the shape most likely to trip a lazy skip.
	buf := []byte(magic)
	buf = codec.AppendMap(buf, 4)
	buf = codec.AppendUint(buf, keyVersion)
	buf = codec.AppendUint(buf, version)
	buf = codec.AppendUint(buf, keyTerminal)
	buf = codec.AppendBytes(buf, tid[:])
	buf = codec.AppendUint(buf, keyFrames)
	buf = codec.AppendArray(buf, 1)
	buf = codec.AppendBytes(buf, []byte("frame"))
	buf = codec.AppendUint(buf, 99) // a word this decoder has never heard
	buf = codec.AppendArray(buf, 2)
	buf = codec.AppendText(buf, "future")
	buf = codec.AppendArray(buf, 1)
	buf = codec.AppendUint(buf, 42)

	p, err := DecodeParts(buf)
	if err != nil {
		t.Fatalf("an unknown key was fatal: %v", err)
	}
	if p.Terminal != tid || len(p.Frames) != 1 || string(p.Frames[0]) != "frame" {
		t.Fatalf("skipping the unknown key damaged known fields: %+v", p)
	}
}
