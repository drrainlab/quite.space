// Minting a segment channel (RB-2). "Prepare this device for the segment."
//
// Two radios only hear each other when they agree about region, modem preset
// and — above all — the channel name and key. Getting two people in two cities
// to type the same 32 bytes correctly is not a workflow; a shareable link is.
//
// # Why this MINTS a channel rather than exporting one
//
// A Meshtastic channel URL contains the key. Our decoder deliberately hashes
// the key where it reads it and drops the plaintext (config.go), which is a
// property with a test behind it — so we *cannot* build a URL from a radio we
// read, and should not want to.
//
// So the key is born here instead, from crypto/rand, and is returned exactly
// once. We are its origin, never its extractor. That also gives read-after-
// write verification for free: because we know what we minted, we know the
// fingerprint the radio must report back, and the fingerprint is all we keep.
//
// # The URL carries a secret
//
// It is not a diagnostic string. Anyone holding it can join the segment's
// radio channel. It belongs in the same category as the channel key itself:
// shown once, shared over something trustworthy, never logged.
package meshtastic

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
)

// ChannelURLPrefix is the reference client's sharing format.
const ChannelURLPrefix = "https://meshtastic.org/e/#"

// pskLen is 32 bytes — AES-256, the strongest a Meshtastic channel takes.
const pskLen = 32

// SegmentChannel is a newly minted channel for one segment. The Key lives in
// this struct and nowhere else: it is never persisted, never logged, and
// never returned by any read path.
type SegmentChannel struct {
	Name string
	Key  []byte

	// The radio settings the segment shares, carried in the same URL so one
	// import configures everything that has to match.
	Region      uint32
	ModemPreset uint32
	HopLimit    uint32
}

// MintSegmentChannel creates a channel with a fresh random key.
func MintSegmentChannel(name string, region, preset, hopLimit uint32) (*SegmentChannel, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("meshtastic: a segment channel needs a name")
	}
	// The firmware refuses a longer name outright rather than truncating —
	// better to say so here than to have the device reject it later.
	if len(name) > MaxChannelNameLen {
		return nil, fmt.Errorf("meshtastic: channel name %q is %d characters; "+
			"the radio firmware allows at most %d", name, len(name), MaxChannelNameLen)
	}
	key := make([]byte, pskLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("meshtastic: no entropy for a channel key: %w", err)
	}
	return &SegmentChannel{
		Name: name, Key: key,
		Region: region, ModemPreset: preset, HopLimit: hopLimit,
	}, nil
}

// MaxChannelNameLen is the firmware's limit (char name[12] — 11 plus NUL).
// Exceeding it is refused by the device, not truncated.
const MaxChannelNameLen = 11

// Fingerprint is what survives after the key is gone: the same truncated hash
// the config reader computes, so a radio's reported channel can be checked
// against what was minted without either side holding the key.
func (c *SegmentChannel) Fingerprint() string {
	_, fp := keyFingerprint(c.Key)
	return fp
}

// ChannelSet / ChannelSettings / LoRaConfig field numbers, from the reference
// protobufs (apponly.proto, channel.proto, config.proto):
//
//	ChannelSet:      settings=1, lora_config=2
//	ChannelSettings: psk=2, name=3
//	LoRaConfig:      use_preset=1, modem_preset=2, region=7, hop_limit=8,
//	                 tx_enabled=9
const (
	csSettings   = 1
	csLoRaConfig = 2
)

// URL renders the channel as the reference client's sharing link.
//
// CONTAINS THE KEY. Treat every returned string as secret.
func (c *SegmentChannel) URL() string {
	settings := appendBytesField(nil, 2, c.Key)              // psk
	settings = appendBytesField(settings, 3, []byte(c.Name)) // name

	lora := appendBoolField(nil, 1, true)                    // use_preset
	lora = appendVarintField(lora, 2, uint64(c.ModemPreset)) // modem_preset
	lora = appendVarintField(lora, 7, uint64(c.Region))      // region
	lora = appendVarintField(lora, 8, uint64(c.HopLimit))    // hop_limit
	lora = appendBoolField(lora, 9, true)                    // tx_enabled

	set := appendBytesField(nil, csSettings, settings)
	set = appendBytesField(set, csLoRaConfig, lora)

	// urlsafe base64 with padding stripped, matching the reference client.
	s := base64.URLEncoding.EncodeToString(set)
	s = strings.ReplaceAll(s, "=", "")
	return ChannelURLPrefix + s
}

// AddCommands are the copy-pasteable CLI steps that ADD this channel without
// disturbing anything already on the radio.
//
// Deliberately NOT `--seturl`: that replaces the entire channel set, and a
// radio with channels its owner cares about would lose them. Adding is the
// only safe default; replacing has to be a decision someone makes on purpose.
func (c *SegmentChannel) AddCommands(port string) []string {
	if port == "" {
		port = "/dev/ttyACM0"
	}
	key := base64.StdEncoding.EncodeToString(c.Key)
	return []string{
		fmt.Sprintf("meshtastic --port %s --ch-add %s", port, c.Name),
		// The reference CLI clears the name when it writes a psk to a freshly
		// added channel, so the name is set again afterwards. Found on real
		// hardware; without the third command the channel ends up unnamed and
		// the two radios then disagree about the channel identity.
		fmt.Sprintf("meshtastic --port %s --ch-index %%d --ch-set psk base64:%s", port, key),
		fmt.Sprintf("meshtastic --port %s --ch-index %%d --ch-set name %s", port, c.Name),
	}
}

// RegionCommands set the segment's LoRa settings, for a radio that does not
// already match. Separate from the channel because most radios need only one
// or the other.
func (c *SegmentChannel) RegionCommands(port string) []string {
	if port == "" {
		port = "/dev/ttyACM0"
	}
	return []string{
		fmt.Sprintf("meshtastic --port %s --set lora.region %s",
			port, enumName(regionNames, c.Region)),
		fmt.Sprintf("meshtastic --port %s --set lora.modem_preset %s --set lora.use_preset true",
			port, enumName(presetNames, c.ModemPreset)),
		fmt.Sprintf("meshtastic --port %s --set lora.hop_limit %d", port, c.HopLimit),
	}
}

// Profile is the segment profile this channel will produce once imported —
// the thing to check a radio against afterwards. It carries the fingerprint,
// never the key, so it is safe to save and send.
func (c *SegmentChannel) Profile(index int) Profile {
	region, preset, hop := c.Region, c.ModemPreset, c.HopLimit
	tx := true
	return Profile{
		Name:             c.Name,
		Region:           &region,
		ModemPreset:      &preset,
		HopLimit:         &hop,
		TxEnabled:        &tx,
		ChannelIndex:     index,
		ChannelName:      c.Name,
		Key:              &KeyRequirement{Class: KeyCustom, Fingerprint: c.Fingerprint()},
		channelSpecified: true,
	}
}

// FreeChannelSlot reports the first index a channel can be added at without
// disturbing anything already configured, and how many slots exist.
//
// Add-only: a radio carrying channels its owner set up is not ours to edit.
func FreeChannelSlot(cfg NodeConfig) (int, bool) {
	const slots = 8
	used := map[int]bool{}
	for _, ch := range cfg.Channels {
		// A DISABLED slot with no key is genuinely free; anything else is
		// somebody's channel.
		if ch.Role == ChannelDisabled && ch.KeyClass == KeyNone && ch.Name == "" {
			continue
		}
		used[ch.Index] = true
	}
	for i := range slots {
		if !used[i] {
			return i, true
		}
	}
	return 0, false
}
