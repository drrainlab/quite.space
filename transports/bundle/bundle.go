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

// Bundle key table v0.
const (
	keyVersion  = 1
	keyTerminal = 2
	keyFrames   = 3
)

// Write exports frames for one terminal.
func Write(path string, terminal id.TerminalID, frames [][]byte) error {
	buf := []byte(magic)
	buf = codec.AppendMap(buf, 3)
	buf = codec.AppendUint(buf, keyVersion)
	buf = codec.AppendUint(buf, version)
	buf = codec.AppendUint(buf, keyTerminal)
	buf = codec.AppendBytes(buf, terminal[:])
	buf = codec.AppendUint(buf, keyFrames)
	buf = codec.AppendArray(buf, len(frames))
	for _, f := range frames {
		buf = codec.AppendBytes(buf, f)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Read imports a bundle, returning the terminal and its frames. Frames are
// opaque here — validation (signatures, chains, admission) happens in the
// event log, not in the transport (ADR-007).
func Read(path string) (id.TerminalID, [][]byte, error) {
	var terminal id.TerminalID
	data, err := os.ReadFile(path)
	if err != nil {
		return terminal, nil, err
	}
	if len(data) < len(magic) || string(data[:len(magic)]) != magic {
		return terminal, nil, errors.New("bundle: not a terminal-bundle file")
	}
	d := codec.NewDecoder(data[len(magic):])
	m, err := d.ReadMapHeader()
	if err != nil {
		return terminal, nil, err
	}
	var frames [][]byte
	var seenTerminal bool
	for {
		k, ok, err := m.Next()
		if err != nil {
			return terminal, nil, err
		}
		if !ok {
			break
		}
		switch k {
		case keyVersion:
			v, e := d.ReadUint()
			if e != nil {
				return terminal, nil, e
			}
			if v != version {
				return terminal, nil, fmt.Errorf("bundle: unsupported version %d", v)
			}
		case keyTerminal:
			b, e := d.ReadBytes()
			if e != nil {
				return terminal, nil, e
			}
			if len(b) != id.Size {
				return terminal, nil, errors.New("bundle: bad terminal id")
			}
			copy(terminal[:], b)
			seenTerminal = true
		case keyFrames:
			n, e := d.ReadArray()
			if e != nil {
				return terminal, nil, e
			}
			for range n {
				f, e := d.ReadBytes()
				if e != nil {
					return terminal, nil, e
				}
				frames = append(frames, append([]byte(nil), f...))
			}
		default:
			if err := d.SkipItem(); err != nil {
				return terminal, nil, err
			}
		}
	}
	if err := d.Done(); err != nil {
		return terminal, nil, err
	}
	if !seenTerminal {
		return terminal, nil, errors.New("bundle: missing terminal id")
	}
	return terminal, frames, nil
}
