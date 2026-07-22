package schemas

import (
	"bytes"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func testRef(chunks int) *AssetRef {
	r := &AssetRef{
		Role: "original", MediaType: "image/jpeg", Size: uint64(chunks) * 4096,
		ChunkSize: 4096,
	}
	r.AssetID[0] = 0xA1
	r.Key[0] = 0xB2
	r.PlaintextDigest[0] = 0xC3
	if chunks <= InlineChunkMax {
		for i := 0; i < chunks; i++ {
			var h id.Hash
			h[0] = byte(i + 1)
			r.InlineChunks = append(r.InlineChunks, h)
		}
	} else {
		var m id.Hash
		m[0] = 0xEE
		r.ManifestWireID = &m
		r.ManifestVer = 1
	}
	return r
}

func TestVisualBlockRoundTrip(t *testing.T) {
	b := &VisualBlock{
		Caption: "north ridge at dawn", Alt: "foggy pines on a ridge",
		ThumbMIME: "image/webp", Thumb: []byte{1, 2, 3},
		Original: testRef(3),
	}
	enc, err := b.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeVisualBlock(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Alt != b.Alt || got.Caption != b.Caption || !bytes.Equal(got.Thumb, b.Thumb) {
		t.Fatalf("mismatch: %+v", got)
	}
	if got.Original.MediaType != "image/jpeg" || len(got.Original.InlineChunks) != 3 {
		t.Fatalf("asset ref mangled: %+v", got.Original)
	}
	// Universal fallback = alt.
	fb, err := DecodeBlockFallback(enc)
	if err != nil || fb != b.Alt {
		t.Fatalf("fallback: %q %v", fb, err)
	}
	// Alt is mandatory.
	b2 := &VisualBlock{Alt: "", Original: testRef(1)}
	if _, err := b2.Encode(); err == nil {
		t.Fatal("visual without alt accepted")
	}
}

// The forward-compatibility guarantee, tested directly: a block type from
// the future — unknown schema, unknown keys, unknown nested structures —
// still yields its fallback text via generic decode.
func TestUnknownBlockFallbackSurvives(t *testing.T) {
	buf := codec.AppendMap(nil, 4)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, "~ aurora field · 7 voices")
	buf = codec.AppendUint(buf, 7)
	inner := codec.AppendMap(nil, 1) // nested unknown structure
	inner = codec.AppendUint(inner, 99)
	inner = codec.AppendBytes(inner, []byte{9, 9, 9})
	buf = append(buf, inner...)
	buf = codec.AppendUint(buf, 12)
	buf = codec.AppendArray(buf, 2)
	buf = codec.AppendText(buf, "future")
	buf = codec.AppendUint(buf, 42)
	buf = codec.AppendUint(buf, 900)
	buf = codec.AppendBool(buf, true)

	fb, err := DecodeBlockFallback(buf)
	if err != nil {
		t.Fatal(err)
	}
	if fb != "~ aurora field · 7 voices" {
		t.Fatalf("fallback mangled: %q", fb)
	}
	// And a block WITHOUT key 1 is invalid for the whole family.
	noFb := codec.AppendMap(nil, 1)
	noFb = codec.AppendUint(noFb, 2)
	noFb = codec.AppendText(noFb, "content")
	if _, err := DecodeBlockFallback(noFb); err == nil {
		t.Fatal("block without fallback accepted")
	}
}

func TestVoiceAndAudioBlocks(t *testing.T) {
	v := &VoiceBlock{DurationMS: 84000, Waveform: bytes.Repeat([]byte{7}, 48),
		Transcript: "meet at the ridge", Language: "en", Original: testRef(2)}
	enc, err := v.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeVoiceBlock(enc)
	if err != nil || got.Transcript != v.Transcript || len(got.Waveform) != 48 {
		t.Fatalf("voice: %+v %v", got, err)
	}
	if fb, _ := DecodeBlockFallback(enc); fb != "meet at the ridge" {
		t.Fatalf("voice fallback should be transcript, got %q", fb)
	}
	// Without transcript the fallback is honest metadata.
	v2 := &VoiceBlock{DurationMS: 84000, Original: testRef(1)}
	enc2, _ := v2.Encode()
	if fb, _ := DecodeBlockFallback(enc2); fb != "Voice message · 01:24" {
		t.Fatalf("voice fallback: %q", fb)
	}

	a := &AudioBlock{Title: "field recording 12", BPM: 92, Loop: true,
		DurationMS: 198000, Original: testRef(20)}
	encA, err := a.Encode()
	if err != nil {
		t.Fatal(err)
	}
	gotA, err := DecodeAudioBlock(encA)
	if err != nil || gotA.BPM != 92 || !gotA.Loop || gotA.Original.ManifestWireID == nil {
		t.Fatalf("audio: %+v %v", gotA, err)
	}
	// Oversized waveform rejected.
	bad := &VoiceBlock{DurationMS: 1000, Waveform: make([]byte, 65), Original: testRef(1)}
	if _, err := bad.Encode(); err == nil {
		t.Fatal("oversized waveform accepted")
	}
}

func TestFileAndLinkBlocks(t *testing.T) {
	f := &FileBlock{Filename: "../../etc/passwd\x00.pdf", MediaType: "application/pdf",
		Size: 123456, Original: testRef(4)}
	enc, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeFileBlock(enc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(got.Filename, "/\\\x00") {
		t.Fatalf("filename not normalized: %q", got.Filename)
	}

	l := &LinkBlock{URL: "https://quiet.example/post/7", Title: "a quiet post"}
	encL, err := l.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeLinkBlock(encL); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"javascript:alert(1)", "data:text/html,hi", "file:///etc/passwd", ""} {
		if _, err := (&LinkBlock{URL: bad}).Encode(); err == nil {
			t.Fatalf("dangerous url accepted: %q", bad)
		}
	}
	// SVG preview rejected (a preview is pixels, not a document).
	badPrev := &LinkBlock{URL: "https://ok.example/", ThumbMIME: "image/svg+xml", Thumb: []byte("<svg>")}
	if _, err := badPrev.Encode(); err == nil {
		t.Fatal("svg preview accepted")
	}
}

func TestReactionBlock(t *testing.T) {
	r := &ReactionBlock{Target: id.EventID{0x11}, Emoji: " 🌲 ", Active: true}
	enc, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeReactionBlock(enc)
	if err != nil || got.Emoji != "🌲" || !got.Active || got.Target != r.Target {
		t.Fatalf("reaction: %+v %v", got, err)
	}
	// active=false round-trips (state-based, not toggle).
	r2 := &ReactionBlock{Target: id.EventID{0x11}, Emoji: "🌲", Active: false}
	enc2, _ := r2.Encode()
	got2, err := DecodeReactionBlock(enc2)
	if err != nil || got2.Active {
		t.Fatalf("inactive reaction: %+v %v", got2, err)
	}
	// Multi-word "emoji" rejected; control chars rejected.
	for _, bad := range []string{"hello world", "a\x01b", "", strings.Repeat("🌲", 20)} {
		if _, err := (&ReactionBlock{Target: id.EventID{1}, Emoji: bad, Active: true}).Encode(); err == nil {
			t.Fatalf("bad emoji accepted: %q", bad)
		}
	}
}

func TestLiveSignalBlock(t *testing.T) {
	s := &LiveSignalBlock{
		FallbackText: "~ slow-pines signal · density 42% · 03:18",
		Engine:       LiveSignalEngineV1,
		Preset:       "slow-pines@1",
		Seed:         173991,
		Params: []SignalParam{
			{Name: "density", Value: 420}, {Name: "wind", Value: 180},
		},
	}
	enc, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if len(enc) > 300 {
		t.Fatalf("live signal should be a few hundred bytes, got %d", len(enc))
	}
	got, err := DecodeLiveSignalBlock(enc)
	if err != nil || got.Preset != "slow-pines@1" || len(got.Params) != 2 {
		t.Fatalf("signal: %+v %v", got, err)
	}
	// Network tolerance: unknown preset name and params pass the protocol
	// validator (renderer falls back; producer UIs are the strict layer).
	future := &LiveSignalBlock{FallbackText: "~ future field", Engine: "qs.other_engine.v3",
		Preset: "aurora-field@7", Seed: 1,
		Params: []SignalParam{{Name: "unknown_param", Value: 999}}}
	encF, err := future.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(BlockLiveSignal, encF); err != nil {
		t.Fatalf("future preset rejected by protocol validator: %v", err)
	}
	// Universal constraints still enforced.
	for _, bad := range []*LiveSignalBlock{
		{FallbackText: "", Engine: LiveSignalEngineV1, Preset: "x@1"},
		{FallbackText: "f", Engine: LiveSignalEngineV1, Preset: "NoVersion"},
		{FallbackText: "f", Engine: LiveSignalEngineV1, Preset: "x@1",
			Params: []SignalParam{{Name: "p", Value: 1001}}},
	} {
		if _, err := bad.Encode(); err == nil {
			t.Fatalf("invalid signal accepted: %+v", bad)
		}
	}
}

func TestEncodedSizeGate(t *testing.T) {
	// Per-field limits keep every legal block under the payload gate…
	big := &VisualBlock{Alt: "x", Caption: strings.Repeat("a", MaxCaptionLen),
		Thumb: make([]byte, MaxInlinePreview), ThumbMIME: "image/webp",
		Original: testRef(2)}
	enc, err := big.Encode()
	if err != nil {
		t.Fatalf("legal maximal block rejected: %v", err)
	}
	if len(enc) > MaxBlockPayloadBytes {
		t.Fatalf("legal block exceeds payload gate: %d", len(enc))
	}
	// …and the final defense checks the actually-encoded CBOR, not field
	// arithmetic.
	if _, err := finishBlock(make([]byte, MaxBlockPayloadBytes+1)); err == nil {
		t.Fatal("encoded-size gate not enforced")
	}
}
