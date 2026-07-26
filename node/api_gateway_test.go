package node

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/bridge"
	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

func gatewayScreen(t *testing.T, rt *Runtime) gatewayResp {
	t.Helper()
	api := &APIServer{rt: rt}
	w := httptest.NewRecorder()
	api.handleGateway(w, httptest.NewRequest("GET", "/api/gateway", nil))
	if w.Code != 200 {
		t.Fatalf("gateway screen returned %d: %s", w.Code, w.Body.String())
	}
	var resp gatewayResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v\n%s", err, w.Body.String())
	}
	return resp
}

func gatewayScreenRaw(t *testing.T, rt *Runtime) string {
	t.Helper()
	api := &APIServer{rt: rt}
	w := httptest.NewRecorder()
	api.handleGateway(w, httptest.NewRequest("GET", "/api/gateway", nil))
	return w.Body.String()
}

// "No radio configured" and "the radio is gone" are different problems with
// different next steps, and a screen that shows both as "not connected"
// helps with neither.
func TestTheScreenSeparatesNoRadioFromALostRadio(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()

	g := gatewayScreen(t, rt)
	if g.Radio.Attached {
		t.Fatal("a node with no radio reported one attached")
	}
	if len(g.Advice) == 0 || !strings.Contains(g.Advice[0], "No radio is attached") {
		t.Errorf("no advice for a node with no radio: %v", g.Advice)
	}

	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	hub.SetConfig(&meshtastic.HubConfig{
		Region: 3, ModemPreset: 0, HopLimit: 3, UsePreset: true, TxEnabled: true,
		ChannelName: "quiet-beta", PSK: []byte{9, 8, 7, 6, 5, 4, 3, 2, 1, 0, 1, 2, 3, 4, 5, 6},
		Firmware: "2.5.13.abcdef",
	})
	if err := rt.StartMeshtastic("tcp:" + hub.Addr()); err != nil {
		t.Fatal(err)
	}

	g = gatewayScreen(t, rt)
	if !g.Radio.Attached || !g.Radio.Connected {
		t.Fatalf("an attached radio did not show as connected: %+v", g.Radio)
	}
	if g.Config == nil {
		t.Fatal("the node reported its configuration and the screen shows none")
	}
	if g.Config.Region != "EU_868" || g.Config.Firmware != "2.5.13.abcdef" {
		t.Errorf("configuration wrong: %+v", g.Config)
	}
}

// The screen exists to name the field that is wrong. Showing the settings
// without judging them leaves the person exactly where they started.
func TestTheScreenNamesTheFieldThatIsWrong(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()

	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	hub.SetConfig(&meshtastic.HubConfig{
		Region: 2 /* EU_433 */, ModemPreset: 0, HopLimit: 3,
		UsePreset: true, TxEnabled: true, ChannelName: "quiet-beta",
	})
	if err := rt.StartMeshtastic("tcp:" + hub.Addr()); err != nil {
		t.Fatal(err)
	}
	profile, err := meshtastic.ParseProfile([]byte(
		"region = EU_868\nmodem_preset = LONG_FAST\n"))
	if err != nil {
		t.Fatal(err)
	}
	rt.SetRadioProfile(&profile)

	g := gatewayScreen(t, rt)
	if g.ProfileVerdict != "wrong" {
		t.Fatalf("verdict = %q, want wrong", g.ProfileVerdict)
	}
	var region gwCheck
	for _, c := range g.Profile {
		if c.Field == "lora.region" {
			region = c
		}
	}
	if region.Status != "mismatch" || region.Want != "EU_868" || region.Got != "EU_433" {
		t.Fatalf("region check wrong: %+v", region)
	}
	if !strings.Contains(region.Fix, "--set lora.region EU_868") {
		t.Errorf("no usable fix on the screen: %q", region.Fix)
	}
}

// A node that reports nothing must not be described as misconfigured. The
// screen has to say "could not check", which is a different sentence with a
// different next step.
func TestTheScreenSaysUnverifiedRatherThanWrong(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()

	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close() // no SetConfig: this node reports nothing
	if err := rt.StartMeshtastic("tcp:" + hub.Addr()); err != nil {
		t.Fatal(err)
	}
	profile, err := meshtastic.ParseProfile([]byte("region = EU_868\n"))
	if err != nil {
		t.Fatal(err)
	}
	rt.SetRadioProfile(&profile)

	g := gatewayScreen(t, rt)
	if g.ProfileVerdict != "unverified" {
		t.Fatalf("verdict = %q, want unverified", g.ProfileVerdict)
	}
	joined := strings.Join(g.Advice, " ")
	if strings.Contains(joined, "not on the same air") {
		t.Errorf("a node that reported nothing was called misconfigured: %v", g.Advice)
	}
	if !strings.Contains(joined, "meshtastic --info") {
		t.Errorf("no way forward offered: %v", g.Advice)
	}
}

// The bootstrap ritual, end to end: an unpinned gateway is shown with a
// fingerprint and an explanation, and pinning it from the screen works —
// but ONLY for a gateway this node actually heard.
func TestPinningFromTheScreenOnlyWorksForAGatewayWeHeard(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	rt.noteBeacon("radio", gatewayBeacon(t, priv, bridge.Beacon{
		Label: "roof Pi", BootID: 1, Sequence: 1,
	}), time.Now())

	g := gatewayScreen(t, rt)
	if len(g.Gateways) != 1 || g.Gateways[0].Trusted {
		t.Fatalf("expected one untrusted gateway: %+v", g.Gateways)
	}
	fp := g.Gateways[0].Fingerprint
	if !strings.Contains(strings.Join(g.Advice, " "), fp) {
		t.Errorf("the advice does not give the fingerprint to compare: %v", g.Advice)
	}

	api := &APIServer{rt: rt}
	// A fingerprint we never heard cannot be pinned, however well formed.
	w := httptest.NewRecorder()
	api.handlePinGateway(w, httptest.NewRequest("POST", "/api/gateway/pin",
		strings.NewReader(`{"fingerprint":"deadbeef"}`)))
	if w.Code != 404 {
		t.Fatalf("pinning an unheard gateway returned %d", w.Code)
	}
	if len(rt.Custodians()) != 0 {
		t.Fatal("an unheard key got pinned")
	}

	w = httptest.NewRecorder()
	api.handlePinGateway(w, httptest.NewRequest("POST", "/api/gateway/pin",
		strings.NewReader(`{"fingerprint":"`+fp+`"}`)))
	if w.Code != 200 {
		t.Fatalf("pinning a heard gateway returned %d: %s", w.Code, w.Body.String())
	}
	if got := pinnedKeyOf(rt.Custodians(), "radio"); string(got) != string(pub) {
		t.Fatal("the wrong key was pinned")
	}
	if g := gatewayScreen(t, rt); !g.Gateways[0].Trusted {
		t.Fatal("the gateway is pinned and still shows as untrusted")
	}
}

// The screen is the widest surface any of this reaches. Nothing secret may
// come out of it: not the channel key, not any private key material.
func TestTheScreenLeaksNoSecrets(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()

	psk := []byte{0x9f, 0x2a, 0x01, 0x77, 0xbe, 0xef, 0x12, 0x34,
		0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc}
	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	hub.SetConfig(&meshtastic.HubConfig{
		Region: 3, UsePreset: true, TxEnabled: true, HopLimit: 3,
		ChannelName: "quiet-beta", PSK: psk,
	})
	if err := rt.StartMeshtastic("tcp:" + hub.Addr()); err != nil {
		t.Fatal(err)
	}

	body := gatewayScreenRaw(t, rt)
	for _, secret := range []string{
		string(psk),
		hexOf(psk),
		string(rt.Device.Seed()),
		hexOf(rt.Device.Seed()),
		string(rt.Principal.Seed()),
		hexOf(rt.Principal.Seed()),
	} {
		if secret != "" && strings.Contains(body, secret) {
			t.Fatal("a secret reached the gateway screen")
		}
	}
	// The channel is still described — by class, which is what a person
	// needs in order to know whether the segment is private at all.
	g := gatewayScreen(t, rt)
	if g.Config == nil || len(g.Config.Channels) == 0 {
		t.Fatal("no channel shown at all")
	}
	if !strings.Contains(g.Config.Channels[0].Key, "private") {
		t.Errorf("channel key class not described: %+v", g.Config.Channels[0])
	}
}

// A channel on the built-in key satisfies every other check and is still
// readable by anyone in range. The screen has to say so without overstating
// it: the messages themselves stay encrypted end to end.
func TestTheScreenWarnsAboutThePublicDefaultKey(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	hub.SetConfig(&meshtastic.HubConfig{
		Region: 3, UsePreset: true, TxEnabled: true, HopLimit: 3,
		ChannelName: "LongFast", PSK: []byte{0x01},
	})
	if err := rt.StartMeshtastic("tcp:" + hub.Addr()); err != nil {
		t.Fatal(err)
	}
	advice := strings.Join(gatewayScreen(t, rt).Advice, " ")
	if !strings.Contains(advice, "built-in") {
		t.Errorf("no warning about the public default key: %q", advice)
	}
	if !strings.Contains(advice, "end to end") {
		t.Errorf("the warning overstates the risk — messages ARE still "+
			"encrypted end to end: %q", advice)
	}
}
