// The Gateway screen's data (RB-2). Everything a person needs when their
// radio is not working, in one place.
//
// This is deliberately a STATUS screen, not the Gateway Terminal of
// ENGINEERING_PLAN §5.7 — that remains deferred. Nothing here is an event,
// nothing here is replicated, and nothing here is a claim about anyone.
//
// What it answers, in the order a person actually asks:
//
//	is my radio attached, and if not, is anything being done about it
//	is my radio set up for this segment, and if not, exactly which field
//	is a gateway out there, is it the one I was told about
//	do I trust it, and what happens to my messages if I do not
//
// Never in the response: the channel key (hashed at decode — see
// transports/meshtastic/config.go), any private key, or anything from the
// keystore. A test asserts it.
package node

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

type gatewayResp struct {
	Network        string       `json:"network"`
	Radio          gwRadio      `json:"radio"`
	Config         *gwConfig    `json:"config"`
	Profile        []gwCheck    `json:"profile"`
	ProfileVerdict string       `json:"profileVerdict"`
	Gateways       []gwPresence `json:"gateways"`
	Summary        string       `json:"summary"`
	ForeignBeacons int          `json:"foreignBeacons"`
	Pins           []gwPin      `json:"pins"`
	PinWarnings    []string     `json:"pinWarnings"`
	Advice         []string     `json:"advice"`
	SerialPorts    []string     `json:"serialPorts"`
}

type gwRadio struct {
	Attached bool `json:"attached"`
	// Carrier names the driver that actually answered, so the screen can stop
	// presenting one carrier's vocabulary as every radio's. Everything below
	// NodeNum is Meshtastic's, and on any other carrier it is absent rather
	// than zero — a zero shown in a reading position is a measurement claim.
	Carrier string `json:"carrier,omitempty"`
	// KnownSegment says this device already holds a segment — it arrived with
	// an invitation — so attaching a board needs no phrase from anybody. That
	// is the whole point of carrying the segment: the words were never this
	// person's to know.
	KnownSegment bool   `json:"knownSegment,omitempty"`
	Connected    bool   `json:"connected"`
	Reconnecting bool   `json:"reconnecting"`
	NodeNum      string `json:"nodeNum"`
	TX           int    `json:"tx"`
	RX           int    `json:"rx"`
	Channel      int    `json:"channel"`
	Attempts     int    `json:"attempts"`
	Reconnects   int    `json:"reconnects"`
	NextRetryIn  string `json:"nextRetryIn"`
	Err          string `json:"err"`
}

type gwConfig struct {
	Firmware string      `json:"firmware"`
	Region   string      `json:"region"`
	Preset   string      `json:"preset"`
	HopLimit uint32      `json:"hopLimit"`
	TxOn     bool        `json:"txEnabled"`
	Channels []gwChannel `json:"channels"`
}

type gwChannel struct {
	Index       int    `json:"index"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	Key         string `json:"key"` // class only — never the key
	Fingerprint string `json:"fingerprint"`
}

type gwCheck struct {
	Field  string `json:"field"`
	Status string `json:"status"`
	Want   string `json:"want"`
	Got    string `json:"got"`
	Why    string `json:"why"`
	Fix    string `json:"fix"`
}

type gwPresence struct {
	Fingerprint string `json:"fingerprint"`
	Label       string `json:"label"`
	Network     string `json:"network"`
	Link        string `json:"link"`
	Trusted     bool   `json:"trusted"`
	UplinkUp    bool   `json:"uplinkUp"`
	Queue       uint64 `json:"queue"`
	Fresh       bool   `json:"fresh"`
	LastHeard   string `json:"lastHeard"`
	Key         string `json:"key"` // public, for the pin button
}

type gwPin struct {
	Link        string `json:"link"`
	Fingerprint string `json:"fingerprint"`
	Label       string `json:"label"`
	PinnedAt    string `json:"pinnedAt"`
	Replaced    string `json:"replaced"`
}

func (a *APIServer) handleGateway(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	rt := a.rt
	resp := gatewayResp{
		Network:        rt.MeshNetwork(),
		Summary:        rt.GatewaySummary(now),
		ForeignBeacons: rt.ForeignBeacons(),
		PinWarnings:    rt.CustodianWarnings(),
		Profile:        []gwCheck{},
		Gateways:       []gwPresence{},
		Pins:           []gwPin{},
	}

	m := rt.Mesh()
	// Connected comes from the CARRIER-NEUTRAL face, not from Meshtastic's
	// status, which is an empty struct whenever another driver is the one
	// attached. An RNode that was transmitting read here as attached but not
	// connected, with a node number and a channel of zero sitting in reading
	// positions as though they had been measured.
	rs := rt.RadioState()
	resp.Radio = gwRadio{
		Attached:     rt.MeshAttached(),
		Carrier:      rs.Carrier,
		KnownSegment: rt.KnownSegment(),
		Connected:    rs.Connected,
		Reconnecting: m.Reconnecting,
		TX:           m.TX,
		RX:           m.RX,
		Channel:      int(m.Channel),
		Attempts:     m.Attempts,
		Reconnects:   m.Reconnects,
		Err:          m.Err,
	}
	if resp.Radio.Err == "" {
		resp.Radio.Err = rs.Err
	}
	if m.NodeNum != 0 {
		resp.Radio.NodeNum = hex.EncodeToString([]byte{
			byte(m.NodeNum >> 24), byte(m.NodeNum >> 16),
			byte(m.NodeNum >> 8), byte(m.NodeNum)})
	}
	if m.NextRetryIn > 0 {
		resp.Radio.NextRetryIn = m.NextRetryIn.Round(time.Second).String()
	}

	cfg := rt.MeshConfig()
	if cfg.LoRa != nil || len(cfg.Channels) > 0 {
		c := &gwConfig{Firmware: cfg.Firmware, Channels: []gwChannel{}}
		if cfg.LoRa != nil {
			c.Region = cfg.LoRa.RegionName()
			c.Preset = cfg.LoRa.PresetName()
			if !cfg.LoRa.UsePreset {
				c.Preset = "off (manual bandwidth/sf/cr)"
			}
			c.HopLimit = cfg.LoRa.HopLimit
			c.TxOn = cfg.LoRa.TxEnabled
		}
		for _, ch := range cfg.Channels {
			c.Channels = append(c.Channels, gwChannel{
				Index: ch.Index, Name: ch.Name, Role: ch.Role.String(),
				// The class, never the key. There is no code path from a
				// channel key to this response: it was hashed where it was
				// decoded and the plaintext dropped there.
				Key: ch.KeyClass.String(), Fingerprint: ch.KeyFingerprint,
			})
		}
		resp.Config = c
	}

	if p, ok := rt.RadioProfile(); ok {
		verdict := p.Check(cfg)
		for _, ch := range verdict {
			resp.Profile = append(resp.Profile, gwCheck{
				Field: ch.Field, Status: ch.Status.String(),
				Want: ch.Want, Got: ch.Got, Why: ch.Why, Fix: ch.Fix,
			})
		}
		switch {
		case verdict.Failed():
			resp.ProfileVerdict = "wrong"
		case verdict.Incomplete():
			resp.ProfileVerdict = "unverified"
		default:
			resp.ProfileVerdict = "ok"
		}
	}

	for _, g := range rt.Gateways() {
		p := gwPresence{
			Fingerprint: g.Fingerprint, Label: g.Label, Network: g.NetworkID,
			Link: g.LinkDomain, Trusted: g.Trusted, UplinkUp: g.UplinkUp,
			Queue: g.QueueDepth, Fresh: g.Fresh(now),
			LastHeard: humanAgo(now.Sub(g.LastHeard)),
			Key:       hex.EncodeToString(g.Key),
		}
		resp.Gateways = append(resp.Gateways, p)
	}
	for _, p := range rt.Custodians() {
		pin := gwPin{Link: p.LinkDomain, Fingerprint: p.Fingerprint(),
			Label: p.Label, Replaced: p.Replaced}
		if p.PinnedAt.After(time.Unix(100, 0)) {
			pin.PinnedAt = p.PinnedAt.Format(time.RFC3339)
		}
		resp.Pins = append(resp.Pins, pin)
	}
	if ports, err := meshtastic.ListSerialPorts(); err == nil {
		resp.SerialPorts = ports
	}
	resp.Advice = gatewayAdvice(resp)
	writeJSON(w, resp)
}

// gatewayAdvice turns the state into the next thing to try, in the order a
// person should try it. Ordering matters more than completeness: a wrong
// region is worth knowing about before an unpinned gateway, because with the
// wrong region there is no gateway to pin.
func gatewayAdvice(g gatewayResp) []string {
	var out []string
	switch {
	case !g.Radio.Attached:
		// Appended, not returned: a gateway can also be heard over the LAN,
		// and returning here would silently drop every other thing this
		// person needs to know because one of them did not apply.
		out = append(out, "No radio is attached. Plug an RNode modem in over "+
			"USB and scan — this build drives one directly and it is the "+
			"radio to reach for. A Meshtastic node also works and is attached "+
			"by address at the bottom of this screen. Until one of them is "+
			"here, this device can only reach people over the internet or the "+
			"local network.")
	case g.Radio.Reconnecting:
		out = append(out, "The radio has gone away and is being redialled"+
			retryPhrase(g.Radio)+". If it does not come back, check the cable "+
			"or the node's power.")
	case !g.Radio.Connected:
		out = append(out, "The radio is not responding: "+g.Radio.Err)
	}
	// Everything below this line reads a Meshtastic node's own configuration,
	// and a modem does not have one. Saying so once is honest; letting the
	// Meshtastic advice run would tell somebody with a working radio to go
	// compare settings that do not exist.
	if g.Radio.Connected && g.Radio.Carrier != "" && g.Radio.Carrier != "meshtastic" {
		out = append(out, "This radio is an "+g.Radio.Carrier+" — a modem this "+
			"device drives directly, not a node with firmware of its own. It has "+
			"no channel table, no node number and no configuration to read back, "+
			"so the checks below do not apply to it. Its air is set by this "+
			"device and its segment by the seed.")
		return out
	}
	if g.Config == nil && g.Radio.Connected {
		out = append(out, "This node reported no configuration, so nothing "+
			"about its settings can be checked from here. Compare them by hand "+
			"with `meshtastic --info`.")
	}
	if g.ProfileVerdict == "wrong" {
		out = append(out, "This radio is not on the same air as the segment. "+
			"Apply the fixes listed below, then reconnect — the node reports "+
			"its new configuration immediately.")
	}
	if g.Config != nil {
		// Warn about the channel we actually TRANSMIT on, not every channel
		// the radio happens to have configured. A node with eight channels
		// is normal; what matters is the one carrying our traffic.
		for _, ch := range g.Config.Channels {
			if ch.Index != g.Radio.Channel {
				continue
			}
			if strings.HasPrefix(ch.Key, "default") || ch.Key == "none" {
				out = append(out, "We transmit on channel "+itoa(ch.Index)+
					" ("+chName(ch)+"), which uses "+ch.Key+". Everyone in "+
					"range sees these packets and their radios relay them. "+
					"Your messages stay encrypted end to end regardless — but "+
					"the radio layer adds nothing. Use a dedicated channel "+
					"with a private key.")
			}
		}
		// Transmitting on a channel the radio does not have configured is
		// silence with no error anywhere: the packets go nowhere.
		if _, ok := channelAt(g.Config.Channels, g.Radio.Channel); !ok && g.Radio.Connected {
			out = append(out, "We are set to transmit on channel "+
				itoa(g.Radio.Channel)+", but this radio has no such channel "+
				"configured. Nothing will be sent. Add the channel, or pick "+
				"one this radio has.")
		}
	}
	if len(g.Gateways) == 0 && g.Radio.Connected && g.ProfileVerdict != "wrong" {
		if g.ForeignBeacons > 0 {
			out = append(out, "Gateways are announcing themselves nearby, but "+
				"on a different network id. Check the network id on both sides.")
		} else {
			out = append(out, "No gateway has announced itself yet. Messages "+
				"still go out over the mesh; nothing has offered to carry them "+
				"onward to the internet.")
		}
	}
	for _, gw := range g.Gateways {
		if !gw.Trusted {
			out = append(out, "A gateway is announcing itself with fingerprint "+
				gw.Fingerprint+". If that matches what its operator gave you, "+
				"pin it — until you do, anything it says about carrying your "+
				"messages is only an observation, not proof.")
		}
		if gw.Trusted && !gw.UplinkUp {
			out = append(out, "The gateway is trusted but has no internet "+
				"uplink right now. It can still carry within the mesh; it "+
				"cannot reach the relay.")
		}
		if !gw.Fresh {
			out = append(out, "The gateway has gone quiet (last heard "+
				gw.LastHeard+"). It may be off, out of range, or its radio may "+
				"have dropped.")
		}
	}
	for _, w := range g.PinWarnings {
		out = append(out, "A pinned gateway could not be read: "+w)
	}
	return out
}

func retryPhrase(r gwRadio) string {
	if r.NextRetryIn == "" {
		return ""
	}
	return " (next attempt in " + r.NextRetryIn + ")"
}

// handlePinGateway performs the bootstrap ritual from the screen: the person
// has compared a fingerprint against what the operator told them and is
// saying yes. The key comes from presence — never from a request body — so
// this cannot be used to pin something the node has not actually heard.
func (a *APIServer) handlePinGateway(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Fingerprint string `json:"fingerprint"`
		Label       string `json:"label"`
	}](r)
	if err != nil || body.Fingerprint == "" {
		httpErr(w, http.StatusBadRequest, errors.New("fingerprint required"))
		return
	}
	for _, g := range a.rt.Gateways() {
		if g.Fingerprint != body.Fingerprint {
			continue
		}
		label := body.Label
		if label == "" {
			label = g.Label
		}
		if err := a.rt.PinCustodianLabelled(g.LinkDomain, g.Key, label); err != nil {
			httpErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "link": g.LinkDomain})
		return
	}
	httpErr(w, http.StatusNotFound, errors.New(
		"no gateway with that fingerprint has been heard. A pin is only "+
			"offered for a gateway this node has actually heard announce itself."))
}

// handleUnpinGateway withdraws trust from a link domain.
func (a *APIServer) handleUnpinGateway(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Link string `json:"link"`
	}](r)
	if err != nil || body.Link == "" {
		httpErr(w, http.StatusBadRequest, errors.New("link required"))
		return
	}
	if err := a.rt.UnpinCustodian(body.Link); err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// pinnedKeyOf is a small helper for tests and callers that need the raw key.
func pinnedKeyOf(pins []CustodianPin, link string) ed25519.PublicKey {
	for _, p := range pins {
		if p.LinkDomain == link {
			return p.Key
		}
	}
	return nil
}

// channelAt finds the radio's configured channel at an index.
func channelAt(chans []gwChannel, index int) (gwChannel, bool) {
	for _, ch := range chans {
		if ch.Index == index {
			return ch, true
		}
	}
	return gwChannel{}, false
}

func chName(ch gwChannel) string {
	if ch.Name == "" {
		return "unnamed"
	}
	return ch.Name
}
