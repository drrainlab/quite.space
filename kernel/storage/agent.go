// The local assistant's identity on disk (AI-0).
//
// Three keys and a space id, and the shape of the record is the argument.
//
// It has its OWN terminal seed and its OWN device seed, because a terminal
// key is what authorship is enforced against and a device key is what event
// chains are ordered by — two participants sharing either one would be
// unable to write honestly or unable to write at all.
//
// It has NO principal seed. The controller claim on its manifest is the
// person's own principal: this is an assistant somebody runs, not an
// independent subject, and giving it a principal of its own would promote
// it to one wherever principals are compared. That is a trust decision, not
// a storage detail, and this is the file where it is visible.
package storage

import (
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// AgentRecord is the local assistant. A zero value means there is none yet:
// it is created on first use, because configuring a provider is not the
// same as asking for a space.
type AgentRecord struct {
	// DeviceSeed and DeviceX25519 are the agent's own signing and wrapping
	// keys. Its own, so its chain never collides with the person's.
	DeviceSeed   []byte
	DeviceX25519 [32]byte
	// TerminalSeed and ManifestFrame are the agent terminal it publishes
	// as. The frame is kept so its revision chain survives a restart.
	TerminalSeed  []byte
	ManifestFrame []byte
	// Space is where the assistant is spoken to. Zero until it exists.
	Space id.TerminalID
}

// Exists reports whether this device has an assistant at all.
func (a AgentRecord) Exists() bool { return len(a.TerminalSeed) > 0 }

const agentFields = 5

func appendAgentRecord(buf []byte, a AgentRecord) []byte {
	buf = codec.AppendArray(buf, agentFields)
	buf = codec.AppendBytes(buf, a.DeviceSeed)
	buf = codec.AppendBytes(buf, a.DeviceX25519[:])
	buf = codec.AppendBytes(buf, a.TerminalSeed)
	buf = codec.AppendBytes(buf, a.ManifestFrame)
	buf = codec.AppendBytes(buf, a.Space[:])
	return buf
}

func readAgentRecord(d *codec.Decoder) (AgentRecord, error) {
	var a AgentRecord
	acount, err := d.ReadArray()
	if err != nil {
		return a, err
	}
	if a.DeviceSeed, err = readCopy(d); err != nil {
		return a, err
	}
	x, err := d.ReadBytes()
	if err != nil {
		return a, err
	}
	copy(a.DeviceX25519[:], x)
	if a.TerminalSeed, err = readCopy(d); err != nil {
		return a, err
	}
	if a.ManifestFrame, err = readCopy(d); err != nil {
		return a, err
	}
	sp, err := d.ReadBytes()
	if err != nil {
		return a, err
	}
	copy(a.Space[:], sp)
	// Same forward-compat tail as every other record here: a newer build's
	// extra fields are skipped, not stumbled over.
	for i := agentFields; i < acount; i++ {
		if e := d.SkipItem(); e != nil {
			return a, e
		}
	}
	return a, nil
}
