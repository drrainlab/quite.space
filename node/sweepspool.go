package node

// The sweep fix spool (SP-3.2, ADR-034): one append-only file per
// session, in the connector journal's exact discipline — len‖crc32c
// records, torn-tail truncation on open, mutate-under-lock. The fixes
// live HERE and never in the keystore, which is rewritten whole on
// every save while a fix arrives every few seconds for an hour.
//
// Plaintext, deliberately: this directory is inside the data root a
// person already protects, the spool is deleted at finalize (its truth
// moves into an encrypted asset), and encrypting a file that turns
// over this fast would buy little. What IS bought carefully is fsync:
//
//   a lost TAIL OF POINTS costs a gap in the track, which the format
//   renders honestly — the cheap write is the one allowed to be late
//   (the notify ledger's asymmetry argument);
//   a lost GAP RECORD costs a LIE — a renderer would join across a
//   span the recorder knew it had missed. So gaps always sync, points
//   sync every fourth batch.
//
// One host batch = one spool record: atomic under torn-tail
// truncation, which makes retry idempotency structural — replay
// derives lastSeq, and a re-POSTed batch at or below it is a no-op.

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/field"
)

const (
	spoolRecBatch   = 1 // a host batch of samples
	spoolRecNodeGap = 2 // a node-authored gap (its own lifecycle only)

	spKeyKind    = 1
	spKeySeq     = 2
	spKeySamples = 3
	spKeyUnixMS  = 4
	spKeyDurMS   = 5
	spKeyReason  = 6

	// spoolSyncEvery: point batches fsync every Nth append; gaps always.
	spoolSyncEvery = 4
)

var spoolCrc = crc32.MakeTable(crc32.Castagnoli)

// SpoolSample is one recorded item with an ABSOLUTE clock: resume
// across any restart needs no arithmetic, and torn tails stay honest.
// dt is computed once, at seal.
type SpoolSample struct {
	Tag        uint64 // field.SampleQPoint | field.SampleQGap
	UnixMS     uint64
	LatE7U     uint64
	LonE7U     uint64
	AccuracyM  uint64
	DurationMS uint64
	Reason     uint64
}

type sweepSpool struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	samples []SpoolSample
	lastSeq uint64
	appends int
}

func sweepSpoolDir(dataDir string, sweepID [16]byte) string {
	return filepath.Join(dataDir, "sweeps", fmt.Sprintf("%x", sweepID))
}

func openSweepSpool(dir string) (*sweepSpool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sp := &sweepSpool{path: filepath.Join(dir, "track.spool")}
	if err := sp.replay(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(sp.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	sp.f = f
	return sp, nil
}

func (sp *sweepSpool) replay() error {
	f, err := os.Open(sp.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	valid := int64(0)
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(f, hdr[:]); err != nil {
			break
		}
		n := binary.BigEndian.Uint32(hdr[:4])
		want := binary.BigEndian.Uint32(hdr[4:])
		if n == 0 || n > 1<<20 {
			break
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(f, body); err != nil {
			break
		}
		if crc32.Checksum(body, spoolCrc) != want {
			break // torn record: everything after it is suspect
		}
		valid += 8 + int64(n)
		sp.apply(body)
	}
	return os.Truncate(sp.path, valid)
}

func (sp *sweepSpool) apply(body []byte) {
	d := codec.NewDecoder(body)
	m, err := d.ReadMapHeader()
	if err != nil {
		return
	}
	var kind, seq, unixMS, durMS, reason uint64
	var batch []SpoolSample
	for {
		k, ok, err := m.Next()
		if err != nil || !ok {
			break
		}
		switch k {
		case spKeyKind:
			kind, err = d.ReadUint()
		case spKeySeq:
			seq, err = d.ReadUint()
		case spKeySamples:
			var n int
			if n, err = d.ReadArray(); err == nil {
				for i := 0; i < n && err == nil; i++ {
					var s SpoolSample
					s, err = readSpoolSample(d)
					batch = append(batch, s)
				}
			}
		case spKeyUnixMS:
			unixMS, err = d.ReadUint()
		case spKeyDurMS:
			durMS, err = d.ReadUint()
		case spKeyReason:
			reason, err = d.ReadUint()
		default:
			err = d.SkipItem()
		}
		if err != nil {
			return
		}
	}
	switch kind {
	case spoolRecBatch:
		if seq <= sp.lastSeq {
			return // replayed duplicate
		}
		sp.lastSeq = seq
		sp.samples = append(sp.samples, batch...)
	case spoolRecNodeGap:
		sp.samples = append(sp.samples, SpoolSample{
			Tag: field.SampleQGap, UnixMS: unixMS, DurationMS: durMS, Reason: reason,
		})
	}
}

func readSpoolSample(d *codec.Decoder) (SpoolSample, error) {
	var s SpoolSample
	n, err := d.ReadArray()
	if err != nil {
		return s, err
	}
	fields := []*uint64{&s.Tag, &s.UnixMS, &s.LatE7U, &s.LonE7U, &s.AccuracyM, &s.DurationMS, &s.Reason}
	for i := 0; i < n; i++ {
		if i < len(fields) {
			if *fields[i], err = d.ReadUint(); err != nil {
				return s, err
			}
			continue
		}
		if err := d.SkipItem(); err != nil {
			return s, err
		}
	}
	return s, nil
}

func appendSpoolSample(buf []byte, s SpoolSample) []byte {
	buf = codec.AppendArray(buf, 7)
	buf = codec.AppendUint(buf, s.Tag)
	buf = codec.AppendUint(buf, s.UnixMS)
	buf = codec.AppendUint(buf, s.LatE7U)
	buf = codec.AppendUint(buf, s.LonE7U)
	buf = codec.AppendUint(buf, s.AccuracyM)
	buf = codec.AppendUint(buf, s.DurationMS)
	buf = codec.AppendUint(buf, s.Reason)
	return buf
}

// AppendBatch spools one host batch. Idempotent by seq: a batch at or
// below the high-water mark answers ok without writing (the retry that
// already landed).
func (sp *sweepSpool) AppendBatch(seq uint64, samples []SpoolSample) (bool, error) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if seq <= sp.lastSeq {
		return false, nil
	}
	hasGap := false
	for _, s := range samples {
		if s.Tag == field.SampleQGap {
			hasGap = true
		}
	}
	body := codec.AppendMap(nil, 3)
	body = codec.AppendUint(body, spKeyKind)
	body = codec.AppendUint(body, spoolRecBatch)
	body = codec.AppendUint(body, spKeySeq)
	body = codec.AppendUint(body, seq)
	body = codec.AppendUint(body, spKeySamples)
	body = codec.AppendArray(body, len(samples))
	for _, s := range samples {
		body = appendSpoolSample(body, s)
	}
	sp.appends++
	sync := hasGap || sp.appends%spoolSyncEvery == 0
	if err := sp.appendRecord(body, sync); err != nil {
		return false, err
	}
	sp.lastSeq = seq
	sp.samples = append(sp.samples, samples...)
	return true, nil
}

// AppendNodeGap records a gap the NODE proved from its own lifecycle
// (a restart). Always synced: a lost gap record is a lie waiting to be
// rendered.
func (sp *sweepSpool) AppendNodeGap(unixMS, durationMS, reason uint64) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	body := codec.AppendMap(nil, 4)
	body = codec.AppendUint(body, spKeyKind)
	body = codec.AppendUint(body, spoolRecNodeGap)
	body = codec.AppendUint(body, spKeyUnixMS)
	body = codec.AppendUint(body, unixMS)
	body = codec.AppendUint(body, spKeyDurMS)
	body = codec.AppendUint(body, durationMS)
	body = codec.AppendUint(body, spKeyReason)
	body = codec.AppendUint(body, reason)
	if err := sp.appendRecord(body, true); err != nil {
		return err
	}
	sp.samples = append(sp.samples, SpoolSample{
		Tag: field.SampleQGap, UnixMS: unixMS, DurationMS: durationMS, Reason: reason,
	})
	return nil
}

func (sp *sweepSpool) appendRecord(body []byte, sync bool) error {
	var buf []byte
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(body)))
	buf = binary.BigEndian.AppendUint32(buf, crc32.Checksum(body, spoolCrc))
	buf = append(buf, body...)
	if _, err := sp.f.Write(buf); err != nil {
		return err
	}
	if sync {
		return sp.f.Sync()
	}
	return nil
}

func (sp *sweepSpool) LastSeq() uint64 {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return sp.lastSeq
}

// LastSampleUnixMS is the clock of the latest recorded moment — what a
// resume measures its suspended gap against.
func (sp *sweepSpool) LastSampleUnixMS() uint64 {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	var last uint64
	for _, s := range sp.samples {
		end := s.UnixMS + s.DurationMS
		if end > last {
			last = end
		}
	}
	return last
}

// Samples returns the recorded stream in order, for sealing.
func (sp *sweepSpool) Samples() []SpoolSample {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	out := make([]SpoolSample, len(sp.samples))
	copy(out, sp.samples)
	return out
}

func (sp *sweepSpool) Close() error {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.f == nil {
		return nil
	}
	err := sp.f.Close()
	sp.f = nil
	return err
}
