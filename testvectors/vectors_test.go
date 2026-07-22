// Golden test vectors for protocol v0 (M0.1/M0.2 acceptance): known-answer
// frames built from fixed seeds. Any byte change here is a protocol change
// and must be deliberate. Regenerate with: go test ./testvectors -update
package testvectors

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/capability"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

var update = flag.Bool("update", false, "rewrite golden vector files")

const goldenFile = "protocol_v0.json"

type vectors struct {
	Note           string `json:"note"`
	DeviceSeed     string `json:"device_seed"`
	TerminalSeed   string `json:"terminal_seed"`
	EnvelopeFrame  string `json:"envelope_frame"`
	EnvelopeID     string `json:"envelope_event_id"`
	ManifestFrame  string `json:"manifest_frame"`
	ManifestHash   string `json:"manifest_hash"`
	Fingerprint    string `json:"device_fingerprint"`
	PayloadMessage string `json:"payload_message_text"`
}

func buildVectors(t *testing.T) vectors {
	t.Helper()
	deviceSeed := bytes.Repeat([]byte{0x11}, ed25519.SeedSize)
	terminalSeed := bytes.Repeat([]byte{0x22}, ed25519.SeedSize)
	devPriv := ed25519.NewKeyFromSeed(deviceSeed)
	termPriv := ed25519.NewKeyFromSeed(terminalSeed)

	var dev id.DeviceID
	copy(dev[:], devPriv.Public().(ed25519.PublicKey))
	var term id.TerminalID
	copy(term[:], termPriv.Public().(ed25519.PublicKey))
	var prin id.PrincipalID
	prin[0] = 0x01 // placeholder principal for the vector

	msg := &schemas.TextMessage{Text: "hello from the quiet places"}
	payload, err := msg.Encode()
	if err != nil {
		t.Fatal(err)
	}
	env := &signal.Envelope{
		Terminal:        term,
		Principal:       prin,
		Device:          dev,
		Sequence:        1,
		Schema:          schemas.MessageText,
		CreatedAt:       1753142400, // 2025-07-22T00:00:00Z, fixed
		LogicalClock:    1,
		ProducedBy:      signal.AuthorshipHuman,
		PayloadEncoding: signal.PayloadCBOR,
		Payload:         payload,
		Priority:        signal.PriorityMessage,
	}
	frame, err := env.Sign(devPriv)
	if err != nil {
		t.Fatal(err)
	}

	man := &manifest.Manifest{
		Terminal:       term,
		Controller:     prin,
		Kind:           manifest.KindSensor,
		DeclaredLabels: []string{"environment", "greenhouse", "temperature"},
		IOMode:         manifest.IOSourceOnly,
		Capabilities:   []string{capability.SignalPublish, capability.PresencePublish},
		Publishes:      []string{schemas.ObservationTemp},
		AgencyMode:     manifest.AgencyDeterministic,
		RetentionSecs:  3600,
		AnnounceTTL:    300,
		Revision:       1,
	}
	manFrame, err := man.Sign(termPriv)
	if err != nil {
		t.Fatal(err)
	}

	return vectors{
		Note:           "Protocol v0 known-answer vectors. Seeds are fixed test keys, never real identities.",
		DeviceSeed:     hex.EncodeToString(deviceSeed),
		TerminalSeed:   hex.EncodeToString(terminalSeed),
		EnvelopeFrame:  hex.EncodeToString(frame),
		EnvelopeID:     id.EventIDOf(frame).Hex(),
		ManifestFrame:  hex.EncodeToString(manFrame),
		ManifestHash:   manifest.Hash(manFrame).Hex(),
		Fingerprint:    id.Fingerprint(dev[:]),
		PayloadMessage: msg.Text,
	}
}

func TestGoldenVectors(t *testing.T) {
	got := buildVectors(t)
	if *update {
		data, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenFile, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("golden vectors updated")
		return
	}
	data, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatalf("missing golden file (run with -update once): %v", err)
	}
	var want vectors
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("vectors drifted from golden file — protocol bytes changed.\n got: %+v\nwant: %+v", got, want)
	}
}

// TestVectorsVerifyIndependently re-verifies the golden frames through the
// public verification path only (M0.2 acceptance: independent processes
// agree on the same vectors).
func TestVectorsVerifyIndependently(t *testing.T) {
	data, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Skip("golden file not generated yet")
	}
	var v vectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	frame, err := hex.DecodeString(v.EnvelopeFrame)
	if err != nil {
		t.Fatal(err)
	}
	env, err := signal.Decode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if err := signal.VerifyFrame(frame, env); err != nil {
		t.Fatal(err)
	}
	if id.EventIDOf(frame).Hex() != v.EnvelopeID {
		t.Fatal("event id mismatch")
	}
	if err := schemas.Validate(env.Schema, env.Payload); err != nil {
		t.Fatal(err)
	}
	manFrame, err := hex.DecodeString(v.ManifestFrame)
	if err != nil {
		t.Fatal(err)
	}
	man, err := manifest.Decode(manFrame)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.VerifyFrame(manFrame, man); err != nil {
		t.Fatal(err)
	}
}
