package node

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

func prepare(t *testing.T, rt *Runtime, name string) (prepareResp, int) {
	t.Helper()
	api := &APIServer{rt: rt}
	w := httptest.NewRecorder()
	api.handlePrepareRadio(w, httptest.NewRequest("POST", "/api/gateway/prepare",
		strings.NewReader(`{"name":"`+name+`"}`)))
	var resp prepareResp
	if w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad json: %v\n%s", err, w.Body.String())
		}
	}
	return resp, w.Code
}

// A radio with channels its owner set up is not ours to edit. The prepared
// channel must land in a FREE slot, and the flow must never suggest anything
// that replaces the existing set.
func TestPrepareAddsWithoutTouchingExistingChannels(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()

	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	hub.SetConfig(&meshtastic.HubConfig{
		Region: 9 /* RU */, ModemPreset: 0, HopLimit: 3,
		UsePreset: true, TxEnabled: true, ChannelName: "LongFast",
		PSK: []byte{0x01}, Firmware: "2.7.15",
	})
	if err := rt.StartMeshtastic("tcp:" + hub.Addr()); err != nil {
		t.Fatal(err)
	}

	resp, code := prepare(t, rt, "pinelover")
	if code != 200 {
		t.Fatalf("prepare returned %d", code)
	}
	// The hub reports one channel at index 0, so the next free slot is 1.
	if resp.Index != 1 {
		t.Errorf("would add at slot %d, want 1 — slot 0 is already in use", resp.Index)
	}
	// The segment inherits the radio's own region and preset: those are
	// legally constrained per country and must never be invented here.
	if resp.Region != "RU" || resp.Preset != "LONG_FAST" {
		t.Errorf("did not inherit the radio's settings: %s / %s", resp.Region, resp.Preset)
	}
	all := strings.Join(append(resp.AddCommands, resp.RegionCommands...), "\n")
	if strings.Contains(all, "--seturl") {
		t.Fatal("suggested --seturl, which replaces every channel on the radio")
	}
	if !strings.Contains(all, "--ch-index 1") {
		t.Errorf("commands do not target the free slot: %q", all)
	}
	if !strings.Contains(strings.Join(resp.Steps, " "), "ADD, not Replace") {
		t.Error("the steps do not warn against Replace in the app")
	}
}

// The URL and QR carry the key by design — that is what makes one link
// configure two radios. NOTHING else may, and above all not the profile,
// which is meant to be saved and sent to the other person.
func TestPrepareKeepsTheKeyToTheLinkAlone(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()

	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	hub.SetConfig(&meshtastic.HubConfig{
		Region: 9, ModemPreset: 0, HopLimit: 3, UsePreset: true, TxEnabled: true,
	})
	if err := rt.StartMeshtastic("tcp:" + hub.Addr()); err != nil {
		t.Fatal(err)
	}

	resp, code := prepare(t, rt, "pinelover")
	if code != 200 {
		t.Fatalf("prepare returned %d", code)
	}
	// Recover the key from the URL — the one place it legitimately lives.
	frag := resp.URL[strings.Index(resp.URL, "#")+1:]
	for len(frag)%4 != 0 {
		frag += "="
	}
	raw, err := base64.URLEncoding.DecodeString(frag)
	if err != nil {
		t.Fatalf("our own URL does not decode: %v", err)
	}
	if len(raw) < 32 {
		t.Fatal("the URL is too short to contain a 32-byte key")
	}

	// The profile is the artefact that travels. It must carry the
	// fingerprint and nothing more.
	if strings.Contains(resp.Profile, resp.URL) {
		t.Fatal("the profile embeds the whole link, key and all")
	}
	if !strings.Contains(resp.Profile, resp.Fingerprint) {
		t.Error("the profile carries no fingerprint to verify against")
	}
	if resp.Fingerprint == "" || len(resp.Fingerprint) > 16 {
		t.Errorf("fingerprint looks wrong: %q", resp.Fingerprint)
	}
	// And the person is told, in words, that the link is a secret.
	warn := strings.Join(resp.Warnings, " ")
	if !strings.Contains(warn, "key") || !strings.Contains(warn, "once") {
		t.Errorf("the warnings do not say the link is secret and shown once: %q", warn)
	}
	if resp.QRBase64 == "" {
		t.Error("no QR code produced")
	}
}

// Refuse rather than guess. Building a segment channel for a radio we cannot
// read would produce a link that configures the wrong thing.
func TestPrepareRefusesWithoutAReadableRadio(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()

	if _, code := prepare(t, rt, "pinelover"); code != 409 {
		t.Fatalf("prepare with no radio returned %d, want 409", code)
	}

	// A radio that reports nothing is equally unusable as a basis.
	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close() // no SetConfig
	if err := rt.StartMeshtastic("tcp:" + hub.Addr()); err != nil {
		t.Fatal(err)
	}
	if _, code := prepare(t, rt, "pinelover"); code != 409 {
		t.Fatalf("prepare on a silent radio returned %d, want 409", code)
	}
}

// The firmware refuses an over-long name outright. Saying so here is much
// better than a link no radio will accept.
func TestPrepareRefusesAnOverlongName(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	hub.SetConfig(&meshtastic.HubConfig{Region: 9, UsePreset: true, TxEnabled: true, HopLimit: 3})
	if err := rt.StartMeshtastic("tcp:" + hub.Addr()); err != nil {
		t.Fatal(err)
	}
	if _, code := prepare(t, rt, "pinelover.space"); code != 400 {
		t.Fatalf("a 15-character name returned %d, want 400", code)
	}
}

// Adopting the profile must also point the node at the segment's channel.
// Verifying a profile while transmitting on a different channel is silence
// with no error anywhere.
func TestAdoptingAProfileAlsoSetsTheTransmitChannel(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()

	profile := "name = pinelover\nregion = RU\nchannel_index = 4\n" +
		"channel_name = pinelover\nchannel_key = private:323f1163\n"
	api := &APIServer{rt: rt}
	w := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"profile": profile})
	api.handleAdoptProfile(w, httptest.NewRequest("POST", "/api/gateway/profile",
		strings.NewReader(string(body))))
	if w.Code != 200 {
		t.Fatalf("adopt returned %d: %s", w.Code, w.Body.String())
	}
	if got := rt.MeshChannel(); got != 4 {
		t.Fatalf("transmit channel = %d, want 4 — the profile named channel 4", got)
	}
	if p, ok := rt.RadioProfile(); !ok || p.Name != "pinelover" {
		t.Fatal("the profile was not installed")
	}
}

// Real hardware caught this: ListSerialPorts on a laptop returns mostly
// Bluetooth audio devices, and taking the first one told the person to
// configure their headphones. The attached radio knows exactly which device
// it is talking to; nothing may guess when that answer exists.
func TestPortInstructionsNameTheRadioNotAHeadset(t *testing.T) {
	got, ok := likelyRadioPort([]string{
		"/dev/cu.ATH-S220BT",
		"/dev/cu.Bluetooth-Incoming-Port",
		"/dev/cu.JBLPulse4",
		"/dev/cu.debug-console",
		"/dev/cu.usbmodem24EC4A307B541",
		"/dev/tty.usbmodem24EC4A307B541",
	})
	if !ok {
		t.Fatal("no candidate found among a list that contains a real radio")
	}
	if got != "/dev/cu.usbmodem24EC4A307B541" {
		t.Fatalf("picked %q — that is not the radio", got)
	}

	// Nothing plausible at all is better answered with nothing than with a
	// confident wrong device.
	if _, ok := likelyRadioPort([]string{"/dev/cu.ATH-S220BT", "/dev/cu.JBLPulse4"}); ok {
		t.Fatal("offered a Bluetooth audio device as the radio")
	}
	// Linux shapes.
	if got, ok := likelyRadioPort([]string{"/dev/ttyACM0"}); !ok || got != "/dev/ttyACM0" {
		t.Errorf("Linux device not recognised: %q %v", got, ok)
	}
}
