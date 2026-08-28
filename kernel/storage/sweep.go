package storage

// SweepRecord persists one recording session (SP-3.2, ADR-034) — the
// InstrumentRecord shape: a session that was recording when the node
// went down must be findable when it comes back, or a restart silently
// turns a live operation into a lost track.
//
// The record is the session's IDENTITY AND SAGA STATE only. The fixes
// themselves never touch the keystore: they append to the plaintext
// spool at dataDir/sweeps/<id>/track.spool (the connector-journal
// discipline), because the keystore is rewritten whole on every save
// and a fix arrives every few seconds for an hour.
//
// The tail sub-array holds the finalization saga's idempotency markers:
// each completed step records its proof, so a re-driven saga after a
// crash repeats nothing and skips nothing.

import (
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// Sweep session states. Serialized; values must not move.
const (
	SweepStarting  uint64 = 1 // start saga running: record exists, object may not yet
	SweepRecording uint64 = 2
	SweepSuspended uint64 = 3 // restored after a restart, awaiting a resume claim
	SweepStopping  uint64 = 4 // capture closed, finalize not yet begun
	SweepStopped   uint64 = 5 // result declared, finalization saga running/pending
	SweepDone      uint64 = 6
)

type SweepRecord struct {
	// Space is the room the sweep belongs to.
	Space id.TerminalID
	// SweepID doubles as the Sweep Object's id — one identity, minted
	// BEFORE anything else exists so the start saga can crash anywhere
	// and be re-driven against the same name.
	SweepID   [16]byte
	ParentID  [16]byte // the sector/place being swept
	TaskID    []byte   // optional linked card EventID ("" = none)
	Label     string
	StartedAt uint64
	State     uint64
	Result    string // closed slug, "" until declared
	Note      string // operator prose, held for NoteObservation at finalize

	// Saga markers (the nested tail array, the InstrumentRecord pattern).
	StoppedAt        uint64
	ObjectCreated    bool   // start saga: the Sweep Object exists
	AssetHex         string // finalize 2: track sealed and ingested
	BlockEventID     []byte // finalize 4: block.attached.v1 carrier emitted
	EdgeEventID      []byte // finalize 5: object.attached.v1 role "track"
	CompletedEventID []byte // finalize 6: sweep.completed.v1 emitted
	CardDone         bool   // finalize 7: linked task closed
	NoteEventID      []byte // finalize 8: observation on the parent
	ObjectRevised    bool   // finalize 9: status cache revision
}

func (s SweepRecord) Exists() bool { return s.SweepID != ([16]byte{}) }

// sweepFields is the record's positional arity; append-only forever.
const sweepFields = 9

func appendSweepRecord(buf []byte, r SweepRecord) []byte {
	buf = codec.AppendArray(buf, sweepFields)
	buf = codec.AppendBytes(buf, r.Space[:])
	buf = codec.AppendBytes(buf, r.SweepID[:])
	buf = codec.AppendBytes(buf, r.ParentID[:])
	buf = codec.AppendBytes(buf, r.TaskID)
	buf = codec.AppendText(buf, r.Label)
	buf = codec.AppendUint(buf, r.StartedAt)
	buf = codec.AppendUint(buf, r.State)
	buf = codec.AppendText(buf, r.Result)
	// The tail rides one nested array so the outer arity stays a clean
	// count of concerns; an older reader skips extras by the tail rule.
	buf = codec.AppendArray(buf, 10)
	buf = codec.AppendText(buf, r.Note)
	buf = codec.AppendUint(buf, r.StoppedAt)
	buf = codec.AppendBool(buf, r.ObjectCreated)
	buf = codec.AppendText(buf, r.AssetHex)
	buf = codec.AppendBytes(buf, r.BlockEventID)
	buf = codec.AppendBytes(buf, r.EdgeEventID)
	buf = codec.AppendBytes(buf, r.CompletedEventID)
	buf = codec.AppendBool(buf, r.CardDone)
	buf = codec.AppendBytes(buf, r.NoteEventID)
	buf = codec.AppendBool(buf, r.ObjectRevised)
	return buf
}

func readSweepRecord(d *codec.Decoder) (SweepRecord, error) {
	var r SweepRecord
	n, err := d.ReadArray()
	if err != nil {
		return r, err
	}
	b, err := d.ReadBytes()
	if err != nil {
		return r, err
	}
	copy(r.Space[:], b)
	if b, err = d.ReadBytes(); err != nil {
		return r, err
	}
	copy(r.SweepID[:], b)
	if b, err = d.ReadBytes(); err != nil {
		return r, err
	}
	copy(r.ParentID[:], b)
	if r.TaskID, err = readCopy(d); err != nil {
		return r, err
	}
	if r.Label, err = d.ReadText(); err != nil {
		return r, err
	}
	if r.StartedAt, err = d.ReadUint(); err != nil {
		return r, err
	}
	if r.State, err = d.ReadUint(); err != nil {
		return r, err
	}
	if r.Result, err = d.ReadText(); err != nil {
		return r, err
	}
	tailN, err := d.ReadArray()
	if err != nil {
		return r, err
	}
	read := 0
	step := func(f func() error) error {
		if read >= tailN {
			return nil
		}
		read++
		return f()
	}
	for _, f := range []func() error{
		func() (e error) { r.Note, e = d.ReadText(); return },
		func() (e error) { r.StoppedAt, e = d.ReadUint(); return },
		func() (e error) { r.ObjectCreated, e = d.ReadBool(); return },
		func() (e error) { r.AssetHex, e = d.ReadText(); return },
		func() (e error) { r.BlockEventID, e = readCopy(d); return },
		func() (e error) { r.EdgeEventID, e = readCopy(d); return },
		func() (e error) { r.CompletedEventID, e = readCopy(d); return },
		func() (e error) { r.CardDone, e = d.ReadBool(); return },
		func() (e error) { r.NoteEventID, e = readCopy(d); return },
		func() (e error) { r.ObjectRevised, e = d.ReadBool(); return },
	} {
		if err := step(f); err != nil {
			return r, err
		}
	}
	for i := read; i < tailN; i++ {
		if e := d.SkipItem(); e != nil {
			return r, e
		}
	}
	// Forward-compat tail, as every record here keeps.
	for i := sweepFields; i < n; i++ {
		if e := d.SkipItem(); e != nil {
			return r, e
		}
	}
	return r, nil
}
