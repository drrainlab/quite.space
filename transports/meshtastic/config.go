// Node configuration (RB-2). A Meshtastic node streams its entire
// configuration to a client immediately after `want_config_id` — LoRa
// settings, every channel, device metadata — and until now this client threw
// all of it away and kept only the node number.
//
// Keeping it costs no extra airtime and no extra round trip, and it is the
// difference between "the radios are silent" and "this one is on EU_433
// while the other is on EU_868". Two nodes whose region, modem preset or
// channel key differ never hear each other, and nothing about that failure
// is visible from inside Quiet Spaces: there is no error, no timeout, no
// rejected packet. Just silence, which is exactly what a working radio with
// nobody in range also looks like.
//
// Three rules hold this file honest:
//
//   - What the node did not report is UNKNOWN, never a default. A node on
//     older firmware that sends no LoRaConfig must not be described as
//     "region UNSET" — that is a claim about someone's hardware we have no
//     evidence for.
//   - An enum value this build does not know is rendered as its number.
//     Mapping 200 onto the nearest name we happen to have would be a
//     confident lie.
//   - The channel key is hashed where it is decoded and the plaintext is
//     dropped. It is the one field on a radio that must never travel back
//     out through a screen, a log or a diagnostics export.
//
// Field numbers are transcribed from the official meshtastic/protobufs
// (config.proto, channel.proto, mesh.proto) and are wire-stable:
//
//	FromRadio:       config=5, channel=10, metadata=13
//	Config:          device=1, lora=6
//	LoRaConfig:      use_preset=1, modem_preset=2, bandwidth=3,
//	                 spread_factor=4, coding_rate=5, region=7, hop_limit=8,
//	                 tx_enabled=9, tx_power=10, channel_num=11
//	DeviceConfig:    role=1, rebroadcast_mode=6
//	Channel:         index=1, settings=2, role=3
//	ChannelSettings: psk=2, name=3
//	DeviceMetadata:  firmware_version=1
//
// Verify against real hardware with `quiet-radio --raw`, which prints every
// field number the node actually sends. Anything we misread shows up there
// as an unrecognised field rather than as a wrong answer.
package meshtastic

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Region codes (config.proto, LoRaConfig.RegionCode).
const (
	regionUnset  uint32 = 0
	regionEU433  uint32 = 2
	regionEU868  uint32 = 3
	regionUS     uint32 = 1
	regionRU     uint32 = 9
	regionUA868  uint32 = 15
	regionLora24 uint32 = 13
)

var regionNames = map[uint32]string{
	0: "UNSET", 1: "US", 2: "EU_433", 3: "EU_868", 4: "CN", 5: "JP",
	6: "ANZ", 7: "KR", 8: "TW", 9: "RU", 10: "IN", 11: "NZ_865",
	12: "TH", 13: "LORA_24", 14: "UA_433", 15: "UA_868", 16: "MY_433",
	17: "MY_919", 18: "SG_923", 19: "PH_433", 20: "PH_868", 21: "PH_915",
	22: "ANZ_433",
}

// Modem presets (config.proto, LoRaConfig.ModemPreset).
const (
	presetLongFast uint32 = 0
	presetLongSlow uint32 = 1
)

var presetNames = map[uint32]string{
	0: "LONG_FAST", 1: "LONG_SLOW", 2: "VERY_LONG_SLOW", 3: "MEDIUM_SLOW",
	4: "MEDIUM_FAST", 5: "SHORT_SLOW", 6: "SHORT_FAST", 7: "LONG_MODERATE",
	8: "SHORT_TURBO",
}

// enumName renders a known value by name and an unknown one by number.
func enumName(table map[uint32]string, v uint32) string {
	if s, ok := table[v]; ok {
		return s
	}
	return fmt.Sprintf("UNKNOWN(%d)", v)
}

// enumValue resolves a name back to its number, for reading a profile.
func enumValue(table map[uint32]string, name string) (uint32, bool) {
	for v, s := range table {
		if strings.EqualFold(s, name) {
			return v, true
		}
	}
	return 0, false
}

// LoRa field numbers, named where they are used rather than transcribed
// again at every call site. patch.go writes by number; this table is what
// lets a mismatch report say which setting disagreed.
const (
	loraUsePreset    = 1
	loraModemPreset  = 2
	loraBandwidth    = 3
	loraSpreadFactor = 4
	loraCodingRate   = 5
	loraRegion       = 7
	loraHopLimit     = 8
	loraTxEnabled    = 9
	loraTxPower      = 10
	loraChannelNum   = 11
)

var loraFieldNames = map[int]string{
	loraUsePreset: "use_preset", loraModemPreset: "modem_preset",
	loraBandwidth: "bandwidth", loraSpreadFactor: "spread_factor",
	loraCodingRate: "coding_rate", 6: "frequency_offset", loraRegion: "region",
	loraHopLimit: "hop_limit", loraTxEnabled: "tx_enabled",
	loraTxPower: "tx_power", loraChannelNum: "channel_num",
	12: "sx126x_rx_boosted_gain", 13: "override_duty_cycle",
	14: "override_frequency", 15: "pa_fan_disabled", 103: "ignore_incoming",
	104: "ignore_mqtt", 105: "config_ok_to_mqtt",
}

// Device field numbers (config.proto, DeviceConfig).
const (
	deviceRole        = 1
	deviceRebroadcast = 6
)

var deviceFieldNames = map[int]string{
	deviceRole: "role", 2: "serial_enabled", 4: "button_gpio", 5: "buzzer_gpio",
	deviceRebroadcast: "rebroadcast_mode", 7: "node_info_broadcast_secs",
	8: "double_tap_as_button_press", 9: "is_managed", 10: "disable_triple_click",
	11: "tzdef", 12: "led_heartbeat_disabled",
}

// LoRaSettings is what a node reports about its radio. A nil *LoRaSettings
// means the node reported nothing — not that everything is zero.
//
// Within the message, proto3 omits fields at their default value, so an
// absent scalar genuinely IS the default and reading it as such is correct.
// The distinction that matters is one level up: message present or absent.
//
// This struct is a READING of the configuration, never the thing that gets
// written back. It keeps ten varints and decodeLoRaConfig deliberately skips
// everything else, so re-encoding it would erase whatever a device holds that
// this build has not heard of. Writes go through the raw bytes — see
// NodeConfig.LoRaRaw and patch.go.
type LoRaSettings struct {
	UsePreset    bool
	ModemPreset  uint32
	Region       uint32
	HopLimit     uint32
	TxEnabled    bool
	Bandwidth    uint32
	SpreadFactor uint32
	CodingRate   uint32
	ChannelNum   uint32
	TxPower      int32
}

// RegionName renders the region, by name when we know it.
func (l LoRaSettings) RegionName() string { return enumName(regionNames, l.Region) }

// PresetName renders the modem preset, by name when we know it.
func (l LoRaSettings) PresetName() string { return enumName(presetNames, l.ModemPreset) }

// DeviceSettings is the part of DeviceConfig this build reads.
//
// It exists because `--quiet-neighbours` WRITES rebroadcast_mode and, until
// now, nothing could read it: decodeConfigLoRa returned only for Config.lora
// and dropped every other Config variant on the floor. A write nobody can
// read back is a write nobody can verify, and the measurement that rested on
// it was worthless.
type DeviceSettings struct {
	Role        uint32
	Rebroadcast uint32
}

var roleNames = map[uint32]string{
	0: "CLIENT", 1: "CLIENT_MUTE", 2: "ROUTER", 3: "ROUTER_CLIENT",
	4: "REPEATER", 5: "TRACKER", 6: "SENSOR", 7: "TAK", 8: "CLIENT_HIDDEN",
	9: "LOST_AND_FOUND", 10: "TAK_TRACKER", 11: "ROUTER_LATE",
}

var rebroadcastNames = map[uint32]string{
	0: "ALL", 1: "ALL_SKIP_DECODING", 2: "LOCAL_ONLY", 3: "KNOWN_ONLY",
	4: "NONE", 5: "CORE_PORTNUMS_ONLY",
}

// RoleName renders the device role, by name when we know it.
func (d DeviceSettings) RoleName() string { return enumName(roleNames, d.Role) }

// RebroadcastName renders the rebroadcast mode, by name when we know it.
func (d DeviceSettings) RebroadcastName() string {
	return enumName(rebroadcastNames, d.Rebroadcast)
}

// ChannelRole is Channel.Role.
type ChannelRole uint32

const (
	ChannelDisabled  ChannelRole = 0
	ChannelPrimary   ChannelRole = 1
	ChannelSecondary ChannelRole = 2
)

func (r ChannelRole) String() string {
	switch r {
	case ChannelPrimary:
		return "PRIMARY"
	case ChannelSecondary:
		return "SECONDARY"
	case ChannelDisabled:
		return "DISABLED"
	}
	return fmt.Sprintf("UNKNOWN(%d)", uint32(r))
}

// KeyClass says what KIND of key a channel uses without holding the key.
//
// Meshtastic overloads the psk field: empty means no encryption, a single
// byte selects one of the well-known built-in keys, and 16 or 32 bytes is a
// real key. The first two are public knowledge, and a beta segment resting
// on them is not private in any sense the threat model recognises — so the
// class is reported separately from the fingerprint, loudly.
type KeyClass uint8

const (
	KeyNone    KeyClass = iota // no encryption at all
	KeyDefault                 // the built-in key everyone has
	KeyCustom                  // a real key, private if it was kept private
	KeyUnknown                 // no channel settings reported
)

func (k KeyClass) String() string {
	switch k {
	case KeyNone:
		return "none"
	case KeyDefault:
		return "default (public — anyone with Meshtastic can read this channel)"
	case KeyCustom:
		return "private"
	}
	return "unknown"
}

// ChannelInfo is one configured channel. The key itself is never here.
type ChannelInfo struct {
	Index    int
	Name     string
	Role     ChannelRole
	KeyClass KeyClass
	// KeyFingerprint is a truncated SHA-256 of the key: enough to tell two
	// nodes apart, useless for reconstructing the key. Empty when there is
	// no key to fingerprint.
	KeyFingerprint string
}

// NodeConfig is everything the node told us about itself.
type NodeConfig struct {
	NodeNum  uint32
	Firmware string
	// LoRa is nil when the node reported no LoRaConfig.
	LoRa *LoRaSettings
	// LoRaRaw is the byte-exact LoRaConfig sub-message the node last sent,
	// including every field this build cannot read.
	//
	// It is kept because changing one setting means REPLACING this whole
	// message on the device, and anything not carried over is erased —
	// tx_enabled, a licensed operator's override_frequency, a field a newer
	// firmware added last month. A change is applied to these bytes rather
	// than to the decoded struct above. See patch.go.
	LoRaRaw []byte
	// Device and DeviceRaw are the same two things for DeviceConfig, which
	// carries the rebroadcast mode.
	Device    *DeviceSettings
	DeviceRaw []byte
	Channels  []ChannelInfo
	// Unrecognised counts top-level FromRadio fields this build skipped,
	// keyed by field number. It is what `--raw` prints, and what makes a
	// transcription error in this file visible against real hardware.
	Unrecognised map[int]int
}

// Channel returns the configured channel at index.
func (c NodeConfig) Channel(index int) (ChannelInfo, bool) {
	for _, ch := range c.Channels {
		if ch.Index == index {
			return ch, true
		}
	}
	return ChannelInfo{}, false
}

// PrimaryChannel returns the channel in the PRIMARY role.
func (c NodeConfig) PrimaryChannel() (ChannelInfo, bool) {
	for _, ch := range c.Channels {
		if ch.Role == ChannelPrimary {
			return ch, true
		}
	}
	return ChannelInfo{}, false
}

// Report renders the configuration for a person. Never includes a key.
func (c NodeConfig) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "node        %08x\n", c.NodeNum)
	if c.Firmware != "" {
		fmt.Fprintf(&b, "firmware    %s\n", c.Firmware)
	}
	if c.LoRa == nil {
		b.WriteString("lora        not reported by this node\n")
	} else {
		l := c.LoRa
		fmt.Fprintf(&b, "region      %s\n", l.RegionName())
		if l.UsePreset {
			fmt.Fprintf(&b, "preset      %s\n", l.PresetName())
		} else {
			fmt.Fprintf(&b, "preset      off (bandwidth %d, sf %d, cr %d)\n",
				l.Bandwidth, l.SpreadFactor, l.CodingRate)
		}
		fmt.Fprintf(&b, "hop limit   %d\n", l.HopLimit)
		if l.ChannelNum == 0 {
			b.WriteString("slot        from the primary channel's name\n")
		} else {
			fmt.Fprintf(&b, "slot        %d\n", l.ChannelNum)
		}
		fmt.Fprintf(&b, "tx          %s\n", enabledWord(l.TxEnabled))
	}
	if c.Device != nil {
		fmt.Fprintf(&b, "role        %s\n", c.Device.RoleName())
		fmt.Fprintf(&b, "rebroadcast %s\n", c.Device.RebroadcastName())
	}
	if len(c.Channels) == 0 {
		b.WriteString("channels    not reported by this node\n")
	}
	for _, ch := range c.Channels {
		name := ch.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(&b, "channel %d   %-16s %-9s key %s",
			ch.Index, name, ch.Role, ch.KeyClass)
		if ch.KeyFingerprint != "" {
			fmt.Fprintf(&b, " [%s]", ch.KeyFingerprint)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func enabledWord(v bool) string {
	if v {
		return "enabled"
	}
	return "DISABLED"
}

// keyFingerprint classifies a channel key and hashes it. The plaintext does
// not survive this function.
func keyFingerprint(psk []byte) (KeyClass, string) {
	switch {
	case len(psk) == 0:
		return KeyNone, ""
	case len(psk) == 1 && psk[0] == 0:
		return KeyNone, ""
	case len(psk) == 1:
		// A one-byte psk selects a built-in key by index. Every Meshtastic
		// install has them, so this channel is readable by anyone.
		return KeyDefault, fmt.Sprintf("builtin-%d", psk[0])
	default:
		sum := sha256.Sum256(psk)
		return KeyCustom, hex.EncodeToString(sum[:4])
	}
}

// ---- decoding ----

// absorbConfig folds one FromRadio frame's configuration content into c.
// Returns true when the frame changed something.
func (c *NodeConfig) absorbConfig(field int, raw []byte) bool {
	switch field {
	case 5: // Config
		which, sub, ok := decodeConfigVariant(raw)
		if !ok {
			return false
		}
		switch which {
		case configLoRa:
			lora, ok := decodeLoRaConfig(sub)
			if !ok {
				return false
			}
			c.LoRa, c.LoRaRaw = lora, cloneRaw(sub)
			return true
		case configDevice:
			dev, ok := decodeDeviceConfig(sub)
			if !ok {
				return false
			}
			c.Device, c.DeviceRaw = dev, cloneRaw(sub)
			return true
		}
		return false
	case 10: // Channel
		ch, ok := decodeChannel(raw)
		if !ok {
			return false
		}
		for i, old := range c.Channels {
			if old.Index == ch.Index {
				c.Channels[i] = ch
				return true
			}
		}
		c.Channels = append(c.Channels, ch)
		sort.Slice(c.Channels, func(i, j int) bool {
			return c.Channels[i].Index < c.Channels[j].Index
		})
		return true
	case 13: // DeviceMetadata
		if fw, ok := decodeMetadataFirmware(raw); ok {
			c.Firmware = fw
			return true
		}
	}
	return false
}

// Config one-of members this build reads (config.proto).
const (
	configDevice = 1
	configLoRa   = 6
)

// decodeConfigVariant reads Config and reports WHICH variant it carried plus
// that variant's byte-exact sub-message.
//
// A Config describing something else (display, bluetooth, position) is not
// evidence about the radio and returns nothing. Until this function existed
// the same was true of DeviceConfig, so the rebroadcast mode `radio region
// --quiet-neighbours` writes could never be read back.
func decodeConfigVariant(b []byte) (int, []byte, bool) {
	r := &reader{b: b}
	for !r.done() {
		tag, err := r.varint()
		if err != nil {
			return 0, nil, false
		}
		field, wt := int(tag>>3), int(tag&7)
		if wt == wireBytes && (field == configLoRa || field == configDevice) {
			raw, err := r.bytes()
			if err != nil {
				return 0, nil, false
			}
			return field, raw, true
		}
		if err := r.skip(wt); err != nil {
			return 0, nil, false
		}
	}
	return 0, nil, false
}

func decodeDeviceConfig(b []byte) (*DeviceSettings, bool) {
	d := &DeviceSettings{}
	r := &reader{b: b}
	for !r.done() {
		tag, err := r.varint()
		if err != nil {
			return nil, false
		}
		field, wt := int(tag>>3), int(tag&7)
		if wt != wireVarint {
			if err := r.skip(wt); err != nil {
				return nil, false
			}
			continue
		}
		v, err := r.varint()
		if err != nil {
			return nil, false
		}
		switch field {
		case deviceRole:
			d.Role = uint32(v)
		case deviceRebroadcast:
			d.Rebroadcast = uint32(v)
		}
	}
	return d, true
}

func decodeLoRaConfig(b []byte) (*LoRaSettings, bool) {
	l := &LoRaSettings{}
	r := &reader{b: b}
	for !r.done() {
		tag, err := r.varint()
		if err != nil {
			return nil, false
		}
		field, wt := int(tag>>3), int(tag&7)
		if wt != wireVarint {
			// Floats (frequency_offset, override_frequency) and anything
			// else we do not read: skipped, never guessed at.
			if err := r.skip(wt); err != nil {
				return nil, false
			}
			continue
		}
		v, err := r.varint()
		if err != nil {
			return nil, false
		}
		switch field {
		case loraUsePreset:
			l.UsePreset = v != 0
		case loraModemPreset:
			l.ModemPreset = uint32(v)
		case loraBandwidth:
			l.Bandwidth = uint32(v)
		case loraSpreadFactor:
			l.SpreadFactor = uint32(v)
		case loraCodingRate:
			l.CodingRate = uint32(v)
		case loraRegion:
			l.Region = uint32(v)
		case loraHopLimit:
			l.HopLimit = uint32(v)
		case loraTxEnabled:
			l.TxEnabled = v != 0
		case loraTxPower:
			l.TxPower = int32(v)
		case loraChannelNum:
			l.ChannelNum = uint32(v)
		}
	}
	return l, true
}

func decodeChannel(b []byte) (ChannelInfo, bool) {
	ch := ChannelInfo{KeyClass: KeyUnknown}
	seen := false
	r := &reader{b: b}
	for !r.done() {
		tag, err := r.varint()
		if err != nil {
			return ch, false
		}
		field, wt := int(tag>>3), int(tag&7)
		switch {
		case field == 1 && wt == wireVarint:
			v, err := r.varint()
			if err != nil {
				return ch, false
			}
			ch.Index = int(v)
			seen = true
		case field == 2 && wt == wireBytes: // ChannelSettings
			raw, err := r.bytes()
			if err != nil {
				return ch, false
			}
			if !decodeChannelSettings(raw, &ch) {
				return ch, false
			}
			seen = true
		case field == 3 && wt == wireVarint:
			v, err := r.varint()
			if err != nil {
				return ch, false
			}
			ch.Role = ChannelRole(v)
			seen = true
		default:
			if err := r.skip(wt); err != nil {
				return ch, false
			}
		}
	}
	return ch, seen
}

func decodeChannelSettings(b []byte, ch *ChannelInfo) bool {
	// A settings message that carries no psk field means an empty key, which
	// is "no encryption" — a real answer, not an unknown one.
	ch.KeyClass, ch.KeyFingerprint = KeyNone, ""
	r := &reader{b: b}
	for !r.done() {
		tag, err := r.varint()
		if err != nil {
			return false
		}
		field, wt := int(tag>>3), int(tag&7)
		switch {
		case field == 2 && wt == wireBytes: // psk
			raw, err := r.bytes()
			if err != nil {
				return false
			}
			// Hashed here; the plaintext goes no further than this call.
			ch.KeyClass, ch.KeyFingerprint = keyFingerprint(raw)
		case field == 3 && wt == wireBytes: // name
			raw, err := r.bytes()
			if err != nil {
				return false
			}
			ch.Name = string(raw)
		default:
			if err := r.skip(wt); err != nil {
				return false
			}
		}
	}
	return true
}

func decodeMetadataFirmware(b []byte) (string, bool) {
	r := &reader{b: b}
	for !r.done() {
		tag, err := r.varint()
		if err != nil {
			return "", false
		}
		field, wt := int(tag>>3), int(tag&7)
		if field == 1 && wt == wireBytes {
			raw, err := r.bytes()
			if err != nil {
				return "", false
			}
			return string(raw), true
		}
		if err := r.skip(wt); err != nil {
			return "", false
		}
	}
	return "", false
}

// RegionValue and PresetValue resolve a name (or a bare number) for tools
// that take these as flags. Unknown names give the zero value rather than an
// error: a dev tool asked for a region it cannot resolve should report an
// obviously wrong radio, not refuse to start.
func RegionValue(name string) uint32 {
	v, _ := resolveEnum(regionNames, name, "region")
	return v
}

func PresetValue(name string) uint32 {
	v, _ := resolveEnum(presetNames, name, "modem preset")
	return v
}

// RegionName and PresetName render one value.
//
// They exist because two call sites reached into RegionNames()/PresetNames()
// with the enum value as a SLICE INDEX — a sorted list of names addressed by
// a number that is not its position in that list. It read correctly only by
// coincidence, and only for the values somebody happened to try.
func RegionName(v uint32) string { return enumName(regionNames, v) }
func PresetName(v uint32) string { return enumName(presetNames, v) }
