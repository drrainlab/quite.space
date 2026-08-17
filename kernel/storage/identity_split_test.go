package storage

import (
	"errors"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// The authority device is the one holding the root seed. It gets a SIGNING
// principal back, because certifying and revoking are its job and nobody
// else's.
func TestAuthorityKeepsItsSigningPrincipal(t *testing.T) {
	p, _ := identity.NewPrincipal(identity.NewRand())
	d, _ := identity.NewDevice(identity.NewRand())
	k := &Keystore{
		PrincipalSeed: p.Seed(),
		DeviceSeed:    d.Seed(),
		DeviceX25519:  d.X25519Priv(),
	}
	pid, signer, dev, err := k.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if signer == nil {
		t.Fatal("the authority device lost its signing key")
	}
	if pid != p.ID || dev.ID != d.ID {
		t.Fatal("identity changed across a load")
	}
}

// A secondary device holds NO root seed. Its principal is read from the
// certificate the authority signed for it — never invented, never derived
// from a secret it does not have.
func TestSecondaryReadsItsPrincipalFromItsCertificate(t *testing.T) {
	p, _ := identity.NewPrincipal(identity.NewRand())
	child, _ := identity.NewDevice(identity.NewRand())
	cert := p.Certify(child, 1, 0)
	frame, err := cert.Encode()
	if err != nil {
		t.Fatal(err)
	}
	k := &Keystore{
		DeviceSeed:   child.Seed(),
		DeviceX25519: child.X25519Priv(),
		Certs:        []CertRecord{{Device: child.ID, Frame: frame}},
	}
	pid, signer, dev, err := k.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if signer != nil {
		t.Fatal("a secondary must not come back holding a signing principal")
	}
	if pid != p.ID {
		t.Fatal("the secondary did not learn its principal from the certificate")
	}
	if dev.ID != child.ID {
		t.Fatal("device changed across a load")
	}
}

// The branch that matters. A keystore with a device key, no root seed and no
// certificate is DAMAGED — the happy path always hands a secondary its
// certificate during pairing. It must fail closed and say so, never reach for
// self-certification it cannot perform and never invent a principal claim it
// cannot back.
func TestSecondaryWithoutCertificateCannotSelfCertify(t *testing.T) {
	d, _ := identity.NewDevice(identity.NewRand())
	k := &Keystore{DeviceSeed: d.Seed(), DeviceX25519: d.X25519Priv()}

	pid, signer, _, err := k.Identity()
	if err == nil {
		t.Fatal("a device with no root seed and no certificate opened anyway")
	}
	if !errors.Is(err, ErrNoDeviceCertificate) {
		t.Fatalf("wrong refusal, a person cannot act on this: %v", err)
	}
	if signer != nil {
		t.Fatal("a signing principal appeared from nowhere")
	}
	if pid != (id.PrincipalID{}) {
		t.Fatal("a principal was invented on the way out")
	}
}

// A certificate for SOMEBODY ELSE's device is not this device's certificate,
// however valid it is.
func TestACertificateForAnotherDeviceDoesNotAdmitThisOne(t *testing.T) {
	p, _ := identity.NewPrincipal(identity.NewRand())
	mine, _ := identity.NewDevice(identity.NewRand())
	other, _ := identity.NewDevice(identity.NewRand())
	frame, err := p.Certify(other, 1, 0).Encode()
	if err != nil {
		t.Fatal(err)
	}
	k := &Keystore{
		DeviceSeed:   mine.Seed(),
		DeviceX25519: mine.X25519Priv(),
		Certs:        []CertRecord{{Device: other.ID, Frame: frame}},
	}
	if _, _, _, err := k.Identity(); !errors.Is(err, ErrNoDeviceCertificate) {
		t.Fatalf("somebody else's certificate was accepted: %v", err)
	}
}
