// Which radio this device attaches, and to which segment.
//
// It lives in the KEYSTORE rather than beside relays.json, and the reason is
// the seed. A segment seed derives the frame-authentication key for every
// radio on the segment; anyone holding it can forge control frames and stop
// any transfer on the air at will. That is bearer material, and this project's
// plain-JSON files are contractually forbidden from holding any — the comment
// on quicklinks.json says so, and a test holds it to it.
//
// It is stored at all because the alternative is worse than the flag it
// replaces. Attaching a radio from a screen and losing it on the next restart
// would be a button that quietly undoes itself, and the whole point of the
// failover doctrine is that a radio path is ready BEFORE the internet goes
// away — not something a person is asked to re-establish at the exact moment
// nobody can be reached.
package storage

import "github.com/drrainlab/quiet_places/protocol/codec"

// RadioRecord is the radio this device attaches on start. A zero value means
// no radio: the ordinary state, and not an error anywhere.
type RadioRecord struct {
	// Carrier names the driver — "rnode" today. Stored rather than inferred
	// from the device path, because a path says nothing about what is on it
	// and guessing is how a modem gets configured as something else.
	Carrier string
	// Device is the serial path. It is the one field here that legitimately
	// goes stale: these boards enumerate under a different name after a reset,
	// so a failed re-attach is an ordinary event and must never be fatal.
	Device string
	// Seed is the SEGMENT SEED — the derived 32 bytes, not the phrase.
	//
	// The phrase is deliberately not kept. Nothing needs it: the key derives
	// from the seed, and a phrase is the one form of this secret a person
	// might reuse elsewhere.
	Seed []byte
}

// Attached reports whether this device should bring a radio up on start.
func (r RadioRecord) Attached() bool {
	return r.Carrier != "" && r.Device != "" && len(r.Seed) > 0
}

const radioFields = 3

func appendRadioRecord(buf []byte, r RadioRecord) []byte {
	buf = codec.AppendArray(buf, radioFields)
	buf = codec.AppendText(buf, r.Carrier)
	buf = codec.AppendText(buf, r.Device)
	buf = codec.AppendBytes(buf, r.Seed)
	return buf
}

func readRadioRecord(d *codec.Decoder) (RadioRecord, error) {
	var r RadioRecord
	acount, err := d.ReadArray()
	if err != nil {
		return r, err
	}
	if r.Carrier, err = d.ReadText(); err != nil {
		return r, err
	}
	if r.Device, err = d.ReadText(); err != nil {
		return r, err
	}
	if r.Seed, err = readCopy(d); err != nil {
		return r, err
	}
	// The same forward-compat tail as every other record here: a newer
	// build's extra fields are skipped rather than stumbled over.
	for i := radioFields; i < acount; i++ {
		if e := d.SkipItem(); e != nil {
			return r, e
		}
	}
	return r, nil
}
