// Package bundle is the T1 transport (plan §19): export a terminal's frames
// to a *.terminal-bundle file that travels by flash drive, QR, email, or any
// other channel.
//
// Honesty note: v0 bundles carry the signed frames as-is. Payload secrecy is
// exactly the payload's own encryption state (ADR-005 arrives in M1); the
// bundle container adds integrity (per-frame signatures) but no additional
// confidentiality yet, and nothing here claims otherwise.
package bundle

import (
	"errors"
	"fmt"
	"os"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// magic identifies bundle files; version gates format changes.
const (
	magic   = "QPB0"
	version = 0
)

// Bundle key table v0 (append-only, ADR-009: old decoders skip key 4).
const (
	keyVersion  = 1
	keyTerminal = 2
	keyFrames   = 3
	keyBlobs    = 4 // encrypted asset blobs (manifests and chunks)
)

// Encode serializes frames for one terminal (same bytes as the file form —
// bundles travel identically by file, QR, or relay).
func Encode(terminal id.TerminalID, frames [][]byte) []byte {
	return EncodeWithBlobs(terminal, frames, nil)
}

// EncodeWithBlobs additionally carries encrypted asset blobs — the offline
// path for media. A blob is opaque here; possession proves nothing about
// access (keys travel inside the epoch-encrypted block events).
func EncodeWithBlobs(terminal id.TerminalID, frames [][]byte, blobs [][]byte) []byte {
	n := 3
	if len(blobs) > 0 {
		n++
	}
	buf := []byte(magic)
	buf = codec.AppendMap(buf, n)
	buf = codec.AppendUint(buf, keyVersion)
	buf = codec.AppendUint(buf, version)
	buf = codec.AppendUint(buf, keyTerminal)
	buf = codec.AppendBytes(buf, terminal[:])
	buf = codec.AppendUint(buf, keyFrames)
	buf = codec.AppendArray(buf, len(frames))
	for _, f := range frames {
		buf = codec.AppendBytes(buf, f)
	}
	if len(blobs) > 0 {
		buf = codec.AppendUint(buf, keyBlobs)
		buf = codec.AppendArray(buf, len(blobs))
		for _, b := range blobs {
			buf = codec.AppendBytes(buf, b)
		}
	}
	return buf
}

// Write exports frames for one terminal to a file.
func Write(path string, terminal id.TerminalID, frames [][]byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, Encode(terminal, frames), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Read imports a bundle file.
func Read(path string) (id.TerminalID, [][]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return id.TerminalID{}, nil, err
	}
	return Decode(data)
}

// Decode parses bundle bytes, returning the terminal and its frames.
func Decode(data []byte) (id.TerminalID, [][]byte, error) {
	tid, frames, _, err := DecodeFull(data)
	return tid, frames, err
}

// DecodeFull additionally returns carried asset blobs. Frames and blobs are
// opaque here — validation happens in the event log and the asset layer,
// not in the transport (ADR-007).
func DecodeFull(data []byte) (id.TerminalID, [][]byte, [][]byte, error) {
	var terminal id.TerminalID
	if len(data) < len(magic) || string(data[:len(magic)]) != magic {
		return terminal, nil, nil, errors.New("bundle: not a terminal-bundle file")
	}
	d := codec.NewDecoder(data[len(magic):])
	m, err := d.ReadMapHeader()
	if err != nil {
		return terminal, nil, nil, err
	}
	var frames [][]byte
	var blobs [][]byte
	var seenTerminal bool
	for {
		k, ok, err := m.Next()
		if err != nil {
			return terminal, nil, nil, err
		}
		if !ok {
			break
		}
		switch k {
		case keyVersion:
			v, e := d.ReadUint()
			if e != nil {
				return terminal, nil, nil, e
			}
			if v != version {
				return terminal, nil, nil, fmt.Errorf("bundle: unsupported version %d", v)
			}
		case keyTerminal:
			b, e := d.ReadBytes()
			if e != nil {
				return terminal, nil, nil, e
			}
			if len(b) != id.Size {
				return terminal, nil, nil, errors.New("bundle: bad terminal id")
			}
			copy(terminal[:], b)
			seenTerminal = true
		case keyFrames:
			n, e := d.ReadArray()
			if e != nil {
				return terminal, nil, nil, e
			}
			for range n {
				f, e := d.ReadBytes()
				if e != nil {
					return terminal, nil, nil, e
				}
				frames = append(frames, append([]byte(nil), f...))
			}
		case keyBlobs:
			n, e := d.ReadArray()
			if e != nil {
				return terminal, nil, nil, e
			}
			for range n {
				b, e := d.ReadBytes()
				if e != nil {
					return terminal, nil, nil, e
				}
				blobs = append(blobs, append([]byte(nil), b...))
			}
		default:
			if err := d.SkipItem(); err != nil {
				return terminal, nil, nil, err
			}
		}
	}
	if err := d.Done(); err != nil {
		return terminal, nil, nil, err
	}
	if !seenTerminal {
		return terminal, nil, nil, errors.New("bundle: missing terminal id")
	}
	return terminal, frames, blobs, nil
}
