package storage

import (
	"bytes"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

func TestSweepRecordRoundTrip(t *testing.T) {
	r := SweepRecord{
		Label: "Sweep 04 — western edge", StartedAt: 1_790_000_000,
		State: SweepStopped, Result: "nothing_found", Note: "чисто, следов нет",
		StoppedAt: 1_790_002_520, ObjectCreated: true, AssetHex: "ab12",
		BlockEventID: []byte{1, 2}, EdgeEventID: []byte{3},
		CompletedEventID: []byte{4, 5, 6}, CardDone: true,
		NoteEventID: []byte{7}, ObjectRevised: true,
		TaskID: []byte{9, 9},
	}
	r.Space[0], r.SweepID[0], r.ParentID[0] = 0xA, 0xB, 0xC

	buf := appendSweepRecord(nil, r)
	got, err := readSweepRecord(codec.NewDecoder(buf))
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != r.Label || got.State != r.State || got.Result != r.Result ||
		got.Note != r.Note || got.StoppedAt != r.StoppedAt ||
		got.ObjectCreated != r.ObjectCreated || got.AssetHex != r.AssetHex ||
		!bytes.Equal(got.CompletedEventID, r.CompletedEventID) ||
		got.CardDone != r.CardDone || got.ObjectRevised != r.ObjectRevised ||
		got.SweepID != r.SweepID || got.ParentID != r.ParentID {
		t.Fatalf("round trip diverged:\n got %+v\nwant %+v", got, r)
	}
}

// The tail is append-only forever: a record with MORE tail items than
// this build knows must read cleanly, and one with FEWER (an older
// writer) must leave the newer fields zero rather than erroring.
func TestSweepRecordTailCompat(t *testing.T) {
	r := SweepRecord{Label: "x", StartedAt: 1, State: SweepRecording}
	r.SweepID[0] = 1
	buf := appendSweepRecord(nil, r)
	// A future writer appends an 11th tail item: rebuild with a larger
	// tail by hand — swap the tail arity byte and append one item.
	// Simpler: decode must tolerate a record whose tail has extras, so
	// append a fresh record with a manually enlarged tail.
	long := codec.AppendArray(nil, sweepFields)
	long = codec.AppendBytes(long, r.Space[:])
	long = codec.AppendBytes(long, r.SweepID[:])
	long = codec.AppendBytes(long, r.ParentID[:])
	long = codec.AppendBytes(long, nil)
	long = codec.AppendText(long, r.Label)
	long = codec.AppendUint(long, r.StartedAt)
	long = codec.AppendUint(long, r.State)
	long = codec.AppendText(long, "")
	long = codec.AppendArray(long, 11)
	long = codec.AppendText(long, "")
	long = codec.AppendUint(long, 0)
	long = codec.AppendBool(long, false)
	long = codec.AppendText(long, "")
	long = codec.AppendBytes(long, nil)
	long = codec.AppendBytes(long, nil)
	long = codec.AppendBytes(long, nil)
	long = codec.AppendBool(long, false)
	long = codec.AppendBytes(long, nil)
	long = codec.AppendBool(long, false)
	long = codec.AppendText(long, "from the future")
	got, err := readSweepRecord(codec.NewDecoder(long))
	if err != nil {
		t.Fatalf("a future tail broke the reader: %v", err)
	}
	if got.SweepID != r.SweepID {
		t.Fatal("identity lost under a future tail")
	}
	_ = buf
}
