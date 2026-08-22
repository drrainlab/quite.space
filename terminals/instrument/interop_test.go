package instrument_test

// THE INTEROP GATE (QI-M0.4 / M1): frames produced by the INDEPENDENT C
// core are driven through a real Go Space. Go consumes bytes it did not
// produce; if the reducer ends up holding exactly the values the C tool
// was asked to emit, the two implementations agree on the protocol.
//
// Both sides start from the same fixed inputs — testvectors/
// instrument_v1.json: the device seeds, the space, the fixed epoch key
// and the captured wrap that addresses the device. The C tool unwraps
// that wrap (its only HPKE direction), seals readings under the key, and
// signs frames from sequence 1; this side emits the same epoch payload
// into a fresh space so the member's authorization set names the device,
// restores the fixed key, and absorbs what the C side produced.
//
// Until the C core exists the fixture directory is empty and the gate
// skips — loudly, so absence is never mistaken for success.

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/crypto"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/human"
)

const (
	emittedDir  = "../../sdk/instrument-c/testdata/emitted"
	vectorsFile = "../../testvectors/instrument_v1.json"
)

type interopVectors struct {
	SpaceID          string `json:"space_id"`
	EpochKey         string `json:"epoch_key"`
	EpochN           uint64 `json:"epoch_n"`
	EpochPayloadCBOR string `json:"epoch_payload_cbor"`
	DeviceID         string `json:"device_id"`
}

// Fixture layout per case: <name>.frames (one hex frame per line, in
// emission order) and <name>.expect (channel=value lines, e.g.
// temperature=21.4, door=true, mode=auto).
func TestCProducedFramesReduceInAGoSpace(t *testing.T) {
	matches, _ := filepath.Glob(filepath.Join(emittedDir, "*.frames"))
	if len(matches) == 0 {
		t.Skip("interop gate: no C-produced frames checked in yet (sdk/instrument-c, QI-M1)")
	}
	raw, err := os.ReadFile(vectorsFile)
	if err != nil {
		t.Fatal(err)
	}
	var v interopVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for _, framesPath := range matches {
		name := strings.TrimSuffix(filepath.Base(framesPath), ".frames")
		t.Run(name, func(t *testing.T) {
			s, dev := memberSpace(t, v)
			for _, l := range readLines(t, framesPath) {
				frame, err := hex.DecodeString(l)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.Absorb(frame); err != nil {
					t.Fatalf("Go refused a C frame: %v", err)
				}
			}
			if s.UnauthorizedInstrument != 0 {
				t.Fatalf("%d C frames came from a device the epoch does not address (want %s)", s.UnauthorizedInstrument, dev)
			}
			if s.Undecryptable != 0 {
				t.Fatalf("%d C frames could not be opened with the shared epoch key", s.Undecryptable)
			}
			for _, exp := range readLines(t, strings.TrimSuffix(framesPath, ".frames")+".expect") {
				ch, want, _ := strings.Cut(exp, "=")
				got, found := "", false
				for k, o := range s.State.ValueObservations() {
					if k.Channel == ch {
						got, found = display(o.Value), true
					}
				}
				if !found || got != want {
					t.Fatalf("channel %s: Go reduced %q (found=%v), C intended %q", ch, got, found, want)
				}
			}
		})
	}
}

// memberSpace is a member's replica in which the C device is the current
// instrument epoch's recipient and the fixed epoch key is known.
func memberSpace(t *testing.T, v interopVectors) (*terminals.Space, string) {
	t.Helper()
	owner, err := human.New("owner")
	if err != nil {
		t.Fatal(err)
	}
	space := id.TerminalID(mustHex(t, v.SpaceID))
	s := terminals.Replica(space)
	s.EnablePrivate(owner.Device)
	s.AddMember(owner.Device.ID, owner.Device.X25519Pub)
	payload := mustHex(t, v.EpochPayloadCBOR)
	if _, err := owner.Emit(s, schemas.InstrumentEpoch, payload, owner.DefaultAuthorship(), 0); err != nil {
		t.Fatal(err)
	}
	var key crypto.EpochKey
	key.N = v.EpochN
	copy(key.Key[:], mustHex(t, v.EpochKey))
	s.RestoreInstrumentEpochs([]crypto.EpochKey{key})
	return s, v.DeviceID
}

func display(v schemas.ValueObservation) string {
	switch {
	case v.HasBool:
		if v.BoolValue {
			return "true"
		}
		return "false"
	case v.EnumValue != "":
		return v.EnumValue
	}
	sign := ""
	if v.Negative {
		sign = "-"
	}
	digits := uintStr(v.Magnitude)
	for uint64(len(digits)) <= v.Decimals {
		digits = "0" + digits
	}
	if v.Decimals == 0 {
		return sign + digits
	}
	cut := len(digits) - int(v.Decimals)
	return sign + digits[:cut] + "." + digits[cut:]
}

func uintStr(u uint64) string {
	if u == 0 {
		return "0"
	}
	var b []byte
	for u > 0 {
		b = append([]byte{byte('0' + u%10)}, b...)
		u /= 10
	}
	return string(b)
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}
