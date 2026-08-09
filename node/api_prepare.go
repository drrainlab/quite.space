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
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/meshtastic"
	"github.com/drrainlab/quiet_places/transports/rnode"
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
	plan, err := meshtastic.PlanSegmentApply(cfg, ch, slot, false)
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

	// Two different questions, and merging them is how a report lies.
	//
	// The first: did the write disturb anything it was not supposed to?
	// set_config replaces a whole sub-message, so a channel write that
	// accidentally carried a LoRa message would silently reset the region,
	// the slot and the transmitter. Verify compares the radio's own report
	// against the exact bytes the plan intended to leave behind.
	if err := plan.Verify(after); err != nil {
		resp.Report = append(resp.Report, "WRONG · "+err.Error())
		resp.Note = "The radio came back holding something the write did not " +
			"ask for. Nothing on this node has been pointed at the new channel."
		writeJSON(w, resp)
		return
	}

	// The second: does this radio now MEET the segment — which includes
	// settings this write never touched. A failure here is not a failed
	// write; it is a radio that cannot talk to its peers yet.
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
		resp.Note = "The channel was written, but this radio does not yet meet " +
			"the segment. Nothing on this node has been pointed at the new " +
			"channel. The report above says which setting differs — a radio " +
			"whose transmitter is off, or whose preset is unset, cannot reach " +
			"a peer however right its channel is."
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
	// A port that answered in somebody else's protocol gets a SECOND question,
	// and it is asked HERE rather than inside the Meshtastic package.
	//
	// One carrier package importing another would tie two drivers together
	// that the whole seam exists to keep apart — and the node is the layer
	// that already knows about both, because choosing between them is its job.
	// The question itself sets nothing: DETECT is one of the two commands in
	// the RNode protocol with an ask form, so a port belonging to hardware
	// that is none of our business is not touched by being asked.
	rnodes := detectRNodes(probes)

	resp := scanResp{Ports: []scanPort{}}
	candidates, foreign, modems := 0, 0, 0
	for _, p := range probes {
		sp := scanPort{
			Port: p.Port, Kind: string(p.Kind), Detail: p.Detail,
			Firmware: p.Firmware, Region: p.Region, Preset: p.Preset,
			Channels: p.Channels, Key: p.PrimaryKey,
		}
		if rnodes[p.Port] {
			sp.Kind = "rnode"
			sp.Detail = "an RNode modem. Attach it with the phrase your segment " +
				"shares — everyone on it derives the same key from those words"
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
		// candidates are the ports actually tried; foreign are the ones that
		// answered in somebody else's protocol. The pair is what lets the
		// summary below stop guessing.
		if sp.Kind != "skipped" {
			candidates++
			switch sp.Kind {
			case "foreign":
				foreign++
			case "rnode":
				modems++
			}
		}
		resp.Ports = append(resp.Ports, sp)
	}

	// A HOST THAT CAN SEE HARDWARE THE NODE CANNOT. On Android there are no
	// serial ports to probe — the loop above finds nothing and always will —
	// so the devices come from the app's own USB service instead. They are
	// appended rather than substituted: a build could have both, and the
	// interface should not have to know which platform it is on.
	hostRadios, hostErr := a.rt.HostRadios()
	for _, hr := range hostRadios {
		sp := scanPort{Port: HostDevicePrefix + hr.Name, Detail: hr.Why}
		if hr.Supported {
			sp.Kind = "rnode"
			modems++
			candidates++
		} else {
			sp.Kind = "foreign"
			foreign++
			candidates++
		}
		if hr.Label != "" {
			sp.Detail = hr.Label + " — " + hr.Why
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
		resp.Note = "Found one Meshtastic node. It is the other carrier this " +
			"build speaks; an RNode modem is the one it drives directly."
	case resp.Found > 1:
		resp.Note = "Found more than one Meshtastic node. Pick the one you mean."
	case hostErr != nil:
		resp.Note = "The system would not list USB devices: " + hostErr.Error()
	case len(resp.Ports) == 0 && a.rt.HasRadioHost():
		// The honest sentence for a phone. There is no such thing as a serial
		// port here, so offering to look for one again is advice that cannot
		// come true.
		resp.Note = "Nothing is plugged in. Connect an RNode modem with a " +
			"USB-OTG cable — this device reaches radios through USB, not " +
			"through serial ports, and it will ask before using one."
	case len(resp.Ports) == 0:
		resp.Note = "No serial ports at all. Plug a radio in over USB — an " +
			"RNode modem is what this build drives directly."
	case modems > 0:
		// Naming what is there beats explaining what is not. This is the
		// common case on a desk with RNode boards on it, and until this scan
		// could say the word the only answer the screen had was a shrug.
		what := "an RNode modem"
		if modems > 1 {
			what = "RNode modems"
		}
		resp.Note = "Found " + what + ". Attach one with the phrase your " +
			"segment shares — every radio on a segment derives the same key " +
			"from those words, so it has to be the same phrase everywhere."
	case foreign > 0 && foreign == candidates:
		// Do not offer the port-holding advice when we know exactly what is on
		// the port. Every candidate answered, and none of them in this
		// protocol — telling somebody to close the Meshtastic app would send
		// them looking for a problem they do not have.
		resp.Note = "Something is on every port, and none of it is Meshtastic — " +
			"most likely another firmware. An RNode modem is attached at " +
			"startup with --rnode and its own segment seed, not from this screen."
	default:
		resp.Note = "Nothing answered on any port — no RNode modem and no " +
			"Meshtastic node. If a radio is plugged in, close anything else " +
			"that might be holding its port (the Meshtastic app, a serial " +
			"monitor) and scan again. A board that has just been reset can " +
			"take a few seconds to come up."
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

// ---- meeting over the radio (MR-1) ----
//
// Four routes, because a meeting on a radio segment has four moments: say who
// I am, see who is here, offer somebody a way in, and answer an offer. None
// of them touches a relay or the internet, which is the whole point — a LoRa
// segment in a field has neither.

func (a *APIServer) handleRadioAnnounce(w http.ResponseWriter, r *http.Request) {
	if err := a.rt.AnnounceOnRadio(); err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"announced": a.rt.DisplayName(),
		"note": "everyone within radio range now knows this device exists and " +
			"what it is called. That is what somebody needs in order to seal an " +
			"invitation to it."})
}

func (a *APIServer) handleRadioNeighbours(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"neighbours": a.rt.RadioNeighbours()})
}

func (a *APIServer) handleRadioInvite(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Space  string `json:"space"`
		Device string `json:"device"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	tid, err := id.ParseTerminalID(body.Space)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	dev, err := id.ParseDeviceID(body.Device)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.rt.InviteOverRadio(tid, dev); err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"offered": body.Device,
		"note": "the invitation is sealed to that device's key, so it is safe " +
			"on the air: everyone in range hears bytes only they can open."})
}

func (a *APIServer) handleRadioOffers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"invitations": a.rt.RadioOffers()})
}

func (a *APIServer) handleRadioAccept(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		ID string `json:"id"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	// Answering is now an ANSWER, not a join: it tells the other side we want
	// in, and the space arrives when they grant it. Reporting a join here
	// would be the same overstatement as reporting handed-to-transport as
	// delivery — the very thing this wave keeps removing.
	if err := a.rt.AcceptRadioLine(body.ID); err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{
		"state": "accepted",
		"note": "your answer is on the air. They grant the space when it " +
			"reaches them, and it appears here then — not before."})
}

// handleRadioMeet opens a line with a neighbour and offers it over the air.
func (a *APIServer) handleRadioMeet(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Device string `json:"device"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	dev, err := id.ParseDeviceID(body.Device)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	tid, err := a.rt.OfferLineOverRadio(dev)
	if errors.Is(err, ErrLinkNotReady) {
		// 202: accepted, not done. The probe is on the air and the answer
		// decides whether an invitation is worth sending at all.
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, map[string]string{"state": "probing", "note": err.Error()})
		return
	}
	if err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, meetResponse(tid))
}

// meetResponse is the answer to "start a line", and it exists as a function so
// the id in it can be tested.
//
// It used to send tid.String(), which is the DISPLAY form: "terminal:" plus a
// TRUNCATED hex. The client stores what it is given as the current space and
// asks for that space's entries, palette and appearance — so every one of
// those came back 400, and the console filled with "encoding/hex: invalid
// byte: U+0074 't'", which is the 't' of "terminal:". Pressing "start a line"
// appeared to break the whole screen.
//
// Hex is what every other endpoint sends and what ParseTerminalID reads.
// String() is for a person to look at.
func meetResponse(tid id.TerminalID) map[string]string {
	return map[string]string{"space": tid.Hex(),
		"note": "a new line, for the two of you. They have been OFFERED it — " +
			"nobody is a member and no key has moved until they answer."}
}

// detectRNodes asks every port that answered in a foreign protocol whether it
// is an RNode modem.
//
// Only foreign ports are asked, and the restraint is the point: a Meshtastic
// node already identified itself, a busy port cannot be opened without taking
// it from whoever holds it, and a skipped port was skipped for a reason. That
// leaves exactly the ports where the question is both unanswered and cheap.
func detectRNodes(probes []meshtastic.PortProbe) map[string]bool {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out = map[string]bool{}
	)
	for _, p := range probes {
		if p.Kind != meshtastic.ProbeForeign {
			continue
		}
		wg.Add(1)
		go func(port string) {
			defer wg.Done()
			// An error is not a verdict: the port may have been taken between
			// the two questions. Silence and failure both mean "not named",
			// never "not a radio".
			if ok, err := rnode.Detect(port, 900*time.Millisecond); err == nil && ok {
				mu.Lock()
				out[port] = true
				mu.Unlock()
			}
		}(p.Port)
	}
	wg.Wait()
	return out
}
