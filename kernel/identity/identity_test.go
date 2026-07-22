package identity

import (
	"bytes"
	"crypto/ed25519"
	"testing"
)

func newTestIdentity(t *testing.T) (*Principal, *Device) {
	t.Helper()
	p, err := NewPrincipal(NewRand())
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDevice(NewRand())
	if err != nil {
		t.Fatal(err)
	}
	return p, d
}

func TestCertificateRoundTrip(t *testing.T) {
	p, d := newTestIdentity(t)
	cert := p.Certify(d, 10, 0)
	if err := cert.Verify(); err != nil {
		t.Fatal(err)
	}
	enc, err := cert.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeCertificate(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Principal != cert.Principal || got.Device != cert.Device ||
		got.X25519Pub != cert.X25519Pub || got.IssuedAt != 10 {
		t.Fatalf("mismatch: %+v", got)
	}
	if err := got.Verify(); err != nil {
		t.Fatal(err)
	}
	// Determinism.
	enc2, _ := cert.Encode()
	if !bytes.Equal(enc, enc2) {
		t.Fatal("certificate encoding not deterministic")
	}
}

func TestCertificateForgeryRejected(t *testing.T) {
	p, d := newTestIdentity(t)
	other, _ := newTestIdentity(t)
	cert := p.Certify(d, 1, 0)
	cert.Principal = other.ID // claim a different principal
	if err := cert.Verify(); err == nil {
		t.Fatal("forged certificate verified")
	}
}

func TestAdmitFlow(t *testing.T) {
	p, d := newTestIdentity(t)
	s := NewStore()

	// Unknown device fails closed.
	if err := s.Admit(p.ID, d.ID, 5); err == nil {
		t.Fatal("uncertified device admitted")
	}
	if err := s.AddCertificate(p.Certify(d, 1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.Admit(p.ID, d.ID, 5); err != nil {
		t.Fatal(err)
	}

	// Wrong principal rejected.
	other, _ := newTestIdentity(t)
	if err := s.Admit(other.ID, d.ID, 5); err == nil {
		t.Fatal("wrong principal admitted")
	}

	// Revocation: later events rejected, earlier still valid (ADR-002).
	rev := p.Revoke(d.ID, 100)
	if err := rev.Verify(); err != nil {
		t.Fatal(err)
	}
	encRev, err := rev.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeRevocation(encRev)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddRevocation(back); err != nil {
		t.Fatal(err)
	}
	if err := s.Admit(p.ID, d.ID, 100); err != nil {
		t.Fatal("event at revocation time rejected")
	}
	if err := s.Admit(p.ID, d.ID, 101); err == nil {
		t.Fatal("post-revocation event admitted")
	}
}

func TestRevocationFromWrongPrincipalRejected(t *testing.T) {
	p, d := newTestIdentity(t)
	mallory, _ := newTestIdentity(t)
	s := NewStore()
	if err := s.AddCertificate(p.Certify(d, 1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRevocation(mallory.Revoke(d.ID, 50)); err == nil {
		t.Fatal("revocation by non-owning principal accepted")
	}
}

func TestRecoveryBundle(t *testing.T) {
	p, d := newTestIdentity(t)
	cert := p.Certify(d, 1, 0)
	pass := []byte("correct horse battery staple")

	bundle, err := ExportRecoveryBundle(p, []*Certificate{cert}, pass)
	if err != nil {
		t.Fatal(err)
	}
	restored, certs, err := ImportRecoveryBundle(bundle, pass)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID != p.ID {
		t.Fatal("restored principal id differs")
	}
	if len(certs) != 1 || certs[0].Device != d.ID {
		t.Fatal("certificates not restored")
	}
	// Restored key must produce valid signatures.
	sig := restored.Sign([]byte("probe"))
	if !ed25519.Verify(ed25519.PublicKey(p.ID[:]), []byte("probe"), sig) {
		t.Fatal("restored key signature invalid")
	}
	// Wrong passphrase fails closed.
	if _, _, err := ImportRecoveryBundle(bundle, []byte("wrong passphrase")); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	// Tampered bundle fails.
	bundle[len(bundle)-1] ^= 1
	if _, _, err := ImportRecoveryBundle(bundle, pass); err == nil {
		t.Fatal("tampered bundle accepted")
	}
}

func TestFingerprintFormat(t *testing.T) {
	p, _ := newTestIdentity(t)
	fp := p.Fingerprint()
	if len(fp) != 49 { // 10 groups of 4 + 9 spaces
		t.Fatalf("fingerprint %q has length %d", fp, len(fp))
	}
}
