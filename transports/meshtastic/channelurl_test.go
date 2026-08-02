package meshtastic

import (
	"bytes"
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"
)

func mintTest(t *testing.T) *SegmentChannel {
	t.Helper()
	c, err := MintSegmentChannel("pinelover", regionRU, presetLongFast, 3)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The one test only an independent implementation can give us: does the
// reference Meshtastic client parse the URL we generate, and does it read
// back the channel we meant?
//
// Our protobuf is hand-rolled. A wrong field number here produces a URL that
// looks perfectly well-formed, imports "successfully", and silently
// configures the wrong thing — the exact class of bug that costs an evening
// of blaming the radio. Skipped when the reference client is not installed.
func TestReferenceClientParsesOurURL(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	probe := exec.Command("python3", "-c", "import meshtastic")
	if err := probe.Run(); err != nil {
		t.Skip("the reference meshtastic package is not installed")
	}

	c := mintTest(t)
	script := `
import sys, base64
from meshtastic import apponly_pb2
url = sys.argv[1]
frag = url.split("#", 1)[1]
frag += "=" * (-len(frag) % 4)
cs = apponly_pb2.ChannelSet()
cs.ParseFromString(base64.urlsafe_b64decode(frag))
s = cs.settings[0]
print("name=%s" % s.name)
print("psk=%s" % base64.b64encode(s.psk).decode())
print("region=%d" % cs.lora_config.region)
print("preset=%d" % cs.lora_config.modem_preset)
print("hop=%d" % cs.lora_config.hop_limit)
print("use_preset=%s" % cs.lora_config.use_preset)
print("settings_count=%d" % len(cs.settings))
`
	out, err := exec.Command("python3", "-c", script, c.URL()).CombinedOutput()
	if err != nil {
		t.Fatalf("the reference client could not parse our URL: %v\n%s", err, out)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		}
	}
	want := map[string]string{
		"name":           "pinelover",
		"psk":            base64.StdEncoding.EncodeToString(c.Key),
		"region":         "9", // RU
		"preset":         "0", // LONG_FAST
		"hop":            "3",
		"use_preset":     "True",
		"settings_count": "1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("reference client read %s=%q, we meant %q", k, got[k], v)
		}
	}
}

// The key must be freshly random every time. A minted channel that repeated
// would put two segments on the same air with the same key.
func TestEveryMintedKeyIsFreshAndFullLength(t *testing.T) {
	seen := map[string]bool{}
	for range 16 {
		c := mintTest(t)
		if len(c.Key) != 32 {
			t.Fatalf("key is %d bytes, want 32", len(c.Key))
		}
		if bytes.Equal(c.Key, make([]byte, 32)) {
			t.Fatal("the key is all zeros")
		}
		k := string(c.Key)
		if seen[k] {
			t.Fatal("a minted key repeated")
		}
		seen[k] = true
	}
}

// The URL carries the key by construction — that is what makes it work. But
// NOTHING else we hand out may. The profile in particular is meant to be
// saved and sent around.
func TestOnlyTheURLCarriesTheKey(t *testing.T) {
	c := mintTest(t)
	keyForms := []string{
		string(c.Key),
		base64.StdEncoding.EncodeToString(c.Key),
		base64.URLEncoding.EncodeToString(c.Key),
	}

	prof := c.Profile(4).Format()
	for _, form := range keyForms {
		if strings.Contains(prof, form) {
			t.Fatal("the channel key reached the saved profile")
		}
	}
	if !strings.Contains(prof, c.Fingerprint()) {
		t.Error("the profile carries no fingerprint to verify against")
	}

	// The region commands configure the radio but need no key.
	for _, cmd := range c.RegionCommands("/dev/ttyACM0") {
		for _, form := range keyForms {
			if strings.Contains(cmd, form) {
				t.Fatalf("a region command carries the key: %q", cmd)
			}
		}
	}
	// The URL, and the psk-setting command, are the deliberate exceptions.
	if !strings.HasPrefix(c.URL(), ChannelURLPrefix) {
		t.Errorf("URL prefix wrong: %q", c.URL())
	}
}

// The firmware refuses an over-long name rather than truncating it. Better to
// say so before someone shares a URL that no radio will accept.
func TestOverlongChannelNameIsRefusedWithTheLimit(t *testing.T) {
	_, err := MintSegmentChannel("pinelover.space", regionRU, presetLongFast, 3)
	if err == nil {
		t.Fatal("a 15-character channel name was accepted")
	}
	if !strings.Contains(err.Error(), "11") {
		t.Errorf("the error does not state the limit: %v", err)
	}
	if _, err := MintSegmentChannel("   ", regionRU, presetLongFast, 3); err == nil {
		t.Error("a blank name was accepted")
	}
}

// Add-only. A radio carrying channels its owner set up is not ours to edit,
// so we must never suggest --seturl, which replaces the whole set.
func TestCommandsAddRatherThanReplace(t *testing.T) {
	c := mintTest(t)
	all := strings.Join(append(c.AddCommands("/dev/ttyACM0"),
		c.RegionCommands("/dev/ttyACM0")...), "\n")
	if strings.Contains(all, "--seturl") {
		t.Fatal("suggested --seturl, which replaces every channel on the radio")
	}
	if !strings.Contains(all, "--ch-add") {
		t.Error("no add command offered")
	}
	// The name is set again after the psk: the reference CLI clears it when
	// writing a key to a freshly added channel. Found on real hardware.
	if !strings.Contains(all, "--ch-set name") {
		t.Error("the name is never set, so the channel would stay unnamed")
	}
}

// Which slot is free must be answered from what the radio reports, and a
// configured channel is never a candidate.
func TestFreeSlotNeverStepsOnAConfiguredChannel(t *testing.T) {
	cfg := NodeConfig{Channels: []ChannelInfo{
		{Index: 0, Role: ChannelPrimary, KeyClass: KeyDefault},
		{Index: 1, Name: "Korolev", Role: ChannelSecondary, KeyClass: KeyCustom},
		{Index: 2, Name: "pushka", Role: ChannelSecondary, KeyClass: KeyCustom},
		{Index: 3, Name: "SVAO", Role: ChannelSecondary, KeyClass: KeyDefault},
		{Index: 4, Role: ChannelDisabled, KeyClass: KeyNone},
	}}
	slot, ok := FreeChannelSlot(cfg)
	if !ok || slot != 4 {
		t.Fatalf("free slot = %d (%v), want 4", slot, ok)
	}

	full := NodeConfig{}
	for i := range 8 {
		full.Channels = append(full.Channels, ChannelInfo{
			Index: i, Name: "taken", Role: ChannelSecondary, KeyClass: KeyCustom})
	}
	if _, ok := FreeChannelSlot(full); ok {
		t.Fatal("offered a slot on a radio whose channels are all in use")
	}
}

// A shared link must ADD the channel, never replace what a radio already has.
//
// Found against the reference CLI, which refused the link we generated with a
// bare "Invalid URL" — and would, in the phone app, have replaced every
// channel on the device instead. The prefix carries that meaning, so it is
// worth a test of its own rather than a constant nobody reads.
func TestTheSharedLinkAddsRatherThanReplaces(t *testing.T) {
	c, err := MintSegmentChannel("segment", regionRU, presetLongFast, 3)
	if err != nil {
		t.Fatal(err)
	}
	u := c.URL()
	if !strings.Contains(u, "add=true") {
		t.Fatalf("the sharing link does not say add: %s\n"+
			"Without it the reference client refuses it, and a phone scanning "+
			"the QR wipes the radio's other channels.", u)
	}
	if !strings.HasPrefix(u, "https://meshtastic.org/e/") || !strings.Contains(u, "#") {
		t.Fatalf("link is not in the reference sharing format: %s", u)
	}
}
