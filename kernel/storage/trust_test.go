package storage

import (
	"bytes"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// The trust store must round-trip exactly. A certificate that changes shape
// across a restart is a device that stops being admitted, and a revocation
// that does is a device that quietly comes back.
func TestKeystoreRoundTripsCertificates(t *testing.T) {
	k := &Keystore{
		Certs: []CertRecord{
			{Device: id.DeviceID{1}, Frame: []byte("cert-for-mac")},
			{Device: id.DeviceID{2}, Frame: []byte("cert-for-phone")},
		},
		Revs: []RevRecord{
			{Device: id.DeviceID{2}, Frame: []byte("revocation-of-phone")},
		},
	}
	got, err := decodeKeystore(k.encode())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Certs) != 2 || len(got.Revs) != 1 {
		t.Fatalf("trust store lost records: %d certs, %d revocations",
			len(got.Certs), len(got.Revs))
	}
	if got.Certs[0].Device != (id.DeviceID{1}) ||
		!bytes.Equal(got.Certs[0].Frame, []byte("cert-for-mac")) {
		t.Fatalf("certificate changed: %+v", got.Certs[0])
	}
	if got.Revs[0].Device != (id.DeviceID{2}) ||
		!bytes.Equal(got.Revs[0].Frame, []byte("revocation-of-phone")) {
		t.Fatalf("revocation changed: %+v", got.Revs[0])
	}
}

// The legacy allowlist is frozen once, at migration, and it decides who may
// speak without a certificate forever after. Losing it across a restart would
// silently re-open the door this wave exists to close.
func TestKeystoreCarriesTheLegacyAllowlist(t *testing.T) {
	k := &Keystore{
		LegacyBindings: []LegacyBinding{
			{Principal: id.PrincipalID{7}, Device: id.DeviceID{8}},
			{Principal: id.PrincipalID{9}, Device: id.DeviceID{10}},
		},
	}
	got, err := decodeKeystore(k.encode())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.LegacyBindings) != 2 {
		t.Fatalf("allowlist lost entries: %d", len(got.LegacyBindings))
	}
	if got.LegacyBindings[1].Principal != (id.PrincipalID{9}) ||
		got.LegacyBindings[1].Device != (id.DeviceID{10}) {
		t.Fatalf("binding changed: %+v", got.LegacyBindings[1])
	}
}

// A keystore written before this wave has neither key. It must open exactly
// as before — an upgrade is never a reason to lose somebody's spaces — and
// it must come back with an EMPTY allowlist rather than a nil-means-everyone
// one, because the migration has not run yet and the difference decides who
// gets admitted.
func TestAKeystoreWithoutTrustStillOpens(t *testing.T) {
	k := &Keystore{DisplayName: "alice"}
	got, err := decodeKeystore(k.encode())
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "alice" {
		t.Fatalf("display name lost: %q", got.DisplayName)
	}
	if len(got.Certs) != 0 || len(got.Revs) != 0 || len(got.LegacyBindings) != 0 {
		t.Fatal("trust records appeared from nowhere")
	}
}

// A newer build appends a field to a record; this one must skip it rather
// than die mid-decode. Same lesson SpaceMeta's decoder already carries.
func TestATrustRecordFromANewerBuildStillDecodes(t *testing.T) {
	// Hand-roll one cert entry with an extra trailing element.
	dev := id.DeviceID{4}
	var buf []byte
	buf = codec.AppendArray(buf, 3) // one element longer than certFields
	buf = codec.AppendBytes(buf, dev[:])
	buf = codec.AppendBytes(buf, []byte("cert"))
	buf = codec.AppendText(buf, "something a later wave added")

	d := codec.NewDecoder(buf)
	rec, err := readCertRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Device != (id.DeviceID{4}) || !bytes.Equal(rec.Frame, []byte("cert")) {
		t.Fatalf("record changed: %+v", rec)
	}
}
