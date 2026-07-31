// Relay identity (RR-1): a PERSISTENT private key whose SPKI digest is the
// relay's pinned name. The certificate is regenerated freely on every
// start — clients pin the KEY, never the certificate, so validity windows
// and rotation of the cert itself are non-events. Rotating the KEY is a
// deliberate operation shipped to clients as a [current, next] pin set in
// the registry.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const identityFile = "relay-identity.pem"

// loadOrCreateIdentity returns the TLS certificate built from the
// persistent key in dataDir (creating both dir and key on first run) and
// the key's SPKI pin.
func loadOrCreateIdentity(dataDir string) (tls.Certificate, string, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return tls.Certificate{}, "", err
	}
	path := filepath.Join(dataDir, identityFile)
	priv, err := loadKey(path)
	if errors.Is(err, os.ErrNotExist) {
		priv, err = createKey(path)
	}
	if err != nil {
		return tls.Certificate{}, "", err
	}
	// Fresh certificate from the persistent key. NotAfter is generous only
	// to keep inspection tools calm — pinned clients never check it.
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	pin, err := spkiPin(der)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, pin, nil
}

func loadKey(path string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		return nil, errors.New("relay-identity.pem: not an EC private key")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

func createKey(path string) (*ecdsa.PrivateKey, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	b := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return priv, nil
}

func spkiPin(rawCert []byte) (string, error) {
	cert, err := x509.ParseCertificate(rawCert)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}
