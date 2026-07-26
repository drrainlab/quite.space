package meshtastic

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
)

// ---- a fake device that answers want_config with a config dump ----
//
// These encoders are the test's statement of what real firmware sends,
// transcribed from the schema independently of the decoder in config.go, so
// a typo in either shows up here. What they cannot catch is a systematic
// misreading of the upstream .proto — both halves would then be wrong
// together. Only real hardware settles that, which is what the `--raw` field
// dump in cmd/quiet-radio exists for.

func devLoRa(region, preset, hop uint32, usePreset, txEnabled bool) []byte {
	lora := appendBoolField(nil, 1, usePreset)
	lora = appendVarintField(lora, 2, uint64(preset))
	lora = appendVarintField(lora, 7, uint64(region))
	lora = appendVarintField(lora, 8, uint64(hop))
	lora = appendBoolField(lora, 9, txEnabled)
	cfg := appendBytesField(nil, 6, lora) // Config.lora
	return appendBytesField(nil, 5, cfg)  // FromRadio.config
}

func devChannel(index int, name string, role uint32, psk []byte) []byte {
	set := appendBytesField(nil, 2, psk)
	set = appendBytesField(set, 3, []byte(name))
	ch := appendVarintField(nil, 1, uint64(index))
	ch = appendBytesField(ch, 2, set)
	ch = appendVarintField(ch, 3, uint64(role))
	return appendBytesField(nil, 10, ch) // FromRadio.channel
}

func devMetadata(firmware string) []byte {
	md := appendBytesField(nil, 1, []byte(firmware))
	return appendBytesField(nil, 13, md) // FromRadio.metadata
}

// dialFake connects a Radio to a device that emits dump during the config
// handshake, exactly where real firmware emits it.
func dialFake(t *testing.T, dump ...[]byte) *Radio {
	t.Helper()
	cli, dev := net.Pipe()
	go func() {
		defer dev.Close()
		br := bufio.NewReader(dev)
		for {
			frame, err := readFrame(br)
			if err != nil {
				return
			}
			r := &reader{b: frame}
			for !r.done() {
				tag, err := r.varint()
				if err != nil {
					return
				}
				field, wt := int(tag>>3), int(tag&7)
				if field == 3 && wt == wireVarint { // want_config_id
					want, err := r.varint()
					if err != nil {
						return
					}
					writeFrame(dev, hubMyInfo(0x1234))
					for _, f := range dump {
						writeFrame(dev, f)
					}
					writeFrame(dev, hubConfigComplete(uint32(want)))
					continue
				}
				if err := r.skip(wt); err != nil {
					return
				}
			}
		}
	}()
	radio, err := Connect(cli, Options{})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { radio.Close() })
	return radio
}

var betaPSK = []byte{
	0x9f, 0x2a, 0x01, 0x77, 0xbe, 0xef, 0x12, 0x34,
	0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc,
}

// The node already streams its whole configuration during the handshake; we
// were throwing it away. Keeping it is what lets a person see WHY two radios
// cannot hear each other, instead of staring at silence.
func TestRadioReportsWhatTheNodeIsConfiguredFor(t *testing.T) {
	radio := dialFake(t,
		devMetadata("2.5.13.abcdef"),
		devLoRa(regionEU868, presetLongFast, 3, true, true),
		devChannel(0, "quiet-beta", 1, betaPSK),
	)
	cfg := radio.Config()
	if cfg.LoRa == nil {
		t.Fatal("the node reported its LoRa config and we kept none of it")
	}
	if got := cfg.LoRa.RegionName(); got != "EU_868" {
		t.Errorf("region = %q, want EU_868", got)
	}
	if got := cfg.LoRa.PresetName(); got != "LONG_FAST" {
		t.Errorf("modem preset = %q, want LONG_FAST", got)
	}
	if cfg.LoRa.HopLimit != 3 || !cfg.LoRa.TxEnabled || !cfg.LoRa.UsePreset {
		t.Errorf("lora scalars wrong: %+v", *cfg.LoRa)
	}
	if cfg.Firmware != "2.5.13.abcdef" {
		t.Errorf("firmware = %q", cfg.Firmware)
	}
	ch, ok := cfg.Channel(0)
	if !ok {
		t.Fatal("channel 0 was reported and we kept none of it")
	}
	if ch.Name != "quiet-beta" || ch.Role != ChannelPrimary {
		t.Errorf("channel 0 = %+v", ch)
	}
	if ch.KeyClass != KeyCustom || ch.KeyFingerprint == "" {
		t.Errorf("a 16-byte private key was not recognised: %+v", ch)
	}
}

// The PSK is the one thing on a radio that must never be shown back. It is
// hashed where it is decoded, and the plaintext is never held anywhere a
// diagnostic, a log line or a screen could reach it.
func TestConfigNeverCarriesThePSK(t *testing.T) {
	radio := dialFake(t,
		devLoRa(regionEU868, presetLongFast, 3, true, true),
		devChannel(0, "quiet-beta", 1, betaPSK),
	)
	cfg := radio.Config()
	rendered := fmt.Sprintf("%#v | %+v | %s", cfg, cfg, cfg.Report())
	for _, form := range []string{
		string(betaPSK),
		fmt.Sprintf("%x", betaPSK),
		fmt.Sprintf("%v", betaPSK),
	} {
		if strings.Contains(rendered, form) {
			t.Fatalf("the channel key leaked into a rendering of the config")
		}
	}
	// And it is not reachable through the struct either.
	ch, _ := cfg.Channel(0)
	if strings.Contains(fmt.Sprintf("%#v", ch), fmt.Sprintf("%x", betaPSK[:4])) {
		t.Fatal("the channel key leaked into the channel struct")
	}
}

// The default Meshtastic key is public knowledge. A beta segment running on
// it is not private in any sense, and the diagnostic has to say so rather
// than report a cheerful match.
func TestDefaultKeyIsRecognisedAsNotPrivate(t *testing.T) {
	radio := dialFake(t,
		devLoRa(regionEU868, presetLongFast, 3, true, true),
		devChannel(0, "LongFast", 1, []byte{0x01}),
	)
	ch, ok := radio.Config().Channel(0)
	if !ok {
		t.Fatal("no channel 0")
	}
	if ch.KeyClass != KeyDefault {
		t.Fatalf("the well-known default key was not recognised: %+v", ch)
	}
}

// Firmware evolves faster than this subset does. An enum value we do not
// know is reported as the number we saw — never mapped to a neighbouring
// name, which would be a confident lie about someone's radio.
func TestUnknownEnumIsReportedAsItsNumber(t *testing.T) {
	radio := dialFake(t, devLoRa(200, 199, 3, true, true))
	cfg := radio.Config()
	if got := cfg.LoRa.RegionName(); got != "UNKNOWN(200)" {
		t.Errorf("unknown region rendered as %q", got)
	}
	if got := cfg.LoRa.PresetName(); got != "UNKNOWN(199)" {
		t.Errorf("unknown preset rendered as %q", got)
	}
}

// A node that reports nothing must read as "could not verify", not as
// "misconfigured". Nodes on older firmware, and our own Hub, send no config
// at all — telling their operator that their region is wrong would send
// them chasing a fault that does not exist.
func TestSilentNodeIsUnknownNotWrong(t *testing.T) {
	radio := dialFake(t) // handshake, no config dump
	cfg := radio.Config()
	if cfg.LoRa != nil {
		t.Fatal("invented a LoRa config the node never sent")
	}
	if _, ok := cfg.Channel(0); ok {
		t.Fatal("invented a channel the node never sent")
	}
}
