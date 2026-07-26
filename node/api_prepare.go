// "Prepare this device for the segment" (RB-2). One place that turns a bare
// Meshtastic radio into one that can carry a Quiet Spaces segment.
//
// It MINTS a channel rather than writing to the radio. Two reasons, and the
// second is the load-bearing one:
//
//   - We have no AdminMessage support, and adding it means getting session
//     passkeys and rollback-on-partial-failure right against hardware. Not a
//     thing to get subtly wrong on someone's only radio.
//   - A channel URL contains the key, and our config reader deliberately
//     hashes keys where it reads them and drops the plaintext. We therefore
//     CANNOT export a channel we read — and should not want to. Minting makes
//     us the origin of the key rather than its extractor.
//
// Because we know what we minted, we know the fingerprint the radio must
// report back afterwards. That is read-after-write verification with the key
// never stored anywhere: the profile carries the fingerprint alone.
//
// The response is shown ONCE and is secret. The URL is not a diagnostic
// string — anyone holding it can join the segment's radio channel.
package node

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

type prepareResp struct {
	Channel string `json:"channel"`
	Index   int    `json:"index"`
	Region  string `json:"region"`
	Preset  string `json:"preset"`
	Hop     uint32 `json:"hopLimit"`

	// URL and QR CONTAIN THE CHANNEL KEY. Returned once, never stored.
	URL      string `json:"url"`
	QRBase64 string `json:"qrPngBase64"`

	// Fingerprint is what survives: it identifies the key without revealing
	// it, and is what a radio is verified against afterwards.
	Fingerprint string `json:"fingerprint"`
	Profile     string `json:"profile"`

	AddCommands    []string `json:"addCommands"`
	RegionCommands []string `json:"regionCommands"`
	Steps          []string `json:"steps"`
	Warnings       []string `json:"warnings"`
}

// handlePrepareRadio mints a segment channel for this radio.
//
// It reads the attached radio first so the answer fits the device in front of
// the person: which slot is free, and whether the LoRa settings already
// match. Refuses rather than guessing when there is no radio to read.
func (a *APIServer) handlePrepareRadio(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Name string `json:"name"`
	}](r)
	if err != nil || strings.TrimSpace(body.Name) == "" {
		httpErr(w, http.StatusBadRequest, errors.New(
			"a channel name is required (at most 11 characters — the radio "+
				"firmware refuses longer ones rather than shortening them)"))
		return
	}

	cfg := a.rt.MeshConfig()
	if cfg.LoRa == nil {
		httpErr(w, http.StatusConflict, errors.New(
			"this node has not reported its LoRa configuration, so a segment "+
				"channel cannot be built for it. Attach a radio first; if one "+
				"is attached, it may be older firmware that reports nothing."))
		return
	}
	slot, ok := meshtastic.FreeChannelSlot(cfg)
	if !ok {
		httpErr(w, http.StatusConflict, errors.New(
			"all eight channel slots on this radio are in use. Free one in the "+
				"Meshtastic app first — nothing here will overwrite a channel "+
				"you configured."))
		return
	}

	// The segment inherits the radio's CURRENT region and preset. Those are
	// legally and physically constrained per country, so inventing values
	// here would be worse than useless.
	ch, err := meshtastic.MintSegmentChannel(body.Name,
		cfg.LoRa.Region, cfg.LoRa.ModemPreset, cfg.LoRa.HopLimit)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}

	port := devicePortFor(a.rt)
	prof := ch.Profile(slot)

	resp := prepareResp{
		Channel:     ch.Name,
		Index:       slot,
		Region:      cfg.LoRa.RegionName(),
		Preset:      cfg.LoRa.PresetName(),
		Hop:         cfg.LoRa.HopLimit,
		URL:         ch.URL(),
		Fingerprint: ch.Fingerprint(),
		Profile:     prof.Format(),
		AddCommands: withSlot(ch.AddCommands(port), slot),
		Steps: []string{
			"Scan the code in the Meshtastic app, or open the link on the phone " +
				"paired with the radio.",
			"Choose ADD, not Replace. Replacing would remove the channels " +
				"already on this radio.",
			"Do the same on every radio in the segment — the same link, the " +
				"same channel. Radios with different keys never hear each other.",
			"Come back here and press Verify. This node re-reads the radio and " +
				"checks what actually landed.",
		},
		Warnings: []string{
			"This link contains the channel key. Anyone who has it can join " +
				"this radio channel. Share it the way you would share a password, " +
				"and not through the mesh itself.",
			"It is shown once. Nothing here stores the key — only its " +
				"fingerprint (" + ch.Fingerprint() + "), which is what Verify " +
				"compares against.",
		},
	}
	// Region commands are only worth showing when the radio does not already
	// match; a person following a list of no-ops loses trust in the list.
	if cfg.LoRa.Region == 0 || !cfg.LoRa.UsePreset {
		resp.RegionCommands = ch.RegionCommands(port)
	}

	if png, err := qrcode.Encode(ch.URL(), qrcode.Medium, 320); err == nil {
		resp.QRBase64 = base64.StdEncoding.EncodeToString(png)
	}
	writeJSON(w, resp)
}

// withSlot fills the channel index into the command templates. The index is
// known only after reading the radio, which is why the templates carry a
// placeholder rather than a guess.
func withSlot(cmds []string, slot int) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, strings.ReplaceAll(c, "%d", itoa(slot)))
	}
	return out
}

// handleAdoptProfile installs a segment profile so the Gateway screen can
// check this radio against it. Sent back by the prepare flow, or pasted from
// whoever set the segment up.
func (a *APIServer) handleAdoptProfile(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Profile string `json:"profile"`
	}](r)
	if err != nil || strings.TrimSpace(body.Profile) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("profile text required"))
		return
	}
	p, err := meshtastic.ParseProfile([]byte(body.Profile))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	a.rt.SetRadioProfile(&p)
	// The profile names the channel the segment lives on; transmitting on a
	// different one is silence with no error anywhere.
	if p.ChannelIndex >= 0 {
		a.rt.SetMeshChannel(uint32(p.ChannelIndex))
	}
	writeJSON(w, map[string]any{
		"ok":      true,
		"name":    p.Name,
		"channel": p.ChannelIndex,
	})
}

// devicePortFor names the device the person should point the Meshtastic CLI
// at. The attached radio knows exactly which one it is; only when nothing is
// attached do we fall back to guessing, and even then a bare list of serial
// ports is the wrong answer — on a laptop it is mostly Bluetooth audio.
func devicePortFor(rt *Runtime) string {
	if t := rt.MeshTarget(); t != "" {
		if dev, ok := strings.CutPrefix(t, "serial:"); ok {
			return dev
		}
		// A TCP radio has no serial device; the CLI takes --host instead, and
		// a made-up /dev path would be worse than none.
		return ""
	}
	if ports, err := meshtastic.ListSerialPorts(); err == nil {
		if dev, ok := likelyRadioPort(ports); ok {
			return dev
		}
	}
	return ""
}

// likelyRadioPort picks a plausible USB serial device out of the OS list.
// Bluetooth audio devices show up there too and must never be suggested.
func likelyRadioPort(ports []string) (string, bool) {
	for _, p := range ports {
		low := strings.ToLower(p)
		if strings.Contains(low, "bluetooth") || strings.Contains(low, "debug-console") {
			continue
		}
		// Prefer the callout device on macOS; on Linux these are ttyUSB/ttyACM.
		if strings.Contains(low, "usbmodem") || strings.Contains(low, "usbserial") ||
			strings.Contains(low, "ttyusb") || strings.Contains(low, "ttyacm") ||
			strings.Contains(low, "wchusb") || strings.Contains(low, "slab_usb") {
			if strings.HasPrefix(p, "/dev/tty.") {
				continue // macOS: the cu.* twin is the one to use
			}
			return p, true
		}
	}
	return "", false
}

type applyResp struct {
	Applied  bool     `json:"applied"`
	Verified bool     `json:"verified"`
	Channel  string   `json:"channel"`
	Index    int      `json:"index"`
	Steps    []string `json:"steps"`
	Report   []string `json:"report"`
	Profile  string   `json:"profile"`
	Note     string   `json:"note"`
}

// handleApplyRadio is the one-button path: write the segment channel to the
// attached radio, reboot it, wait for it to come back, then RE-READ and check
// that what landed is what was asked for.
//
// "Applied" and "verified" are two separate answers on purpose. An
// acknowledged write is not a verified one — our protobuf subset is
// hand-rolled and firmware differs — so the button never claims success on
// the strength of having sent something.
func (a *APIServer) handleApplyRadio(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Name string `json:"name"`
	}](r)
	if err != nil || strings.TrimSpace(body.Name) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("a channel name is required"))
		return
	}

	cfg := a.rt.MeshConfig()
	if cfg.LoRa == nil {
		httpErr(w, http.StatusConflict, errors.New(
			"this node has not reported its configuration, so nothing can be "+
				"written to it safely. Attach a radio first."))
		return
	}
	slot, ok := meshtastic.FreeChannelSlot(cfg)
	if !ok {
		httpErr(w, http.StatusConflict, errors.New(
			"all eight channel slots are in use. Free one first — nothing here "+
				"overwrites a channel you configured."))
		return
	}
	ch, err := meshtastic.MintSegmentChannel(body.Name,
		cfg.LoRa.Region, cfg.LoRa.ModemPreset, cfg.LoRa.HopLimit)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	// The radio's own region and preset are already what the segment will
	// use, so there is nothing to change about them — write only the channel.
	plan, err := meshtastic.PlanSegmentApply(ch, slot, false)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}

	resp := applyResp{Channel: ch.Name, Index: slot, Steps: plan.Steps()}
	// Captured BEFORE the write: the reboot has to be observed as a full
	// away-and-back cycle, not as "connected", which is still true for the
	// couple of seconds the device takes to actually reset.
	cyclesBefore := a.rt.Mesh().Reconnects
	if err := a.rt.ApplyRadioPlan(plan); err != nil {
		httpErr(w, http.StatusBadGateway, err)
		return
	}
	resp.Applied = true

	// The radio is rebooting. The supervised link brings it back on its own.
	if !a.rt.WaitForRadioCycle(cyclesBefore, 60*time.Second) {
		resp.Note = "The settings were written and the radio was asked to " +
			"reboot, but it has not come back yet. Nothing is confirmed until " +
			"it does — wait, or power-cycle it, then press refresh."
		writeJSON(w, resp)
		return
	}

	// Read-after-write: what does the radio itself say now?
	after := a.rt.MeshConfig()
	prof := ch.Profile(slot)
	verdict := prof.Check(after)
	for _, c := range verdict {
		switch c.Status {
		case meshtastic.CheckOK:
			resp.Report = append(resp.Report, "ok · "+c.Field+" · "+c.Got)
		case meshtastic.CheckMismatch:
			resp.Report = append(resp.Report,
				"WRONG · "+c.Field+" · is "+c.Got+", asked for "+c.Want)
		default:
			resp.Report = append(resp.Report, "? · "+c.Field+" · not reported")
		}
	}
	resp.Verified = !verdict.Failed() && !verdict.Incomplete()
	resp.Profile = prof.Format()

	if resp.Verified {
		// Only now is it safe to point this node at the new channel.
		a.rt.SetRadioProfile(&prof)
		a.rt.SetMeshChannel(uint32(slot))
		resp.Note = "The radio reports exactly what was asked for. This node " +
			"now transmits on channel " + itoa(slot) + ". Send the profile to " +
			"whoever else is joining, and prepare their radio the same way."
	} else {
		resp.Note = "The radio came back but does not report what was asked " +
			"for. Nothing on this node has been pointed at the new channel. " +
			"The report above says which field differs."
	}
	writeJSON(w, resp)
}

type scanPort struct {
	Port     string `json:"port"`
	Kind     string `json:"kind"`
	Detail   string `json:"detail"`
	NodeNum  string `json:"nodeNum,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	Region   string `json:"region,omitempty"`
	Preset   string `json:"preset,omitempty"`
	Channels int    `json:"channels,omitempty"`
	Key      string `json:"primaryKey,omitempty"`
}

type scanResp struct {
	Ports    []scanPort `json:"ports"`
	Found    int        `json:"found"`
	Attached string     `json:"attached,omitempty"`
	Note     string     `json:"note"`
}

// handleScanRadios probes the serial ports and reports what is on each.
//
// Device paths are not stable — a T3-S3 that enumerates by its MAC one minute
// comes back by USB location after a reset — so asking a person to type one
// is asking them to track something that changes underneath them.
//
// Every port is reported, including the ones deliberately not opened, with
// the reason. "Why didn't it try that one" should always have an answer.
func (a *APIServer) handleScanRadios(w http.ResponseWriter, r *http.Request) {
	// A radio already attached holds its port open, so a probe would find it
	// busy and say something confusing. Report it as attached instead.
	attached := a.rt.MeshTarget()
	attachedDev := strings.TrimPrefix(attached, "serial:")

	probes := meshtastic.ScanSerial(4 * time.Second)
	resp := scanResp{Ports: []scanPort{}}
	for _, p := range probes {
		sp := scanPort{
			Port: p.Port, Kind: string(p.Kind), Detail: p.Detail,
			Firmware: p.Firmware, Region: p.Region, Preset: p.Preset,
			Channels: p.Channels, Key: p.PrimaryKey,
		}
		if p.NodeNum != 0 {
			sp.NodeNum = itoa(int(p.NodeNum))
		}
		if p.Port == attachedDev {
			sp.Kind = "attached"
			sp.Detail = "this is the radio this node is using"
		}
		if sp.Kind == "radio" {
			resp.Found++
		}
		resp.Ports = append(resp.Ports, sp)
	}

	switch {
	case attachedDev != "":
		resp.Attached = attachedDev
		resp.Note = "A radio is already attached on " + attachedDev + "."
	case resp.Found == 1:
		port, _ := meshtastic.FirstRadio(probes)
		resp.Attached = port
		resp.Note = "Found one Meshtastic node. Attach it to start using the mesh."
	case resp.Found > 1:
		resp.Note = "Found more than one Meshtastic node. Pick the one you mean."
	case len(resp.Ports) == 0:
		resp.Note = "No serial ports at all. Plug a Meshtastic node in over USB."
	default:
		resp.Note = "No Meshtastic node answered. If the device is plugged in, " +
			"close anything else that might be holding its port — the Meshtastic " +
			"app, a serial monitor — and scan again. A device that has just been " +
			"reset can take a few seconds to come up."
	}
	writeJSON(w, resp)
}

// handleAttachRadio attaches a port found by the scan.
func (a *APIServer) handleAttachRadio(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Port string `json:"port"`
	}](r)
	if err != nil || strings.TrimSpace(body.Port) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("port required"))
		return
	}
	target := body.Port
	if !strings.HasPrefix(target, "serial:") && !strings.HasPrefix(target, "tcp:") {
		target = "serial:" + target
	}
	if err := a.rt.StartMeshtastic(target); err != nil {
		httpErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "attached": target})
}
