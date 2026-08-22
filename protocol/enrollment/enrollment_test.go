package enrollment

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/capability"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

func signedManifest(t *testing.T, termPriv ed25519.PrivateKey) []byte {
	t.Helper()
	var term id.TerminalID
	copy(term[:], termPriv.Public().(ed25519.PublicKey))
	m := &manifest.Manifest{Terminal: term, Kind: manifest.KindSensor,
		DeclaredLabels: []string{"Greenhouse", "qp.instr=temperature:number:°C"},
		IOMode:         manifest.IOSourceOnly, Capabilities: []string{capability.SignalPublish},
		Publishes: []string{schemas.ObservationValue}, AgencyMode: manifest.AgencyDeterministic,
		RetentionSecs: 3600, AnnounceTTL: 300, Revision: 1}
	m.Controller[0] = 1
	f, err := m.Sign(termPriv)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func fresh(t *testing.T) (*Enrollment, ed25519.PrivateKey, ed25519.PrivateKey) {
	t.Helper()
	devPriv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32))
	termPriv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{2}, 32))
	mf := signedManifest(t, termPriv)
	e := &Enrollment{Label: "Greenhouse", ManifestFrame: mf, ManifestHash: sha256.Sum256(mf)}
	copy(e.Device[:], devPriv.Public().(ed25519.PublicKey))
	copy(e.Terminal[:], termPriv.Public().(ed25519.PublicKey))
	for i := range e.X25519Pub {
		e.X25519Pub[i] = 9
	}
	e.Nonce[0] = 7
	return e, devPriv, termPriv
}

func TestEnrollmentRoundTripsAndBindsAllThreeKeys(t *testing.T) {
	e, dp, tp := fresh(t)
	if err := e.Sign(dp, tp); err != nil {
		t.Fatal(err)
	}
	b, err := e.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Device != e.Device || got.Terminal != e.Terminal || got.X25519Pub != e.X25519Pub || got.Label != "Greenhouse" {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	// A manifest signed by SOME OTHER terminal, stapled to this device's
	// enrollment, is refused: the body's terminal_pub cannot sign for it.
	otherTerm := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, 32))
	e2, dp2, _ := fresh(t)
	e2.ManifestFrame = signedManifest(t, otherTerm)
	e2.ManifestHash = sha256.Sum256(e2.ManifestFrame)
	if err := e2.Sign(dp2, tp); err != nil {
		t.Fatal(err)
	}
	b2, _ := e2.Encode()
	if _, err := Decode(b2); err == nil {
		t.Fatal("a foreign manifest under this terminal's signature was accepted")
	}
	// The device signature covers the manifest hash: swapping the manifest
	// after signing is caught even when the replacement is self-consistent.
	e3, dp3, tp3 := fresh(t)
	if err := e3.Sign(dp3, tp3); err != nil {
		t.Fatal(err)
	}
	e3.Label = "tampered"
	b3, _ := e3.Encode()
	if _, err := Decode(b3); err == nil {
		t.Fatal("a tampered body verified")
	}
	// Sign refuses a key that is not the advertised identity.
	e4, _, tp4 := fresh(t)
	if err := e4.Sign(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{5}, 32)), tp4); err == nil {
		t.Fatal("signing with a stranger's device key was allowed")
	}
}

func TestProvisionRoundTrip(t *testing.T) {
	p := &Provision{CertFrame: []byte{1, 2, 3}, EpochFrames: [][]byte{{9}, {8, 8}}}
	p.Space[0], p.Principal[0], p.ManifestAck[0] = 1, 2, 3
	b, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeProvision(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Space != p.Space || len(got.EpochFrames) != 2 || !bytes.Equal(got.EpochFrames[1], []byte{8, 8}) || got.ManifestAck != p.ManifestAck {
		t.Fatalf("round trip: %+v", got)
	}
	if _, err := DecodeProvision(append(b, 0)); err == nil {
		t.Fatal("trailing bytes accepted")
	}
}
