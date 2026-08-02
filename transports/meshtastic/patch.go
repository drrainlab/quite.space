// Changing one setting on a radio without erasing the rest.
//
// Meshtastic's admin.set_config carries a whole Config sub-message and
// REPLACES what the device holds with it. There is no field mask: a field the
// client does not send becomes its proto3 default on the device. So "set the
// region", implemented as "encode a LoRaConfig with a region in it", is not a
// partial write at all — it is a full write in which every other setting
// silently reverts.
//
// That is not hypothetical. PlanLoRaApply did exactly this, and because
// tx_enabled defaults to FALSE, every `terminal radio region` run left the
// board unable to transmit. Nine days of radio measurements were taken
// against radios our own tooling had muted, and the last of them had to be
// thrown out.
//
// The obvious repair — decode into LoRaSettings, change a field, encode it
// back — is still lossy, and quietly so. LoRaSettings keeps ten varints;
// decodeLoRaConfig skips everything else by design, naming frequency_offset
// and override_frequency in its own comment. A licensed operator's override
// frequency survives `terminal radio region` today and would have vanished
// the day we "fixed" the bug that way.
//
// So a patch is applied to the BYTES. Fields this build has never heard of
// pass through untouched, in the order the device sent them.
package meshtastic

import (
	"fmt"
	"sort"
)

// patchVarints returns raw with each named field set to the given value,
// preserving every other field — known or unknown — byte for byte.
//
// A field set to zero is REMOVED rather than written: in proto3 an absent
// scalar decodes as its default, so that is the same instruction in fewer
// bytes and is what the firmware itself encodes.
//
// A field whose number we want but whose WIRE TYPE is not a varint stops the
// whole patch. That combination means this build has the field number wrong,
// and there is no safe way to continue: writing to it corrupts a field we
// have misidentified, and skipping it quietly performs a write that does not
// do what it said. The write is refused instead.
func patchVarints(raw []byte, set map[int]uint64) ([]byte, error) {
	pending := make(map[int]uint64, len(set))
	for k, v := range set {
		pending[k] = v
	}
	var out []byte
	r := &reader{b: raw}
	for !r.done() {
		start := r.pos
		tag, err := r.varint()
		if err != nil {
			return nil, fmt.Errorf("meshtastic: unreadable configuration at byte %d: %w",
				start, err)
		}
		field, wt := int(tag>>3), int(tag&7)
		if err := r.skip(wt); err != nil {
			return nil, fmt.Errorf("meshtastic: unreadable configuration field %d: %w",
				field, err)
		}
		v, wanted := set[field]
		if !wanted {
			out = append(out, raw[start:r.pos]...)
			continue
		}
		if wt != wireVarint {
			return nil, fmt.Errorf("meshtastic: the radio holds field %d as a "+
				"different shape than this build expects (wire type %d, not a "+
				"varint), so this setting cannot be changed safely — writing to "+
				"it would corrupt a field we have misidentified", field, wt)
		}
		if _, stillPending := pending[field]; !stillPending {
			// A repeat of a field already patched. Config fields are all
			// singular, and a decoder takes the last occurrence, so dropping
			// the repeat leaves exactly one — and exactly one is what the
			// device will encode when it reports back.
			continue
		}
		delete(pending, field)
		out = appendVarintField(out, field, v)
	}
	// Fields the device never reported. Appended in a stable order so the
	// same patch twice produces the same bytes.
	for _, field := range sortedFields(pending) {
		out = appendVarintField(out, field, pending[field])
	}
	return out, nil
}

// cloneRaw copies a retained sub-message, keeping the difference between "the
// node reported nothing" (nil) and "the node reported a message in which every
// field happens to be at its default" (present, and empty).
//
// That difference decides whether a write is allowed. A DeviceConfig with a
// CLIENT role and ALL rebroadcasting is entirely proto3 defaults, so it
// arrives as zero bytes — and `append([]byte(nil))` on it yields nil, which
// would refuse to change the only setting such a device holds.
func cloneRaw(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append(make([]byte, 0, len(b)), b...)
}

func sortedFields(m map[int]uint64) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// fieldValue is one field as it sat on the wire: its wire type and the raw
// bytes of its value, with the tag stripped.
type fieldValue struct {
	wire int
	raw  []byte
}

// isDefault reports whether this value is the proto3 default for its shape,
// i.e. whether a sender omitting the field entirely would have meant the same
// thing.
func (f fieldValue) isDefault() bool {
	for _, b := range f.raw {
		if b != 0 {
			return false
		}
	}
	return true
}

func (f fieldValue) String() string {
	if f.wire == wireVarint {
		r := &reader{b: f.raw}
		if v, err := r.varint(); err == nil {
			return fmt.Sprintf("%d", v)
		}
	}
	return fmt.Sprintf("%x", f.raw)
}

// fieldsOf decomposes a message into its fields, keeping the LAST occurrence
// of each — which is what a protobuf decoder does.
func fieldsOf(raw []byte) (map[int]fieldValue, error) {
	out := map[int]fieldValue{}
	r := &reader{b: raw}
	for !r.done() {
		tag, err := r.varint()
		if err != nil {
			return nil, err
		}
		field, wt := int(tag>>3), int(tag&7)
		begin := r.pos
		if err := r.skip(wt); err != nil {
			return nil, err
		}
		out[field] = fieldValue{wire: wt, raw: raw[begin:r.pos]}
	}
	return out, nil
}

// ConfigMismatch is one field where a radio does not hold what was intended.
type ConfigMismatch struct {
	// Field is the protobuf field number, and Name what this build calls it
	// — or empty, when the disagreement is about a field we cannot name. An
	// unnameable mismatch still matters: it means the write disturbed
	// something we do not understand, which is worse than one we do.
	Field int
	Name  string
	Want  string
	Got   string
}

func (m ConfigMismatch) String() string {
	name := m.Name
	if name == "" {
		name = fmt.Sprintf("field %d", m.Field)
	}
	return fmt.Sprintf("%s: asked for %s, radio holds %s", name, m.Want, m.Got)
}

// compareConfig reports every field where two encodings of a configuration
// disagree.
//
// It compares FIELDS, not bytes. The device re-encodes what it stores, in its
// own order and omitting its own defaults, so two byte strings can carry
// identical configuration and share almost no bytes. A field absent on one
// side and at its proto3 default on the other is agreement — that is what
// "absent means default" means, and calling it a difference would make every
// verification fail on the first zero.
func compareConfig(want, got []byte, names map[int]string) ([]ConfigMismatch, error) {
	wf, err := fieldsOf(want)
	if err != nil {
		return nil, fmt.Errorf("meshtastic: cannot read the intended configuration: %w", err)
	}
	gf, err := fieldsOf(got)
	if err != nil {
		return nil, fmt.Errorf("meshtastic: cannot read the configuration the radio "+
			"came back with: %w", err)
	}
	seen := map[int]bool{}
	var fields []int
	for f := range wf {
		fields, seen[f] = append(fields, f), true
	}
	for f := range gf {
		if !seen[f] {
			fields = append(fields, f)
		}
	}
	sort.Ints(fields)

	var out []ConfigMismatch
	for _, f := range fields {
		w, haveW := wf[f]
		g, haveG := gf[f]
		switch {
		case haveW && haveG:
			if w.wire == g.wire && string(w.raw) == string(g.raw) {
				continue
			}
			// Two encodings of the same varint (a redundant continuation
			// byte, say) are the same value. Compare what it means, not how
			// it was written.
			if w.wire == wireVarint && g.wire == wireVarint && w.String() == g.String() {
				continue
			}
			out = append(out, ConfigMismatch{f, names[f], w.String(), g.String()})
		case haveW:
			if w.isDefault() {
				continue // absent on the radio == the default we asked for
			}
			out = append(out, ConfigMismatch{f, names[f], w.String(), "not set"})
		default:
			if g.isDefault() {
				continue
			}
			out = append(out, ConfigMismatch{f, names[f], "not set", g.String()})
		}
	}
	return out, nil
}
