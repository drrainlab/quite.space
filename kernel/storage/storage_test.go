package storage

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/crypto"
	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func TestKeystoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	pass := []byte("a decent passphrase")
	root, err := Open(dir, pass)
	if err != nil {
		t.Fatal(err)
	}

	p, err := identity.NewPrincipal(identity.NewRand())
	if err != nil {
		t.Fatal(err)
	}
	d, err := identity.NewDevice(identity.NewRand())
	if err != nil {
		t.Fatal(err)
	}
	ks := NewKeystore(p, d)
	term := id.TerminalID{0xAB}
	ks.TerminalSeeds[term] = bytes.Repeat([]byte{7}, 32)
	e1, _ := crypto.NewEpochKey(1)
	e2, _ := crypto.NewEpochKey(2)
	ks.Epochs[term] = []crypto.EpochKey{e1, e2}
	if err := root.SaveKeystore(ks); err != nil {
		t.Fatal(err)
	}

	// Reopen the root as a new process would.
	root2, err := Open(dir, pass)
	if err != nil {
		t.Fatal(err)
	}
	got, err := root2.LoadKeystore()
	if err != nil {
		t.Fatal(err)
	}
	p2, d2, err := got.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if p2.ID != p.ID || d2.ID != d.ID || d2.X25519Pub != d.X25519Pub {
		t.Fatal("identity did not survive the keystore")
	}
	if !bytes.Equal(got.TerminalSeeds[term], ks.TerminalSeeds[term]) {
		t.Fatal("terminal seed lost")
	}
	eps := got.Epochs[term]
	if len(eps) != 2 || eps[0].Key != e1.Key || eps[1].Key != e2.Key {
		t.Fatal("epoch keys lost — restart would lock us out of our own spaces")
	}
}

func TestWrongPassphraseFailsClosed(t *testing.T) {
	dir := t.TempDir()
	root, err := Open(dir, []byte("right passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := identity.NewPrincipal(identity.NewRand())
	d, _ := identity.NewDevice(identity.NewRand())
	if err := root.SaveKeystore(NewKeystore(p, d)); err != nil {
		t.Fatal(err)
	}
	wrong, err := Open(dir, []byte("wrong passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.LoadKeystore(); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase not rejected: %v", err)
	}
}

func TestFreshRootHasNoKeystore(t *testing.T) {
	root, err := Open(t.TempDir(), []byte("some passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.LoadKeystore(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist on fresh root, got %v", err)
	}
}

func TestBlobStore(t *testing.T) {
	root, err := Open(t.TempDir(), []byte("some passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("field recording, 44.1kHz")
	h, err := root.PutBlob(data)
	if err != nil {
		t.Fatal(err)
	}
	// Idempotent put.
	h2, err := root.PutBlob(data)
	if err != nil || h2 != h {
		t.Fatal("content addressing broken")
	}
	got, err := root.GetBlob(h)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("get: %v", err)
	}
	if !root.HasBlob(h) {
		t.Fatal("HasBlob false for stored blob")
	}
	if root.HasBlob(id.Hash{0xFF}) {
		t.Fatal("HasBlob true for missing blob")
	}
	// Corruption is detected, not served.
	if err := os.WriteFile(root.blobPath(h), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := root.GetBlob(h); err == nil {
		t.Fatal("corrupted blob served")
	}
}
