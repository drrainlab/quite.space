package storage

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

// An external record (QI-M) carries public halves and no seeds, and an
// internal one the reverse; both survive the ring byte-exactly, and a
// record with the OLD two-item simulator array still reads.
func TestInstrumentRecordRoundTripsBothShapes(t *testing.T) {
	ext := InstrumentRecord{Label: "Greenhouse", Kind: "sensor", Channels: []string{"qp.instr=t:number"},
		ManifestFrame: []byte{1, 2}, External: true}
	ext.Space[0], ext.DevicePub[0], ext.X25519Pub[0], ext.TerminalPub[0] = 1, 2, 3, 4
	in := InstrumentRecord{Label: "Sim", Kind: "sensor", DeviceSeed: make([]byte, 32),
		TerminalSeed: make([]byte, 32), ManifestFrame: []byte{9}, Simulated: true, SimSeed: 42}
	for _, want := range []InstrumentRecord{ext, in} {
		buf := appendInstrumentRecord(nil, want)
		got, err := readInstrumentRecord(codec.NewDecoder(buf))
		if err != nil {
			t.Fatal(err)
		}
		if got.External != want.External || got.DevicePub != want.DevicePub || got.TerminalPub != want.TerminalPub ||
			got.X25519Pub != want.X25519Pub || got.SimSeed != want.SimSeed || got.Label != want.Label ||
			!got.Exists() {
			t.Fatalf("round trip diverged:\n got %+v\nwant %+v", got, want)
		}
	}
	// The pre-QI-M shape: a two-item simulator array.
	old := codec.AppendArray(nil, instrFields)
	old = codec.AppendBytes(old, make([]byte, 32))
	old = codec.AppendText(old, "Old")
	old = codec.AppendText(old, "sensor")
	old = codec.AppendArray(old, 0)
	old = codec.AppendBytes(old, make([]byte, 32))
	old = codec.AppendBytes(old, make([]byte, 32))
	old = codec.AppendBytes(old, make([]byte, 32))
	old = codec.AppendBytes(old, []byte{7})
	old = codec.AppendArray(old, 2)
	old = codec.AppendBool(old, true)
	old = codec.AppendUint(old, 5)
	got, err := readInstrumentRecord(codec.NewDecoder(old))
	if err != nil || !got.Simulated || got.SimSeed != 5 || got.External {
		t.Fatalf("old shape: %+v err=%v", got, err)
	}
}
