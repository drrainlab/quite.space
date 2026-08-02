// Remembering what a radio held, so a bad write can be undone.
//
// Everything else in this package treats the device as the source of truth,
// which is right: what a radio reports is the only evidence about it. But
// there is one thing the device cannot tell you, and it is the question you
// ask the moment something stops working — what did it hold BEFORE.
//
// That question has been unanswerable, and expensively so. When a write
// erased tx_enabled on two boards, nothing recorded that it had ever been on;
// the state had to be reconstructed from firmware defaults and memory.
//
// A snapshot is the raw sub-messages the radio itself reported, kept
// verbatim. It is not a profile: a profile says what a segment REQUIRES and
// travels between people, while a snapshot says what one particular device
// HELD at one moment and belongs to that device alone. It carries no key —
// channel settings are a separate message and are not in it.
package meshtastic

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// ConfigSnapshot is one radio's configuration as it reported it.
type ConfigSnapshot struct {
	NodeNum  uint32 `json:"node"`
	Firmware string `json:"firmware,omitempty"`
	TakenAt  string `json:"taken_at"`
	// LoRa and Device are the byte-exact sub-messages, hex-encoded so the
	// file is readable and diffable by hand. Absent means the radio did not
	// report that message — which is different from reporting an empty one,
	// and the distinction survives the round trip.
	LoRa   string `json:"lora,omitempty"`
	Device string `json:"device,omitempty"`
	// LoRaEmpty and DeviceEmpty record "reported, and every field was at its
	// default". Without them an all-default DeviceConfig — a CLIENT radio
	// rebroadcasting everything, which is the commonest one there is — would
	// come back from a file as "never reported", and its single setting would
	// become unchangeable.
	LoRaEmpty   bool `json:"lora_empty,omitempty"`
	DeviceEmpty bool `json:"device_empty,omitempty"`
}

// SnapshotOf captures a configuration. at is passed in rather than read from
// the clock so the same capture is reproducible in a test.
func SnapshotOf(cfg NodeConfig, at time.Time) ConfigSnapshot {
	s := ConfigSnapshot{
		NodeNum:  cfg.NodeNum,
		Firmware: cfg.Firmware,
		TakenAt:  at.UTC().Format(time.RFC3339),
	}
	if cfg.LoRaRaw != nil {
		s.LoRa, s.LoRaEmpty = hex.EncodeToString(cfg.LoRaRaw), len(cfg.LoRaRaw) == 0
	}
	if cfg.DeviceRaw != nil {
		s.Device, s.DeviceEmpty = hex.EncodeToString(cfg.DeviceRaw), len(cfg.DeviceRaw) == 0
	}
	return s
}

// Config decodes a snapshot back into the shape the rest of this package
// works with.
func (s ConfigSnapshot) Config() (NodeConfig, error) {
	out := NodeConfig{NodeNum: s.NodeNum, Firmware: s.Firmware}
	if s.LoRa != "" || s.LoRaEmpty {
		raw, err := hex.DecodeString(s.LoRa)
		if err != nil {
			return out, fmt.Errorf("meshtastic: the saved LoRa settings are not "+
				"readable: %w", err)
		}
		out.LoRaRaw = cloneRaw(append([]byte{}, raw...))
		out.LoRa, _ = decodeLoRaConfig(raw)
	}
	if s.Device != "" || s.DeviceEmpty {
		raw, err := hex.DecodeString(s.Device)
		if err != nil {
			return out, fmt.Errorf("meshtastic: the saved device settings are not "+
				"readable: %w", err)
		}
		out.DeviceRaw = cloneRaw(append([]byte{}, raw...))
		out.Device, _ = decodeDeviceConfig(raw)
	}
	return out, nil
}

// SnapshotFile is a set of snapshots keyed by node number, for a person who
// has more than one radio on the bench.
type SnapshotFile struct {
	Radios map[string]ConfigSnapshot `json:"radios"`
}

// Encode renders the file. Keys are sorted by Go's own map marshalling, so
// the bytes are stable and a diff of the file is readable.
func (f SnapshotFile) Encode() ([]byte, error) {
	if f.Radios == nil {
		f.Radios = map[string]ConfigSnapshot{}
	}
	return json.MarshalIndent(f, "", "  ")
}

// DecodeSnapshotFile reads one back. An empty input is an empty file, not an
// error: the first capture on a machine has nothing to read.
func DecodeSnapshotFile(b []byte) (SnapshotFile, error) {
	f := SnapshotFile{Radios: map[string]ConfigSnapshot{}}
	if len(b) == 0 {
		return f, nil
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, err
	}
	if f.Radios == nil {
		f.Radios = map[string]ConfigSnapshot{}
	}
	return f, nil
}

// SnapshotKey names a radio in the file.
func SnapshotKey(nodeNum uint32) string { return fmt.Sprintf("%08x", nodeNum) }

// DiffConfig reports every field where two configurations disagree, across
// both sub-messages, so a drift can be named rather than guessed at.
func DiffConfig(want, got NodeConfig) ([]ConfigMismatch, error) {
	var out []ConfigMismatch
	compare := func(wantRaw, gotRaw []byte, names map[int]string, what string) error {
		switch {
		case wantRaw == nil:
			// Never captured. Not a drift, and claiming one would send
			// somebody looking for a change that did not happen.
			return nil
		case gotRaw == nil:
			// A radio that has stopped reporting something we hold a record
			// of. Silence here would be the worst kind: the diff would say
			// "nothing moved" about a message it never looked at.
			out = append(out, ConfigMismatch{
				Name: what,
				Want: "recorded",
				Got:  "the radio is no longer reporting this message",
			})
			return nil
		}
		d, err := compareConfig(wantRaw, gotRaw, names)
		if err != nil {
			return err
		}
		out = append(out, d...)
		return nil
	}
	if err := compare(want.LoRaRaw, got.LoRaRaw, loraFieldNames, "LoRa settings"); err != nil {
		return nil, err
	}
	if err := compare(want.DeviceRaw, got.DeviceRaw, deviceFieldNames,
		"device settings"); err != nil {
		return nil, err
	}
	return out, nil
}

// PlanRestore builds the writes that put a snapshot back onto a radio.
//
// It returns, separately, the fields it CANNOT restore. Those are fields that
// differ and are not varints — a float such as override_frequency, which this
// build reads no better than it writes. Restoring what can be restored and
// reporting the rest is the honest shape: refusing outright would make the
// recovery tool useless in exactly the situation somebody needs it, and
// staying silent would hand back a radio described as restored when it is
// not.
func PlanRestore(cur NodeConfig, snap ConfigSnapshot) (*ApplyPlan, []string, error) {
	want, err := snap.Config()
	if err != nil {
		return nil, nil, err
	}
	if want.LoRaRaw == nil && want.DeviceRaw == nil {
		return nil, nil, fmt.Errorf("meshtastic: the saved configuration for %s "+
			"holds nothing to restore", SnapshotKey(snap.NodeNum))
	}
	p := &ApplyPlan{Reboot: true}
	var cannot []string

	add := func(field int, curRaw, wantRaw []byte, names map[int]string, what string) error {
		if wantRaw == nil {
			return nil
		}
		if curRaw == nil {
			return fmt.Errorf("meshtastic: this radio is not reporting its %s, so "+
				"there is nothing to write the saved values into", what)
		}
		set, unwritable, err := restoreSet(curRaw, wantRaw, names)
		if err != nil {
			return err
		}
		cannot = append(cannot, unwritable...)
		if len(set) == 0 {
			return nil
		}
		payload, expect, err := planConfigWrite(field, curRaw, set, what)
		if err != nil {
			return err
		}
		p.steps = append(p.steps, applyStep{what: "restore " + what, payload: payload})
		if field == configLoRa {
			p.expectLoRa = expect
		} else {
			p.expectDevice = expect
		}
		p.Summary = append(p.Summary, fmt.Sprintf(
			"Put %d saved %s back as they were at %s.", len(set), what, snap.TakenAt))
		return nil
	}

	if err := add(configLoRa, cur.LoRaRaw, want.LoRaRaw, loraFieldNames,
		"LoRa settings"); err != nil {
		return nil, nil, err
	}
	if err := add(configDevice, cur.DeviceRaw, want.DeviceRaw, deviceFieldNames,
		"device settings"); err != nil {
		return nil, nil, err
	}
	if len(p.steps) == 0 {
		return nil, cannot, nil // already identical; nothing to write
	}
	p.Summary = append(p.Summary,
		"Reboot the radio, then re-read it and check what landed.")
	return p, cannot, nil
}

// restoreSet works out which fields to write to turn curRaw back into wantRaw,
// and which differences this build cannot express.
func restoreSet(curRaw, wantRaw []byte, names map[int]string) (map[int]uint64, []string, error) {
	diff, err := compareConfig(wantRaw, curRaw, names)
	if err != nil {
		return nil, nil, err
	}
	wf, err := fieldsOf(wantRaw)
	if err != nil {
		return nil, nil, err
	}
	cf, err := fieldsOf(curRaw)
	if err != nil {
		return nil, nil, err
	}
	set := map[int]uint64{}
	var cannot []string
	for _, m := range diff {
		w, haveW := wf[m.Field]
		c, haveC := cf[m.Field]
		varintBoth := (!haveW || w.wire == wireVarint) && (!haveC || c.wire == wireVarint)
		if !varintBoth {
			name := m.Name
			if name == "" {
				name = fmt.Sprintf("field %d", m.Field)
			}
			cannot = append(cannot, fmt.Sprintf("%s (saved %s, now %s) — this "+
				"build cannot write this kind of field", name, m.Want, m.Got))
			continue
		}
		if !haveW {
			set[m.Field] = 0 // absent in the snapshot: back to its default
			continue
		}
		r := &reader{b: w.raw}
		v, err := r.varint()
		if err != nil {
			return nil, nil, err
		}
		set[m.Field] = v
	}
	sort.Strings(cannot)
	return set, cannot, nil
}
