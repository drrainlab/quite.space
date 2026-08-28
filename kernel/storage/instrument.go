package storage

// InstrumentRecord persists one instrument participant (QI-1) — the
// AgentRecord shape, one per instrument rather than one per node,
// because a household grows greenhouses faster than it grows assistants.
//
// The simulator flag rides here deliberately: the reference driver is
// not a mock, and an instrument that was simulating when the node went
// down resumes simulating when it comes back — a restart must not
// silently turn a demo greenhouse into a dead card.

import (
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

type InstrumentRecord struct {
	// Space is the room this instrument inhabits.
	Space id.TerminalID
	// Label is the human name ("Greenhouse").
	Label string
	// Kind is "sensor" or "actuator" (manifest vocabulary).
	Kind string
	// Channels is the declared panel, in qp.instr label form — the same
	// signed strings the manifest carries, so restart re-mints the exact
	// manifest revision chain.
	Channels []string
	// DeviceSeed and DeviceX25519 are the instrument's own keys.
	DeviceSeed   []byte
	DeviceX25519 [32]byte
	// TerminalSeed and ManifestFrame are its participant identity — the
	// InstrumentID is this terminal's id.
	TerminalSeed  []byte
	ManifestFrame []byte
	// Simulated marks the built-in reference driver; real drivers leave
	// it false and bring their own transport.
	Simulated bool
	// SimSeed makes the simulator deterministic (owner's amendment 8).
	SimSeed uint64
	// SimTickSeconds overrides the reference cadence (0 = the driver's
	// default). Born on the first PUBLIC greenhouse: a 5s tick is fine in
	// a private room, but in a broadcast space every reading republishes
	// the projection, and the relay rate-limited the node for it.
	SimTickSeconds uint64
	// External marks an instrument whose private keys live ON THE DEVICE
	// (QI-M, ADR-026): this node holds only the public halves below and
	// can neither sign nor seal for it. Seeds above are empty for such a
	// record — by construction, not by convention.
	External    bool
	DevicePub   id.DeviceID
	X25519Pub   [32]byte
	TerminalPub id.TerminalID
}

func (i InstrumentRecord) Exists() bool { return len(i.TerminalSeed) > 0 || i.External }

// instrFields is the record's positional arity; append-only forever.
const instrFields = 9

func appendInstrumentRecord(buf []byte, r InstrumentRecord) []byte {
	buf = codec.AppendArray(buf, instrFields)
	buf = codec.AppendBytes(buf, r.Space[:])
	buf = codec.AppendText(buf, r.Label)
	buf = codec.AppendText(buf, r.Kind)
	buf = codec.AppendArray(buf, len(r.Channels))
	for _, c := range r.Channels {
		buf = codec.AppendText(buf, c)
	}
	buf = codec.AppendBytes(buf, r.DeviceSeed)
	buf = codec.AppendBytes(buf, r.DeviceX25519[:])
	buf = codec.AppendBytes(buf, r.TerminalSeed)
	buf = codec.AppendBytes(buf, r.ManifestFrame)
	// The two simulator fields ride one nested array so the outer arity
	// stays a clean count of concerns.
	// Items 2–5 (QI-M) carry the external device's public halves; an
	// older reader skips them by the tail rule below.
	buf = codec.AppendArray(buf, 7)
	buf = codec.AppendBool(buf, r.Simulated)
	buf = codec.AppendUint(buf, r.SimSeed)
	buf = codec.AppendBool(buf, r.External)
	buf = codec.AppendBytes(buf, r.DevicePub[:])
	buf = codec.AppendBytes(buf, r.X25519Pub[:])
	buf = codec.AppendBytes(buf, r.TerminalPub[:])
	buf = codec.AppendUint(buf, r.SimTickSeconds)
	return buf
}

func readInstrumentRecord(d *codec.Decoder) (InstrumentRecord, error) {
	var r InstrumentRecord
	n, err := d.ReadArray()
	if err != nil {
		return r, err
	}
	sp, err := d.ReadBytes()
	if err != nil {
		return r, err
	}
	copy(r.Space[:], sp)
	if r.Label, err = d.ReadText(); err != nil {
		return r, err
	}
	if r.Kind, err = d.ReadText(); err != nil {
		return r, err
	}
	cn, err := d.ReadArray()
	if err != nil {
		return r, err
	}
	for i := 0; i < cn; i++ {
		c, err := d.ReadText()
		if err != nil {
			return r, err
		}
		r.Channels = append(r.Channels, c)
	}
	if r.DeviceSeed, err = readCopy(d); err != nil {
		return r, err
	}
	x, err := d.ReadBytes()
	if err != nil {
		return r, err
	}
	copy(r.DeviceX25519[:], x)
	if r.TerminalSeed, err = readCopy(d); err != nil {
		return r, err
	}
	if r.ManifestFrame, err = readCopy(d); err != nil {
		return r, err
	}
	simN, err := d.ReadArray()
	if err != nil {
		return r, err
	}
	if simN >= 2 {
		if r.Simulated, err = d.ReadBool(); err != nil {
			return r, err
		}
		if r.SimSeed, err = d.ReadUint(); err != nil {
			return r, err
		}
	}
	read := 2
	if simN >= 6 {
		if r.External, err = d.ReadBool(); err != nil {
			return r, err
		}
		for _, dst := range [][]byte{r.DevicePub[:], r.X25519Pub[:], r.TerminalPub[:]} {
			b, err := d.ReadBytes()
			if err != nil {
				return r, err
			}
			copy(dst, b)
		}
		read = 6
	}
	if simN >= 7 {
		if r.SimTickSeconds, err = d.ReadUint(); err != nil {
			return r, err
		}
		read = 7
	}
	for i := read; i < simN; i++ {
		if e := d.SkipItem(); e != nil {
			return r, e
		}
	}
	// Forward-compat tail, as every record here keeps.
	for i := instrFields; i < n; i++ {
		if e := d.SkipItem(); e != nil {
			return r, e
		}
	}
	return r, nil
}
