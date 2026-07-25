package storage

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/crypto"
	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/protocol/codec"
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

// PA-0.2: Visibility/Role roundtrip through the space entry, and an
// OLD-ARITY (7) space entry — a pre-PA-0 keystore — decodes to the private
// member defaults.
func TestSpaceMetaVisibilityRoleRoundTrip(t *testing.T) {
	prin, _ := identity.NewPrincipal(identity.NewRand())
	dev, _ := identity.NewDevice(identity.NewRand())
	k := NewKeystore(prin, dev)
	tid := id.TerminalID{9}
	k.Spaces[tid] = SpaceMeta{Title: "pub", Owned: true,
		Visibility: "public", Role: RoleReader}
	k.PublicPublish[tid] = PublicPublishState{
		PublisherDevice: dev.ID, ProjectionSeq: 42,
		LastContentDigest: [32]byte{1, 2, 3},
	}
	rt, err := decodeKeystore(k.encode())
	if err != nil {
		t.Fatal(err)
	}
	got := rt.Spaces[tid]
	if got.Visibility != "public" || got.Role != RoleReader {
		t.Fatalf("visibility/role did not roundtrip: %+v", got)
	}
	pp := rt.PublicPublish[tid]
	if pp.ProjectionSeq != 42 || pp.PublisherDevice != dev.ID || pp.LastContentDigest[0] != 1 {
		t.Fatalf("publish state did not roundtrip: %+v", pp)
	}
}

func TestOldAritySpaceEntryDecodesPrivate(t *testing.T) {
	prin, _ := identity.NewPrincipal(identity.NewRand())
	dev, _ := identity.NewDevice(identity.NewRand())
	k := NewKeystore(prin, dev)
	tid := id.TerminalID{3}
	// Hand-build a pre-PA-0 keystore: identical layout but the space entry
	// is the OLD arity-7 form (no visibility/role fields).
	buf := codec.AppendMap(nil, 4)
	buf = codec.AppendUint(buf, ksKeyPrincipal)
	buf = codec.AppendBytes(buf, k.PrincipalSeed)
	buf = codec.AppendUint(buf, ksKeyDevice)
	buf = codec.AppendBytes(buf, k.DeviceSeed)
	buf = codec.AppendUint(buf, ksKeyDeviceX)
	buf = codec.AppendBytes(buf, k.DeviceX25519[:])
	buf = codec.AppendUint(buf, ksKeySpaces)
	buf = codec.AppendArray(buf, 1)
	buf = codec.AppendArray(buf, 7)
	buf = codec.AppendBytes(buf, tid[:])
	buf = codec.AppendText(buf, "legacy")
	buf = codec.AppendBool(buf, false)
	buf = codec.AppendBytes(buf, nil) // manifest frame
	buf = codec.AppendArray(buf, 0)   // members
	buf = codec.AppendBytes(buf, nil) // appearance override
	buf = codec.AppendBytes(buf, nil) // appearance frame
	rt, err := decodeKeystore(buf)
	if err != nil {
		t.Fatal(err)
	}
	got := rt.Spaces[tid]
	if got.Visibility != "" || got.Role != "" {
		t.Fatalf("old entry must decode to private member defaults: %+v", got)
	}
	if got.Title != "legacy" {
		t.Fatalf("old entry title lost: %+v", got)
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
