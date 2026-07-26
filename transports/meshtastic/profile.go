// Radio profiles (RB-2, beta blocker). A segment only works when every node
// on it agrees about region, modem preset and channel key. Disagree about
// any one of them and the radios are not broken and not out of range — they
// are simply on different air, and the symptom is silence, which is exactly
// what a healthy radio with nobody nearby also looks like.
//
// A Profile is the expected settings for one segment. Check() compares a
// node against it and returns, per field, one of three answers:
//
//	ok        — the node reported this and it agrees
//	mismatch  — the node reported this and it differs; here is the fix
//	unknown   — the node did not report this, so nothing is claimed
//
// The third answer is the one that keeps this file honest. A node on older
// firmware reports no LoRaConfig, and describing its region as "UNSET" would
// send its operator chasing a fault that does not exist. Unverified and
// wrong are different answers and are never merged.
//
// The key is never in a profile. Profiles carry a fingerprint — a truncated
// hash — which is enough to tell "the same key as the reference node" from
// "a different key", and useless for learning the key. That is what lets a
// profile be a plain file that travels with the beta package.
package meshtastic

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// CheckStatus is the verdict for one field.
type CheckStatus uint8

const (
	CheckOK CheckStatus = iota
	CheckMismatch
	CheckUnknown
)

func (s CheckStatus) String() string {
	switch s {
	case CheckOK:
		return "ok"
	case CheckMismatch:
		return "mismatch"
	}
	return "unknown"
}

// Check is one field's verdict, with everything a person needs to act.
type Check struct {
	Field  string
	Status CheckStatus
	Want   string
	Got    string
	Why    string // why it matters, or why we could not tell
	Fix    string // the exact thing to type
}

// Verdict summarises a set of checks.
type Verdict []Check

// Failed reports whether anything actually disagreed.
func (v Verdict) Failed() bool {
	for _, c := range v {
		if c.Status == CheckMismatch {
			return true
		}
	}
	return false
}

// Incomplete reports whether anything could not be verified. Kept separate
// from Failed on purpose: "this radio is wrong" and "this radio did not tell
// me" call for different actions from the person holding it.
func (v Verdict) Incomplete() bool {
	for _, c := range v {
		if c.Status == CheckUnknown {
			return true
		}
	}
	return false
}

// Report renders the verdict for a person.
func (v Verdict) Report() string {
	var b strings.Builder
	for _, c := range v {
		switch c.Status {
		case CheckOK:
			fmt.Fprintf(&b, "  ok       %-18s %s\n", c.Field, c.Got)
		case CheckMismatch:
			fmt.Fprintf(&b, "  WRONG    %-18s is %s, segment needs %s\n",
				c.Field, c.Got, c.Want)
			if c.Why != "" {
				fmt.Fprintf(&b, "           %s\n", c.Why)
			}
			fmt.Fprintf(&b, "           fix: %s\n", c.Fix)
		case CheckUnknown:
			fmt.Fprintf(&b, "  ?        %-18s %s\n", c.Field, c.Why)
		}
	}
	return b.String()
}

// KeyRequirement is what a profile demands of the channel key.
type KeyRequirement struct {
	Class KeyClass
	// Fingerprint, when set, demands THIS key rather than merely a private
	// one. Empty means "any key of the required class".
	Fingerprint string
}

// Profile is the expected configuration of one segment. A zero field means
// "not checked" — a profile states what the segment requires and stays
// silent about the rest, so a partial profile is a legitimate one.
type Profile struct {
	Name         string
	Region       *uint32
	ModemPreset  *uint32
	HopLimit     *uint32
	TxEnabled    *bool
	ChannelIndex int
	ChannelName  string
	Key          *KeyRequirement
	// channelSpecified records whether the file named a channel at all, so
	// index 0 (the primary, and the zero value) is distinguishable from
	// "channels not part of this profile".
	channelSpecified bool
}

// ---- checking ----

const unreportedWhy = "the node did not report this — nothing is claimed " +
	"about it. Older firmware may not send it; compare by hand with " +
	"`meshtastic --info`."

// Check compares a node against the profile.
func (p Profile) Check(cfg NodeConfig) Verdict {
	var out Verdict
	if p.Region != nil {
		out = append(out, p.checkLoRa(cfg, "lora.region",
			enumName(regionNames, *p.Region),
			func(l *LoRaSettings) string { return l.RegionName() },
			"radios on different regions transmit on different frequencies "+
				"and never hear each other",
			"meshtastic --set lora.region "+enumName(regionNames, *p.Region)))
	}
	if p.ModemPreset != nil {
		out = append(out, p.checkLoRa(cfg, "lora.modem_preset",
			enumName(presetNames, *p.ModemPreset),
			func(l *LoRaSettings) string {
				if !l.UsePreset {
					return "off (manual bandwidth/sf/cr)"
				}
				return l.PresetName()
			},
			"the preset sets spreading factor and bandwidth; nodes on "+
				"different presets are on different air",
			"meshtastic --set lora.modem_preset "+enumName(presetNames, *p.ModemPreset)+
				" --set lora.use_preset true"))
	}
	if p.HopLimit != nil {
		want := strconv.FormatUint(uint64(*p.HopLimit), 10)
		out = append(out, p.checkLoRa(cfg, "lora.hop_limit", want,
			func(l *LoRaSettings) string {
				return strconv.FormatUint(uint64(l.HopLimit), 10)
			},
			"too few hops and a relayed node is unreachable; too many and "+
				"the segment wastes airtime repeating itself",
			"meshtastic --set lora.hop_limit "+want))
	}
	if p.TxEnabled != nil {
		want := strconv.FormatBool(*p.TxEnabled)
		out = append(out, p.checkLoRa(cfg, "lora.tx_enabled", want,
			func(l *LoRaSettings) string { return strconv.FormatBool(l.TxEnabled) },
			"a node with the transmitter off receives normally and answers "+
				"nothing, which reads exactly like being out of range",
			"meshtastic --set lora.tx_enabled "+want))
	}
	if p.channelSpecified {
		out = append(out, p.checkChannel(cfg)...)
	}
	return out
}

// checkLoRa applies one LoRa field, treating an unreported config as unknown.
func (p Profile) checkLoRa(cfg NodeConfig, field, want string,
	got func(*LoRaSettings) string, why, fix string) Check {

	if cfg.LoRa == nil {
		return Check{Field: field, Status: CheckUnknown, Want: want,
			Why: unreportedWhy}
	}
	g := got(cfg.LoRa)
	if g == want {
		return Check{Field: field, Status: CheckOK, Want: want, Got: g}
	}
	return Check{Field: field, Status: CheckMismatch, Want: want, Got: g,
		Why: why, Fix: fix}
}

func (p Profile) checkChannel(cfg NodeConfig) []Check {
	var out []Check
	ch, reported := cfg.Channel(p.ChannelIndex)
	idx := strconv.Itoa(p.ChannelIndex)

	if p.ChannelName != "" {
		switch {
		case !reported:
			out = append(out, Check{Field: "channel.name", Status: CheckUnknown,
				Want: p.ChannelName,
				Why: "the node did not report channel " + idx +
					" — nothing is claimed about it"})
		case ch.Name == p.ChannelName:
			out = append(out, Check{Field: "channel.name", Status: CheckOK,
				Want: p.ChannelName, Got: ch.Name})
		default:
			out = append(out, Check{Field: "channel.name", Status: CheckMismatch,
				Want: p.ChannelName, Got: ch.Name,
				Why: "the channel name is part of what nodes hash into the " +
					"channel identity; a different name is a different channel",
				Fix: "meshtastic --ch-index " + idx + " --ch-set name " + p.ChannelName})
		}
	}

	if p.Key != nil {
		out = append(out, p.checkKey(ch, reported, idx))
	}
	return out
}

func (p Profile) checkKey(ch ChannelInfo, reported bool, idx string) Check {
	want := p.Key.Class.String()
	if p.Key.Fingerprint != "" {
		want = fmt.Sprintf("%s [%s]", p.Key.Class, p.Key.Fingerprint)
	}
	// The fix never carries a key. It says where the key comes from — the
	// beta package the operator already handed over — because printing it
	// here would put it in a terminal, a scrollback and a shell history.
	fix := "meshtastic --ch-index " + idx +
		" --ch-set psk base64:<the channel key from the beta package> " +
		"(or --seturl with the channel link, which carries the key for you)"

	if !reported || ch.KeyClass == KeyUnknown {
		return Check{Field: "channel.key", Status: CheckUnknown, Want: want,
			Why: "the node did not report channel " + idx +
				" — nothing is claimed about its key"}
	}
	got := ch.KeyClass.String()
	if ch.KeyFingerprint != "" {
		got = fmt.Sprintf("%s [%s]", ch.KeyClass, ch.KeyFingerprint)
	}
	if ch.KeyClass != p.Key.Class {
		why := "the channel key is wrong"
		if ch.KeyClass == KeyDefault {
			why = "this channel uses the built-in Meshtastic key, which " +
				"anyone with a Meshtastic device already has: the segment " +
				"has no radio-layer privacy at all"
		}
		if ch.KeyClass == KeyNone {
			why = "this channel is unencrypted at the radio layer, so " +
				"anyone in range sees every packet on it"
		}
		return Check{Field: "channel.key", Status: CheckMismatch,
			Want: want, Got: got, Why: why, Fix: fix}
	}
	if p.Key.Fingerprint != "" && p.Key.Fingerprint != ch.KeyFingerprint {
		return Check{Field: "channel.key", Status: CheckMismatch,
			Want: want, Got: got,
			Why: "both nodes have a private key and the keys differ. Each " +
				"transmits fine and neither can read the other, which looks " +
				"exactly like being out of range",
			Fix: fix}
	}
	return Check{Field: "channel.key", Status: CheckOK, Want: want, Got: got}
}

// ---- capture ----

// ProfileFrom captures a profile from a node that is already configured
// correctly. This is the intended workflow: set one radio up by hand, take
// its profile, then check every other radio against it. The key stays on the
// radios; only its fingerprint travels.
func ProfileFrom(name string, cfg NodeConfig) (Profile, error) {
	if cfg.LoRa == nil {
		return Profile{}, fmt.Errorf("meshtastic: this node reported no LoRa " +
			"configuration, so there is nothing to capture. A profile taken " +
			"from silence would verify nothing.")
	}
	l := cfg.LoRa
	region, preset, hop, tx := l.Region, l.ModemPreset, l.HopLimit, l.TxEnabled
	p := Profile{
		Name:      name,
		Region:    &region,
		HopLimit:  &hop,
		TxEnabled: &tx,
	}
	if l.UsePreset {
		p.ModemPreset = &preset
	}
	if ch, ok := cfg.PrimaryChannel(); ok {
		p.channelSpecified = true
		p.ChannelIndex = ch.Index
		p.ChannelName = ch.Name
		p.Key = &KeyRequirement{Class: ch.KeyClass, Fingerprint: ch.KeyFingerprint}
	}
	return p, nil
}

// Format renders a profile as the file ParseProfile reads.
func (p Profile) Format() string {
	var b strings.Builder
	b.WriteString("# Quiet Spaces radio profile — the settings every node on\n" +
		"# this segment must share. Contains no keys: `channel_key` is a\n" +
		"# fingerprint, which identifies a key without revealing it.\n")
	if p.Name != "" {
		fmt.Fprintf(&b, "name          = %s\n", p.Name)
	}
	if p.Region != nil {
		fmt.Fprintf(&b, "region        = %s\n", enumName(regionNames, *p.Region))
	}
	if p.ModemPreset != nil {
		fmt.Fprintf(&b, "modem_preset  = %s\n", enumName(presetNames, *p.ModemPreset))
	}
	if p.HopLimit != nil {
		fmt.Fprintf(&b, "hop_limit     = %d\n", *p.HopLimit)
	}
	if p.TxEnabled != nil {
		fmt.Fprintf(&b, "tx_enabled    = %t\n", *p.TxEnabled)
	}
	if p.channelSpecified {
		fmt.Fprintf(&b, "channel_index = %d\n", p.ChannelIndex)
		if p.ChannelName != "" {
			fmt.Fprintf(&b, "channel_name  = %s\n", p.ChannelName)
		}
		if p.Key != nil {
			fmt.Fprintf(&b, "channel_key   = %s\n", formatKeyRequirement(*p.Key))
		}
	}
	return b.String()
}

func formatKeyRequirement(k KeyRequirement) string {
	name := keyClassWord(k.Class)
	if k.Fingerprint != "" {
		return name + ":" + k.Fingerprint
	}
	return name
}

func keyClassWord(k KeyClass) string {
	switch k {
	case KeyNone:
		return "none"
	case KeyDefault:
		return "default"
	case KeyCustom:
		return "private"
	}
	return "unknown"
}

// ---- parsing ----

// ParseProfile reads a profile file. Anything it cannot resolve is an error:
// a profile that silently checks nothing would report a clean pass on a
// radio nobody verified, which is worse than having no profile at all.
func ParseProfile(data []byte) (Profile, error) {
	var p Profile
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return p, fmt.Errorf("line %d: expected `key = value`, got %q", n+1, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if err := p.set(key, value); err != nil {
			return p, fmt.Errorf("line %d: %w", n+1, err)
		}
	}
	return p, nil
}

func (p *Profile) set(key, value string) error {
	switch key {
	case "name":
		p.Name = value
	case "region":
		v, err := resolveEnum(regionNames, value, "region")
		if err != nil {
			return err
		}
		p.Region = &v
	case "modem_preset":
		v, err := resolveEnum(presetNames, value, "modem preset")
		if err != nil {
			return err
		}
		p.ModemPreset = &v
	case "hop_limit":
		v, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return fmt.Errorf("hop_limit %q is not a number", value)
		}
		u := uint32(v)
		p.HopLimit = &u
	case "tx_enabled":
		v, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("tx_enabled %q is not true or false", value)
		}
		p.TxEnabled = &v
	case "channel_index":
		v, err := strconv.Atoi(value)
		if err != nil || v < 0 {
			return fmt.Errorf("channel_index %q is not a channel number", value)
		}
		p.ChannelIndex = v
		p.channelSpecified = true
	case "channel_name":
		p.ChannelName = value
		p.channelSpecified = true
	case "channel_key":
		req, err := parseKeyRequirement(value)
		if err != nil {
			return err
		}
		p.Key = &req
		p.channelSpecified = true
	default:
		return fmt.Errorf("unknown profile key %q", key)
	}
	return nil
}

func parseKeyRequirement(value string) (KeyRequirement, error) {
	word, fp, _ := strings.Cut(value, ":")
	var class KeyClass
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "none":
		class = KeyNone
	case "default":
		class = KeyDefault
	case "private":
		class = KeyCustom
	default:
		return KeyRequirement{}, fmt.Errorf(
			"channel_key %q: expected none, default or private "+
				"(optionally `private:<fingerprint>`)", value)
	}
	fp = strings.TrimSpace(fp)
	if fp != "" && class != KeyCustom {
		return KeyRequirement{}, fmt.Errorf(
			"channel_key %q: only a private key has a fingerprint", value)
	}
	return KeyRequirement{Class: class, Fingerprint: fp}, nil
}

// resolveEnum accepts a name this build knows, or a bare number. The number
// is the escape hatch for firmware that has grown a value we have not: the
// person is asserting it deliberately, which is different from us guessing.
func resolveEnum(table map[uint32]string, value, what string) (uint32, error) {
	if v, ok := enumValue(table, value); ok {
		return v, nil
	}
	if n, err := strconv.ParseUint(value, 10, 32); err == nil {
		return uint32(n), nil
	}
	return 0, fmt.Errorf("%s %q is not one of %s (a bare number is also "+
		"accepted if your firmware is newer than this build)",
		what, value, strings.Join(sortedNames(table), ", "))
}

func sortedNames(table map[uint32]string) []string {
	keys := make([]uint32, 0, len(table))
	for k := range table {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, table[k])
	}
	return out
}
