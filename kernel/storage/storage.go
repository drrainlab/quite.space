// Package storage implements the M1.0 data root (ADR-006): an encrypted
// keystore for private key material (principal, device, epoch keys) and a
// content-addressed blob store. Event segments live in kernel/eventlog;
// materialized state stays derived and rebuildable. The SQLite index cache
// arrives with the UI milestone (pure-Go driver only, ADR-011) — nothing
// stored here will need migration for it.
//
// Layout under one data root:
//
//	keys/salt          scrypt salt (public)
//	keys/keystore.enc  sealed key material
//	blobs/sha256/xx/…  content-addressed attachments
//	events/<terminal>/ append-only segments (kernel/eventlog)
package storage

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"

	"github.com/drrainlab/quiet_places/kernel/crypto"
	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

const (
	saltLen = 16
	scryptN = 1 << 15
	scryptR = 8
	scryptP = 1
)

var keystoreAAD = []byte("quiet-places-keystore-v0")

// Root is an opened data root with the master key in memory.
type Root struct {
	dir string
	key [32]byte
}

// Open unlocks (or initializes) a data root with a passphrase. The scrypt
// salt is created on first use; a wrong passphrase surfaces on the first
// keystore read, not here (the salt alone cannot verify it).
func Open(dir string, passphrase []byte) (*Root, error) {
	if len(passphrase) < 8 {
		return nil, errors.New("storage: passphrase must be at least 8 bytes")
	}
	if err := os.MkdirAll(filepath.Join(dir, "keys"), 0o700); err != nil {
		return nil, err
	}
	saltPath := filepath.Join(dir, "keys", "salt")
	salt, err := os.ReadFile(saltPath)
	if errors.Is(err, os.ErrNotExist) {
		salt = make([]byte, saltLen)
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
		if err := os.WriteFile(saltPath, salt, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	key, err := scrypt.Key(passphrase, salt, scryptN, scryptR, scryptP, 32)
	if err != nil {
		return nil, err
	}
	r := &Root{dir: dir}
	copy(r.key[:], key)
	return r, nil
}

// EventsDir returns the segment directory for a terminal (kernel/eventlog).
func (r *Root) EventsDir(terminal id.TerminalID) string {
	return filepath.Join(r.dir, "events", terminal.Hex())
}

// ---- Keystore ----

// Keystore is the decrypted key material of one node.
type Keystore struct {
	PrincipalSeed []byte
	DeviceSeed    []byte
	DeviceX25519  [32]byte
	// TerminalSeeds holds terminal private keys this node controls.
	TerminalSeeds map[id.TerminalID][]byte
	// Epochs holds known epoch keys per private space.
	Epochs map[id.TerminalID][]crypto.EpochKey
	// SelfTerminalSeed is the user's participant terminal key.
	SelfTerminalSeed []byte
	// Spaces holds per-space metadata needed to reopen them.
	Spaces map[id.TerminalID]SpaceMeta
}

// SpaceMeta is what the node must remember about a space besides its log.
type SpaceMeta struct {
	Title         string
	Owned         bool
	ManifestFrame []byte
	// Members is the controller's rotation list (owned spaces only).
	Members map[id.DeviceID][32]byte
}

// NewKeystore captures a fresh identity's key material.
func NewKeystore(p *identity.Principal, d *identity.Device) *Keystore {
	return &Keystore{
		PrincipalSeed: p.Seed(),
		DeviceSeed:    d.Seed(),
		DeviceX25519:  d.X25519Priv(),
		TerminalSeeds: map[id.TerminalID][]byte{},
		Epochs:        map[id.TerminalID][]crypto.EpochKey{},
		Spaces:        map[id.TerminalID]SpaceMeta{},
	}
}

// Identity rebuilds the principal and device from the keystore.
func (k *Keystore) Identity() (*identity.Principal, *identity.Device, error) {
	if len(k.PrincipalSeed) == 0 || len(k.DeviceSeed) == 0 {
		return nil, nil, errors.New("storage: keystore has no identity")
	}
	p := identity.NewPrincipalFromSeed(k.PrincipalSeed)
	d, err := identity.NewDeviceFromKeys(k.DeviceSeed, k.DeviceX25519)
	if err != nil {
		return nil, nil, err
	}
	return p, d, nil
}

// keystore wire key table v0 (append-only, ADR-009).
const (
	ksKeyPrincipal = 1
	ksKeyDevice    = 2
	ksKeyDeviceX   = 3
	ksKeyTerminals = 4
	ksKeyEpochs    = 5
	ksKeySelfTerm  = 6
	ksKeySpaces    = 7
)

func (k *Keystore) encode() []byte {
	buf := codec.AppendMap(nil, 7)
	buf = codec.AppendUint(buf, ksKeyPrincipal)
	buf = codec.AppendBytes(buf, k.PrincipalSeed)
	buf = codec.AppendUint(buf, ksKeyDevice)
	buf = codec.AppendBytes(buf, k.DeviceSeed)
	buf = codec.AppendUint(buf, ksKeyDeviceX)
	buf = codec.AppendBytes(buf, k.DeviceX25519[:])
	buf = codec.AppendUint(buf, ksKeyTerminals)
	buf = codec.AppendArray(buf, len(k.TerminalSeeds))
	for _, ts := range sortedTerminalSeeds(k.TerminalSeeds) {
		buf = codec.AppendArray(buf, 2)
		buf = codec.AppendBytes(buf, ts.id[:])
		buf = codec.AppendBytes(buf, ts.seed)
	}
	buf = codec.AppendUint(buf, ksKeyEpochs)
	buf = codec.AppendArray(buf, len(k.Epochs))
	for _, ep := range sortedEpochs(k.Epochs) {
		buf = codec.AppendArray(buf, 2)
		buf = codec.AppendBytes(buf, ep.id[:])
		buf = codec.AppendArray(buf, len(ep.keys))
		for _, e := range ep.keys {
			buf = codec.AppendArray(buf, 2)
			buf = codec.AppendUint(buf, e.N)
			buf = codec.AppendBytes(buf, e.Key[:])
		}
	}
	buf = codec.AppendUint(buf, ksKeySelfTerm)
	buf = codec.AppendBytes(buf, k.SelfTerminalSeed)
	buf = codec.AppendUint(buf, ksKeySpaces)
	buf = codec.AppendArray(buf, len(k.Spaces))
	for _, sm := range sortedSpaces(k.Spaces) {
		buf = codec.AppendArray(buf, 5)
		buf = codec.AppendBytes(buf, sm.id[:])
		buf = codec.AppendText(buf, sm.meta.Title)
		buf = codec.AppendBool(buf, sm.meta.Owned)
		buf = codec.AppendBytes(buf, sm.meta.ManifestFrame)
		buf = codec.AppendArray(buf, len(sm.members))
		for _, mm := range sm.members {
			buf = codec.AppendArray(buf, 2)
			buf = codec.AppendBytes(buf, mm.dev[:])
			buf = codec.AppendBytes(buf, mm.xpub[:])
		}
	}
	return buf
}

type spaceEntry struct {
	id      id.TerminalID
	meta    SpaceMeta
	members []memberEntry
}

type memberEntry struct {
	dev  id.DeviceID
	xpub [32]byte
}

func sortedSpaces(m map[id.TerminalID]SpaceMeta) []spaceEntry {
	out := make([]spaceEntry, 0, len(m))
	for tid, meta := range m {
		e := spaceEntry{id: tid, meta: meta}
		for dev, xpub := range meta.Members {
			e.members = append(e.members, memberEntry{dev, xpub})
		}
		for i := 1; i < len(e.members); i++ {
			for j := i; j > 0 && string(e.members[j-1].dev[:]) > string(e.members[j].dev[:]); j-- {
				e.members[j-1], e.members[j] = e.members[j], e.members[j-1]
			}
		}
		out = append(out, e)
	}
	sortByID(out, func(t spaceEntry) id.TerminalID { return t.id })
	return out
}

type terminalSeed struct {
	id   id.TerminalID
	seed []byte
}

type terminalEpochs struct {
	id   id.TerminalID
	keys []crypto.EpochKey
}

func sortedTerminalSeeds(m map[id.TerminalID][]byte) []terminalSeed {
	out := make([]terminalSeed, 0, len(m))
	for tid, s := range m {
		out = append(out, terminalSeed{tid, s})
	}
	sortByID(out, func(t terminalSeed) id.TerminalID { return t.id })
	return out
}

func sortedEpochs(m map[id.TerminalID][]crypto.EpochKey) []terminalEpochs {
	out := make([]terminalEpochs, 0, len(m))
	for tid, keys := range m {
		out = append(out, terminalEpochs{tid, keys})
	}
	sortByID(out, func(t terminalEpochs) id.TerminalID { return t.id })
	return out
}

func sortByID[T any](s []T, key func(T) id.TerminalID) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0; j-- {
			a, b := key(s[j-1]), key(s[j])
			if string(a[:]) <= string(b[:]) {
				break
			}
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func decodeKeystore(data []byte) (*Keystore, error) {
	d := codec.NewDecoder(data)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	k := &Keystore{
		TerminalSeeds: map[id.TerminalID][]byte{},
		Epochs:        map[id.TerminalID][]crypto.EpochKey{},
		Spaces:        map[id.TerminalID]SpaceMeta{},
	}
	for {
		key, ok, er := m.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch key {
		case ksKeyPrincipal:
			var b []byte
			b, er = d.ReadBytes()
			k.PrincipalSeed = append([]byte(nil), b...)
		case ksKeyDevice:
			var b []byte
			b, er = d.ReadBytes()
			k.DeviceSeed = append([]byte(nil), b...)
		case ksKeyDeviceX:
			var b []byte
			b, er = d.ReadBytes()
			if er == nil && len(b) == 32 {
				copy(k.DeviceX25519[:], b)
			}
		case ksKeyTerminals:
			var cnt int
			cnt, er = d.ReadArray()
			if er != nil {
				return nil, er
			}
			for range cnt {
				if _, er = d.ReadArray(); er != nil {
					return nil, er
				}
				var tidB, seed []byte
				if tidB, er = d.ReadBytes(); er != nil {
					return nil, er
				}
				if seed, er = d.ReadBytes(); er != nil {
					return nil, er
				}
				if len(tidB) != id.Size {
					return nil, errors.New("storage: bad terminal id in keystore")
				}
				var tid id.TerminalID
				copy(tid[:], tidB)
				k.TerminalSeeds[tid] = append([]byte(nil), seed...)
			}
		case ksKeyEpochs:
			var cnt int
			cnt, er = d.ReadArray()
			if er != nil {
				return nil, er
			}
			for range cnt {
				if _, er = d.ReadArray(); er != nil {
					return nil, er
				}
				var tidB []byte
				if tidB, er = d.ReadBytes(); er != nil {
					return nil, er
				}
				if len(tidB) != id.Size {
					return nil, errors.New("storage: bad terminal id in epochs")
				}
				var tid id.TerminalID
				copy(tid[:], tidB)
				var ecnt int
				if ecnt, er = d.ReadArray(); er != nil {
					return nil, er
				}
				for range ecnt {
					if _, er = d.ReadArray(); er != nil {
						return nil, er
					}
					var e crypto.EpochKey
					if e.N, er = d.ReadUint(); er != nil {
						return nil, er
					}
					var kb []byte
					if kb, er = d.ReadBytes(); er != nil {
						return nil, er
					}
					if len(kb) != crypto.KeySize {
						return nil, errors.New("storage: bad epoch key length")
					}
					copy(e.Key[:], kb)
					k.Epochs[tid] = append(k.Epochs[tid], e)
				}
			}
		case ksKeySelfTerm:
			var b []byte
			b, er = d.ReadBytes()
			k.SelfTerminalSeed = append([]byte(nil), b...)
		case ksKeySpaces:
			var cnt int
			cnt, er = d.ReadArray()
			if er != nil {
				return nil, er
			}
			for range cnt {
				if _, er = d.ReadArray(); er != nil {
					return nil, er
				}
				var tidB []byte
				if tidB, er = d.ReadBytes(); er != nil {
					return nil, er
				}
				if len(tidB) != id.Size {
					return nil, errors.New("storage: bad space id")
				}
				var tid id.TerminalID
				copy(tid[:], tidB)
				var meta SpaceMeta
				if meta.Title, er = d.ReadText(); er != nil {
					return nil, er
				}
				if meta.Owned, er = d.ReadBool(); er != nil {
					return nil, er
				}
				var mf []byte
				if mf, er = d.ReadBytes(); er != nil {
					return nil, er
				}
				meta.ManifestFrame = append([]byte(nil), mf...)
				var mcnt int
				if mcnt, er = d.ReadArray(); er != nil {
					return nil, er
				}
				meta.Members = map[id.DeviceID][32]byte{}
				for range mcnt {
					if _, er = d.ReadArray(); er != nil {
						return nil, er
					}
					var devB, xpubB []byte
					if devB, er = d.ReadBytes(); er != nil {
						return nil, er
					}
					if xpubB, er = d.ReadBytes(); er != nil {
						return nil, er
					}
					if len(devB) != id.Size || len(xpubB) != 32 {
						return nil, errors.New("storage: bad member entry")
					}
					var dev id.DeviceID
					var xpub [32]byte
					copy(dev[:], devB)
					copy(xpub[:], xpubB)
					meta.Members[dev] = xpub
				}
				k.Spaces[tid] = meta
			}
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	return k, nil
}

// SaveKeystore seals and atomically writes the keystore.
func (r *Root) SaveKeystore(k *Keystore) error {
	aead, err := chacha20poly1305.NewX(r.key[:])
	if err != nil {
		return err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := append(append([]byte(nil), nonce...), aead.Seal(nil, nonce, k.encode(), keystoreAAD)...)
	path := filepath.Join(r.dir, "keys", "keystore.enc")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ErrWrongPassphrase is returned when the keystore does not open.
var ErrWrongPassphrase = errors.New("storage: wrong passphrase or corrupted keystore")

// LoadKeystore reads and opens the keystore. (nil, os.ErrNotExist) means a
// fresh root.
func (r *Root) LoadKeystore() (*Keystore, error) {
	data, err := os.ReadFile(filepath.Join(r.dir, "keys", "keystore.enc"))
	if err != nil {
		return nil, err
	}
	if len(data) < chacha20poly1305.NonceSizeX+1 {
		return nil, ErrWrongPassphrase
	}
	aead, err := chacha20poly1305.NewX(r.key[:])
	if err != nil {
		return nil, err
	}
	nonce, box := data[:chacha20poly1305.NonceSizeX], data[chacha20poly1305.NonceSizeX:]
	plain, err := aead.Open(nil, nonce, box, keystoreAAD)
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	return decodeKeystore(plain)
}

// ---- Blob store (content-addressed) ----

// PutBlob stores bytes under their SHA-256, once.
func (r *Root) PutBlob(data []byte) (id.Hash, error) {
	h := id.HashOf(data)
	path := r.blobPath(h)
	if _, err := os.Stat(path); err == nil {
		return h, nil // already stored; content-addressing dedups
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return h, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return h, err
	}
	return h, os.Rename(tmp, path)
}

// GetBlob fetches a blob and re-verifies its hash — corrupted blobs are
// rejected, never served (fail closed).
func (r *Root) GetBlob(h id.Hash) ([]byte, error) {
	data, err := os.ReadFile(r.blobPath(h))
	if err != nil {
		return nil, err
	}
	if id.HashOf(data) != h {
		return nil, errors.New("storage: blob content does not match its hash")
	}
	return data, nil
}

// HasBlob reports local availability (missing blobs are a normal state the
// UI must show honestly, ADR-006).
func (r *Root) HasBlob(h id.Hash) bool {
	_, err := os.Stat(r.blobPath(h))
	return err == nil
}

func (r *Root) blobPath(h id.Hash) string {
	hex := h.Hex()
	return filepath.Join(r.dir, "blobs", "sha256", hex[:2], hex)
}
