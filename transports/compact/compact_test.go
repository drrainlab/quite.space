package compact

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/transports/loopback"
	"github.com/drrainlab/quiet_places/transports/simulator"
)

func signedFrame(t *testing.T, seq uint64, prev *id.EventID, text string) []byte {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	var term id.TerminalID
	term[0] = 0xAA
	var dev id.DeviceID
	copy(dev[:], priv.Public().(ed25519.PublicKey))
	payload, err := (&schemas.TextMessage{Text: text}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	env := &signal.Envelope{
		Terminal: term, Principal: id.PrincipalID{1}, Device: dev,
		Sequence: seq, Previous: prev, Schema: schemas.MessageText,
		LogicalClock: seq, ProducedBy: signal.AuthorshipHuman,
		PayloadEncoding: signal.PayloadCBOR, Payload: payload,
		Priority: signal.PriorityMessage,
	}
	f, err := env.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// The wave's #1 risk, gate-blocking: byte-exact round-trip — the signature
// still verifies after compact encode/decode.
func TestByteExactRoundTripVerifies(t *testing.T) {
	frame := signedFrame(t, 1, nil, "the signed truth must survive any wrapper")
	enc := encode(frame)
	dec, err := decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dec, frame) {
		t.Fatal("round-trip not byte-exact")
	}
	env, err := signal.Decode(dec)
	if err != nil {
		t.Fatal(err)
	}
	if err := signal.VerifyFrame(dec, env); err != nil {
		t.Fatalf("signature broken by transport encoding: %v", err)
	}
}

// Deflate is used only when it wins; either way decode restores bytes.
func TestDeflateOnlyWhenSmaller(t *testing.T) {
	compressible := bytes.Repeat([]byte("aaaa"), 400)
	enc := encode(compressible)
	if enc[2]&flagDeflate == 0 {
		t.Fatal("compressible payload should deflate")
	}
	if len(enc) >= len(compressible)+3 {
		t.Fatal("deflate did not shrink")
	}
	// High-entropy (a signed frame is mostly ids+sig+ciphertext).
	random := signedFrame(t, 1, nil, "x")
	enc2 := encode(random)
	dec2, err := decode(enc2)
	if err != nil || !bytes.Equal(dec2, random) {
		t.Fatal("round-trip failed on high-entropy input")
	}
}

// Magic property: a compact packet can NEVER be a valid start of the raw
// grammar (raw packets are CBOR maps, first byte 0xA0..0xBF), and a
// raw-only receiver fails CLOSED on compact input.
func TestMagicDisjointFromRawGrammar(t *testing.T) {
	if Magic >= 0xA0 && Magic <= 0xBF {
		t.Fatal("magic collides with the CBOR map-header range")
	}
	frame := signedFrame(t, 1, nil, "probe")
	enc := encode(frame)
	if enc[0] >= 0xA0 && enc[0] <= 0xBF {
		t.Fatal("compact packet opens like a raw map")
	}
	// Raw-side fail-closed: the codec refuses a compact packet outright.
	if _, err := signal.Decode(enc); err == nil {
		t.Fatal("raw decoder must reject compact bytes")
	}
	// Compact side accepts raw packets untouched (pass-through in Poll).
	pair := loopback.NewPair(loopback.Faults{Seed: 2})
	w := Wrap(pair.B)
	if err := pair.A.Send(frame); err != nil { // raw peer sends raw bytes
		t.Fatal(err)
	}
	got := w.Poll()
	if len(got) != 1 || !bytes.Equal(got[0], frame) {
		t.Fatal("raw pass-through broken")
	}
}

// Sub-fragmentation to a radio MTU and reassembly at the wrapper.
func TestSubFragmentationOverLora(t *testing.T) {
	for _, profile := range []simulator.Profile{simulator.Lora64, simulator.Lora128, simulator.Mesh240} {
		for seed := int64(1); seed <= 5; seed++ {
			pair := simulator.NewPair(profile, seed)
			a, b := Wrap(pair.A), Wrap(pair.B)

			frame := signedFrame(t, 1, nil,
				"a message big enough to fragment over a tiny radio MTU — "+
					string(bytes.Repeat([]byte("~"), 300)))
			// Lossy link: retry until it lands (loss is the profile's job;
			// convergence retries are sync's job — here we emulate it).
			delivered := false
			for attempt := 0; attempt < 200 && !delivered; attempt++ {
				if err := a.Send(frame); err != nil {
					continue
				}
				for _, pkt := range b.Poll() {
					if bytes.Equal(pkt, frame) {
						delivered = true
						break
					}
				}
			}
			if !delivered {
				t.Fatalf("profile mtu=%d seed=%d: frame never reassembled", profile.MTU, seed)
			}
			env, err := signal.Decode(frame)
			if err != nil {
				t.Fatal(err)
			}
			if err := signal.VerifyFrame(frame, env); err != nil {
				t.Fatal(err)
			}
			_ = b
		}
	}
}

// Corpus benchmark (informational): packets per event, raw vs compact,
// over Lora128. The ≥35% target is a goal, not a blocker (2A is
// stateless; the id-table lands in 2B).
func TestCorpusPacketCounts(t *testing.T) {
	corpus := map[string][]byte{
		"short_text": signedFrame(t, 1, nil, "see you at the session tonight"),
		"long_text": signedFrame(t, 1, nil,
			string(bytes.Repeat([]byte("field notes / "), 40))),
	}
	mtu := 128
	for name, frame := range corpus {
		rawPkts := (len(frame) + mtu - 1) / mtu
		enc := encode(frame)
		compactPkts := (len(enc) + mtu - 1) / mtu
		t.Logf("%s: frame=%dB raw≈%d pkts · compact=%dB ≈%d pkts",
			name, len(frame), rawPkts, len(enc), compactPkts)
		if compactPkts > rawPkts+1 {
			t.Fatalf("%s: compact pathologically larger", name)
		}
	}
}
