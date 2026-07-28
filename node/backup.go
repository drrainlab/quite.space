// Backing up, and coming back.
//
// The recovery bundle that already exists (kernel/identity) carries two
// things: the principal seed and device certificates. The keystore carries
// eleven, among them TerminalSeeds, Epochs, Spaces and PublicPublish. So
// restoring that bundle on a clean machine gives a person back WHO THEY ARE
// and nothing they were IN — no spaces, no epoch keys, no history. For a
// local-first app with no server to restore from, that is the gap between
// "the beta was rough" and "the beta ate my life".
//
// So this backs up the whole data directory instead of growing the bundle.
// It is the less clever option and the more honest one: whatever the node
// needs to be itself is, by construction, in the directory the node runs from
// — including things a hand-written bundle would forget. One of those is
// PublicPublish.ProjectionSeq, which the keystore comments mark as never
// resettable: a public publisher restored without it can never publish again,
// because readers reject the sequence regression forever.
//
// Encrypted with the same scrypt + XChaCha20-Poly1305 shape as the keystore,
// under a passphrase the person chooses for the backup — not necessarily the
// one that opens the node, because a backup travels and a node does not.
package node

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"

	"github.com/drrainlab/quiet_places/kernel/storage"
)

const (
	backupMagic   = "QSBACKUP1"
	backupSaltLen = 16
	// Same cost as the keystore's own derivation. A backup file is the one
	// artifact that leaves the device, so it gets no less.
	backupScryptN = 1 << 15
	backupScryptR = 8
	backupScryptP = 1
)

var backupAAD = []byte("qs.backup.v1")

// zeroTime flattens every timestamp in the archive. Modification times are
// not what makes a node work, and they are a activity log in disguise.
var zeroTime = time.Unix(0, 0).UTC()

// ErrBackupUnreadable covers a wrong passphrase and a corrupted file alike.
// They are one error on purpose: distinguishing them tells someone grinding a
// stolen backup when they are close.
var ErrBackupUnreadable = errors.New(
	"node: this backup did not open — wrong passphrase, or the file is damaged")

// skipFromBackup names what must NOT travel.
//
// The lock is the whole point: a backup containing node.lock would restore a
// file that means nothing (locks live on descriptors, not on disk), but a
// future reader might be tempted to believe it. Temporary files are half-
// written by definition.
func skipFromBackup(rel string) bool {
	base := filepath.Base(rel)
	return base == storage.LockFileName || strings.HasSuffix(base, ".tmp")
}

// WriteBackup seals the entire data directory into w.
//
// Safe to call while the node is running: it only reads. What it cannot do is
// promise a torn-free snapshot of a directory being written to, so the honest
// use is "back up regularly", not "back up instead of shutting down cleanly".
func WriteBackup(dataDir string, passphrase []byte, w io.Writer) error {
	if len(passphrase) < 8 {
		return storage.ErrPassphraseTooShort
	}
	if !storage.HasRoot(dataDir) {
		return storage.ErrNoRootHere
	}

	salt := make([]byte, backupSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	aead, err := backupAEAD(passphrase, salt)
	if err != nil {
		return err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}

	// Header in the clear: a magic string so a person can identify the file,
	// and the salt+nonce the reader needs. Nothing else — not the directory
	// name, not a file list, not a count.
	if _, err := w.Write([]byte(backupMagic)); err != nil {
		return err
	}
	if _, err := w.Write(salt); err != nil {
		return err
	}
	if _, err := w.Write(nonce); err != nil {
		return err
	}

	plain, err := tarGzip(dataDir)
	if err != nil {
		return err
	}
	_, err = w.Write(aead.Seal(nil, nonce, plain, backupAAD))
	return err
}

// ReadBackup restores a sealed backup into dir, which must be empty or absent.
//
// Refusing a non-empty directory is deliberate. Merging a backup into a live
// root would interleave two histories, and the failure would surface much
// later as a forked chain nobody can explain.
func ReadBackup(dir string, passphrase []byte, r io.Reader) error {
	if len(passphrase) < 8 {
		return storage.ErrPassphraseTooShort
	}
	if err := requireEmptyDir(dir); err != nil {
		return err
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	head := len(backupMagic) + backupSaltLen + chacha20poly1305.NonceSizeX
	if len(raw) <= head || string(raw[:len(backupMagic)]) != backupMagic {
		return ErrBackupUnreadable
	}
	salt := raw[len(backupMagic) : len(backupMagic)+backupSaltLen]
	nonce := raw[len(backupMagic)+backupSaltLen : head]

	aead, err := backupAEAD(passphrase, salt)
	if err != nil {
		return err
	}
	plain, err := aead.Open(nil, nonce, raw[head:], backupAAD)
	if err != nil {
		return ErrBackupUnreadable
	}
	return untarGzip(dir, plain)
}

func backupAEAD(passphrase, salt []byte) (interface {
	Seal(dst, nonce, plaintext, ad []byte) []byte
	Open(dst, nonce, ciphertext, ad []byte) ([]byte, error)
}, error) {
	key, err := scrypt.Key(passphrase, salt, backupScryptN, backupScryptR,
		backupScryptP, chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	return chacha20poly1305.NewX(key)
}

func requireEmptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf(
			"node: %s is not empty — restore into a fresh directory, because merging "+
				"a backup into existing data interleaves two histories and the damage "+
				"only shows up later, as a forked chain nobody can explain", dir)
	}
	return nil
}

func tarGzip(dataDir string) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)

	err := filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if skipFromBackup(rel) {
			return nil
		}
		// Symlinks are not followed and not stored: a data root has no use
		// for them, and following one would copy something from outside the
		// directory into a file the person believes is only their node.
		if !d.Type().IsRegular() && !d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		// Timestamps are not what makes a node work, and they leak activity.
		hdr.ModTime = zeroTime
		hdr.AccessTime = zeroTime
		hdr.ChangeTime = zeroTime
		hdr.Uname, hdr.Gname = "", ""
		hdr.Uid, hdr.Gid = 0, 0
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func untarGzip(dir string, plain []byte) error {
	zr, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		return ErrBackupUnreadable
	}
	tr := tar.NewReader(zr)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return ErrBackupUnreadable
		}
		// Path traversal: a hostile archive naming ../../.ssh/authorized_keys
		// must land nowhere. Rejecting is right — a legitimate backup never
		// contains such a name, so this can only ever be an attack or damage.
		clean := filepath.Clean(filepath.FromSlash(hdr.Name))
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) ||
			filepath.IsAbs(clean) {
			return fmt.Errorf("node: this backup contains a path that escapes the "+
				"restore directory (%q) and was refused", hdr.Name)
		}
		target := filepath.Join(dir, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			// Anything else — symlink, device, fifo — is not part of a data
			// root and is silently dropped rather than recreated.
		}
	}
}
