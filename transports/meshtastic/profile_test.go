package meshtastic

import (
	"strings"
	"testing"
)

func checkFor(t *testing.T, checks []Check, field string) Check {
	t.Helper()
	for _, c := range checks {
		if c.Field == field {
			return c
		}
	}
	t.Fatalf("no check for %q in %v", field, checks)
	return Check{}
}

const betaProfile = `# beta segment
name          = beta-mesh-01
region        = EU_868
modem_preset  = LONG_FAST
hop_limit     = 3
tx_enabled    = true
channel_index = 0
channel_name  = quiet-beta
channel_key   = private:` + betaFingerprint

// keyFingerprint of betaPSK; pinned so a change to the hashing is deliberate.
const betaFingerprint = "3735ff6e"

func TestFingerprintIsStable(t *testing.T) {
	class, fp := keyFingerprint(betaPSK)
	if class != KeyCustom {
		t.Fatalf("class = %v", class)
	}
	if fp != betaFingerprint {
		t.Fatalf("fingerprint = %q, want %q — if this is intentional, update "+
			"the constant; every existing profile file must be regenerated", fp, betaFingerprint)
	}
}

// A mismatch has to name the field, both values, and what to type. "Radios
// misconfigured" sends a person back to a forum; "region is EU_433, the
// segment runs EU_868, run this command" ends the problem.
func TestMismatchNamesTheFieldTheValuesAndTheFix(t *testing.T) {
	p, err := ParseProfile([]byte(betaProfile))
	if err != nil {
		t.Fatal(err)
	}
	radio := dialFake(t,
		devLoRa(regionEU433, presetLongFast, 3, true, true),
		devChannel(0, "quiet-beta", 1, betaPSK),
	)
	checks := p.Check(radio.Config())
	c := checkFor(t, checks, "lora.region")
	if c.Status != CheckMismatch {
		t.Fatalf("region mismatch not reported: %+v", c)
	}
	if c.Want != "EU_868" || c.Got != "EU_433" {
		t.Errorf("want/got = %q/%q", c.Want, c.Got)
	}
	if !strings.Contains(c.Fix, "--set lora.region EU_868") {
		t.Errorf("no usable fix: %q", c.Fix)
	}
	// And nothing else was dragged in with it.
	if got := checkFor(t, checks, "lora.modem_preset"); got.Status != CheckOK {
		t.Errorf("preset matched but reported %v", got.Status)
	}
	if !Verdict(checks).Failed() {
		t.Error("a region mismatch did not fail the verdict")
	}
}

// The honesty rule. A node that reports no LoRa config — older firmware, a
// simulator, our own Hub — must read as "could not verify", never as
// "misconfigured". Telling someone their region is wrong when we simply do
// not know sends them chasing a fault that does not exist.
func TestSilenceIsUnknownNeverMismatch(t *testing.T) {
	p, err := ParseProfile([]byte(betaProfile))
	if err != nil {
		t.Fatal(err)
	}
	radio := dialFake(t) // handshake only: nothing reported
	checks := p.Check(radio.Config())
	if len(checks) == 0 {
		t.Fatal("a profile with fields produced no checks at all")
	}
	for _, c := range checks {
		if c.Status == CheckMismatch {
			t.Errorf("claimed a mismatch about a node that reported nothing: %+v", c)
		}
		if c.Status != CheckUnknown {
			t.Errorf("%s: status %v, want unknown", c.Field, c.Status)
		}
		if c.Why == "" {
			t.Errorf("%s: unknown with no explanation", c.Field)
		}
	}
	v := Verdict(checks)
	if v.Failed() {
		t.Error("unverifiable was reported as failed — those are different answers")
	}
	if !v.Incomplete() {
		t.Error("unverifiable was reported as a clean pass")
	}
}

// The operator workflow: configure ONE node by hand, capture its profile,
// then check every other node against it. The key never passes through a
// file, a terminal or a shell history — only its fingerprint does.
func TestProfileCapturedFromAConfiguredNodeVerifiesThatNode(t *testing.T) {
	radio := dialFake(t,
		devLoRa(regionEU868, presetLongFast, 3, true, true),
		devChannel(0, "quiet-beta", 1, betaPSK),
	)
	cfg := radio.Config()
	p, err := ProfileFrom("beta-mesh-01", cfg)
	if err != nil {
		t.Fatal(err)
	}
	text := p.Format()
	if strings.Contains(text, string(betaPSK)) {
		t.Fatal("the captured profile carries the channel key")
	}
	if !strings.Contains(text, betaFingerprint) {
		t.Fatalf("the captured profile carries no key fingerprint:\n%s", text)
	}
	back, err := ParseProfile([]byte(text))
	if err != nil {
		t.Fatalf("a profile we wrote does not parse: %v", err)
	}
	for _, c := range back.Check(cfg) {
		if c.Status != CheckOK {
			t.Errorf("a node does not match a profile captured from itself: %+v", c)
		}
	}
}

// A profile naming something this build cannot resolve must fail at LOAD.
// Accepting it and silently checking nothing would report a clean pass on a
// radio nobody verified.
func TestUnresolvableProfileFailsLoudly(t *testing.T) {
	_, err := ParseProfile([]byte("region = ATLANTIS_868\n"))
	if err == nil {
		t.Fatal("a region this build does not know was accepted silently")
	}
	if !strings.Contains(err.Error(), "ATLANTIS_868") {
		t.Errorf("the error does not name the offending value: %v", err)
	}
	if _, err := ParseProfile([]byte("nonsense = 1\n")); err == nil {
		t.Fatal("an unknown profile key was accepted silently")
	}
	// Firmware adds regions faster than this table does, so a number is a
	// legitimate escape hatch — the human is asserting it deliberately.
	p, err := ParseProfile([]byte("region = 23\n"))
	if err != nil {
		t.Fatalf("a numeric region was rejected: %v", err)
	}
	radio := dialFake(t, devLoRa(23, presetLongFast, 3, true, true))
	if c := checkFor(t, p.Check(radio.Config()), "lora.region"); c.Status != CheckOK {
		t.Errorf("numeric region did not match itself: %+v", c)
	}
}

// The beta's threat model asks for a private channel key as defence in
// depth. A node on the built-in key satisfies every other field and is still
// readable by anyone with a Meshtastic device — so it cannot report clean.
func TestDefaultKeyFailsAKeyRequirement(t *testing.T) {
	p, err := ParseProfile([]byte("channel_index = 0\nchannel_key = private\n"))
	if err != nil {
		t.Fatal(err)
	}
	radio := dialFake(t,
		devLoRa(regionEU868, presetLongFast, 3, true, true),
		devChannel(0, "LongFast", 1, []byte{0x01}),
	)
	c := checkFor(t, p.Check(radio.Config()), "channel.key")
	if c.Status != CheckMismatch {
		t.Fatalf("the public default key passed a private-key requirement: %+v", c)
	}
	if !strings.Contains(strings.ToLower(c.Why), "anyone") {
		t.Errorf("the explanation does not say why this matters: %q", c.Why)
	}
	// Whatever it says, it does not say the key.
	if strings.Contains(c.Fix+c.Why+c.Got, "base64:AQ==") {
		t.Error("a key value appeared in the explanation")
	}
}

// A wrong key is the failure that looks most like a hardware fault: both
// radios transmit, neither hears anything. It must be named precisely.
func TestWrongKeyIsNamedNotGuessedAt(t *testing.T) {
	p, err := ParseProfile([]byte(betaProfile))
	if err != nil {
		t.Fatal(err)
	}
	other := append([]byte(nil), betaPSK...)
	other[0] ^= 0xff
	radio := dialFake(t,
		devLoRa(regionEU868, presetLongFast, 3, true, true),
		devChannel(0, "quiet-beta", 1, other),
	)
	c := checkFor(t, p.Check(radio.Config()), "channel.key")
	if c.Status != CheckMismatch {
		t.Fatalf("a different private key passed: %+v", c)
	}
	if !strings.Contains(c.Want, betaFingerprint) {
		t.Errorf("the expected fingerprint is not shown: %+v", c)
	}
	if strings.Contains(c.Fix, "%x") || c.Fix == "" {
		t.Errorf("no usable fix: %q", c.Fix)
	}
}

// A channel the profile names but the node never reported is unknown, not
// missing: index 3 may simply not have been in the dump.
func TestMissingChannelIsUnknown(t *testing.T) {
	p, err := ParseProfile([]byte("channel_index = 3\nchannel_name = quiet-beta\n"))
	if err != nil {
		t.Fatal(err)
	}
	radio := dialFake(t, devChannel(0, "LongFast", 1, []byte{0x01}))
	c := checkFor(t, p.Check(radio.Config()), "channel.name")
	if c.Status != CheckUnknown {
		t.Fatalf("an unreported channel was judged: %+v", c)
	}
}
