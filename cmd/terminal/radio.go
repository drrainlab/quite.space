// `terminal radio …` — finding a board and putting Meshtastic on it (RB-2).
//
// The shape of this command follows from one fact about the hardware: a
// board that is not yet running Meshtastic can tell us its CHIP FAMILY and
// nothing else. Dozens of different LoRa boards share one chip, with
// different radio parts and different pin maps, and flashing the wrong
// variant leaves a device that is silent or damaged.
//
// So the flow is: detect what can be detected, SHOW it as evidence, narrow
// the list of variants to those the chip permits, and then ask. There is no
// default selection and no --yes flag that skips the question. Auto-detecting
// a port is helping; auto-choosing a board would be guessing with somebody
// else's hardware.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

// stdinReader is shared by every prompt in this file, and must be.
//
// A bufio.Reader reads AHEAD: it pulls whatever is available into its buffer,
// not just the line it was asked for. So a second reader built over os.Stdin
// starts at whatever the first one did not consume — which is nothing, the
// bytes are in the first one's buffer. With a person typing, each answer
// arrives after its prompt and the loss is invisible; with input piped in,
// every answer after the first disappears and the command reports a refusal
// nobody made.
var stdinReader = bufio.NewReader(os.Stdin)

func runRadio(args []string) error {
	if len(args) < 1 {
		return errors.New(`usage:
  terminal radio list [--identify]       what is on each serial port
                                         (--identify also RESETS silent ports
                                          to read the chip off their boot ROM)
  terminal radio flash [--port PATH] [--release TAG]
                                         install Meshtastic on a board
  terminal radio region [NAME]           set the region (no name = show the choices)
                                         [--preset NAME] [--hop N] [--port PATH]
                                         [--slot N]  pick the frequency slot rather
                                            than letting the primary channel's name
                                            hash choose it — how a segment leaves a
                                            crowded frequency without moving
                                         [--quiet-neighbours]  stop repeating other
                                            meshes' traffic (rebroadcast LOCAL_ONLY)

  terminal radio channel <name> --region NAME [--preset N] [--hop N]
                [--port PATH …]          mint ONE shared channel and write it,
                                         with matching LoRa settings, to every
                                         radio named

Flashing ERASES the board. It needs esptool installed.
A radio transmits nothing until its region is set.`)
	}
	switch args[0] {
	case "list":
		return radioList(args[1:])
	case "flash":
		return radioFlash(args[1:])
	case "region":
		return radioRegion(args[1:])
	case "channel":
		return radioChannel(args[1:])
	default:
		return fmt.Errorf("unknown: terminal radio %s", args[0])
	}
}

// radioCandidate is one port plus everything we managed to learn about it.
type radioCandidate struct {
	probe meshtastic.PortProbe
	boot  meshtastic.BootROM
	// bootErr is why the boot probe learned nothing, when one was asked for.
	// Somebody who typed --identify has agreed to reset a board in exchange
	// for an answer; giving them back the same line they had before the reset
	// spends the reset for nothing.
	bootErr error
	probed  bool
}

// findCandidates scans the ports, optionally pulsing reset on the silent ones
// to read their boot ROM banner.
//
// THE BOOT PROBE IS OFF BY DEFAULT, and the reason is a defect this command
// shipped with. The reset was gated on "the port reported itself silent",
// which was assumed to mean "no Meshtastic node whose traffic could be
// interrupted". It does not: the handshake produces false negatives on
// native-USB boards, and every one of them turned into a listing that
// REBOOTED a working radio. A command that reports what is attached must not
// be able to interrupt what is attached.
func findCandidates(withBootProbe bool) []radioCandidate {
	return findCandidatesOn("", withBootProbe)
}

// findCandidatesOn narrows the scan to one port when asked.
//
// This matters only for the boot probe, and it matters a lot: identifying a
// chip requires RESETTING it, and a bench usually holds one dead board and
// one working one. Without a way to name the port, the only diagnostic for
// the dead board reboots the good one too — which is how a debugging session
// ends up with two broken radios instead of one.
func findCandidatesOn(only string, withBootProbe bool) []radioCandidate {
	var out []radioCandidate
	for _, p := range meshtastic.ScanSerial(1500 * time.Millisecond) {
		if only != "" && p.Port != only {
			continue
		}
		c := radioCandidate{probe: p}
		if withBootProbe && p.Kind == meshtastic.ProbeSilent {
			c.probed = true
			if b, err := meshtastic.ProbeBootROM(p.Port, 3*time.Second); err == nil {
				c.boot = b
			} else {
				c.bootErr = err
			}
		}
		out = append(out, c)
	}
	return out
}

func (c radioCandidate) describe() string {
	switch c.probe.Kind {
	case meshtastic.ProbeRadio:
		bits := []string{"Meshtastic node " + strconv.FormatUint(uint64(c.probe.NodeNum), 10)}
		if c.probe.Firmware != "" {
			bits = append(bits, "firmware "+c.probe.Firmware)
		}
		if c.probe.Region != "" {
			bits = append(bits, "region "+c.probe.Region)
		}
		if c.probe.Preset != "" {
			bits = append(bits, c.probe.Preset)
		}
		return strings.Join(bits, " · ")
	case meshtastic.ProbeBusy:
		return "busy — another program holds this port"
	case meshtastic.ProbeSilent:
		// "did not answer" is the honest wording. It is not the same claim as
		// "there is no radio here": the handshake has false negatives, and
		// this command must not send somebody off to reflash a working board.
		if c.boot.Chip != "" || c.boot.Spoke() {
			return "did not answer as Meshtastic · " + describeBoot(c.boot)
		}
		if c.bootErr != nil {
			return "did not answer as Meshtastic · and did not answer a reset " +
				"either: " + c.bootErr.Error()
		}
		if c.probed {
			return "did not answer as Meshtastic · said nothing on reset either"
		}
		return "did not answer as Meshtastic"
	default:
		return c.probe.Detail
	}
}

// describeBoot renders whatever the boot ROM said, as evidence.
func describeBoot(b meshtastic.BootROM) string {
	if b.Chip != "" {
		return "chip " + b.Chip + " (from its boot banner)"
	}
	if b.Spoke() {
		return "said something on reset, but not a chip banner"
	}
	return "said nothing on reset"
}

func radioList(args []string) error {
	identify, only := false, ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--identify":
			identify = true
		case "--port":
			if i+1 < len(args) {
				only = args[i+1]
				i++
			}
		}
	}
	if only != "" {
		fmt.Printf("looking at %s…\n", only)
	} else {
		fmt.Println("looking at every serial port…")
	}
	cands := findCandidatesOn(only, identify)
	if len(cands) == 0 {
		if only != "" {
			fmt.Printf("no serial port called %s — check the name with "+
				"`terminal radio list`\n", only)
			return nil
		}
		fmt.Println("no serial ports at all — is the board plugged in?")
		return nil
	}
	for _, c := range cands {
		fmt.Printf("  %-32s %s\n", c.probe.Port, c.describe())
	}
	if !identify {
		fmt.Println("\nA port with no Meshtastic on it can also name its chip, but only\n" +
			"by being reset: `terminal radio list --identify --port /dev/…`. That\n" +
			"REBOOTS whatever is on the port, so it is not what a plain listing does —\n" +
			"and name the port, or every other radio on the bench is reset with it.")
	}
	return nil
}

func radioFlash(args []string) error {
	port, release, variant := "", "", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port":
			if i+1 < len(args) {
				port = args[i+1]
				i++
			}
		case "--release":
			if i+1 < len(args) {
				release = args[i+1]
				i++
			}
		case "--variant":
			if i+1 < len(args) {
				variant = args[i+1]
				i++
			}
		}
	}

	// esptool first: discovering it is missing AFTER a 170 MB metadata dance
	// and a set of questions would waste the person's time for nothing.
	tool, err := meshtastic.FindEsptool()
	if err != nil {
		return err
	}

	var chip string
	if port == "" {
		fmt.Println("looking at every serial port…")
		cands := findCandidates(false)
		var pick []radioCandidate
		for _, c := range cands {
			if c.probe.Kind == meshtastic.ProbeSilent || c.probe.Kind == meshtastic.ProbeRadio {
				pick = append(pick, c)
			}
		}
		if len(pick) == 0 {
			for _, c := range cands {
				fmt.Printf("  %-32s %s\n", c.probe.Port, c.describe())
			}
			return errors.New("no board to flash. A port shown as busy is held by " +
				"another program — close the Meshtastic app or a running node and try again")
		}
		fmt.Println()
		for i, c := range pick {
			fmt.Printf("  [%d] %-30s %s\n", i+1, c.probe.Port, c.describe())
		}
		fmt.Println()
		n, err := askNumber("Which device? ", len(pick))
		if err != nil {
			return err
		}
		port = pick[n-1].probe.Port
		// Identify the chip only NOW, on the one device the person chose.
		// This resets it, which is acceptable for a board about to be erased
		// and was never acceptable for the others in the list.
		if pick[n-1].probe.Kind == meshtastic.ProbeSilent {
			if b, err := meshtastic.ProbeBootROM(port, 3*time.Second); err == nil {
				chip = b.Chip
				fmt.Printf("  %s\n", describeBoot(b))
			}
		}
		if pick[n-1].probe.Kind == meshtastic.ProbeRadio {
			fmt.Println("\nNote: this device is ALREADY running Meshtastic. Flashing " +
				"replaces it and erases its settings, channels and keys.")
			if !askYes("Continue anyway?") {
				return errors.New("stopped")
			}
		}
	}

	var rel meshtastic.FirmwareRelease
	if release != "" {
		fmt.Printf("\nusing the release you named: %s\n", release)
		rel, err = meshtastic.ReleaseByTag(nil, release)
	} else {
		fmt.Println("\nasking Meshtastic which firmware is current…")
		rel, err = meshtastic.LatestRelease(nil)
	}
	if err != nil {
		return err
	}
	fmt.Printf("release: %s\n", rel.Version)

	targets := rel.TargetsForChip(chip)
	if chip != "" {
		fmt.Printf("the board's boot banner says %s, so %d of the %d variants "+
			"in this release can run on it.\n", chip, len(targets), len(rel.Targets))
	} else {
		fmt.Printf("the board did not say which chip it has, so all %d variants "+
			"are listed. Picking one that does not match will not work.\n", len(targets))
	}
	var target meshtastic.FirmwareTarget
	if variant != "" {
		// Named outright. This is how a flash gets repeated after a failure
		// that had nothing to do with the choice — the first attempt on this
		// board died at "could not connect", and re-answering a filter every
		// time invites picking a different board by accident.
		found := false
		for _, t := range targets {
			if strings.EqualFold(t.Board, variant) {
				target, found = t, true
				break
			}
		}
		if !found {
			return fmt.Errorf("this release has no variant called %q for that "+
				"chip. Run without --variant to see the list", variant)
		}
		fmt.Printf("\nusing the variant you named: %s\n", target.Board)
	} else {
		fmt.Println("\nThe chip does NOT identify the board — many boards share one chip.")
		fmt.Println("Pick the model printed on YOUR board. Type part of the name to filter.")
		var err error
		target, err = chooseTarget(targets)
		if err != nil {
			return err
		}
	}

	fmt.Printf("\nreading the install recipe for %s…\n", target.Board)
	plan, err := rel.PlanFor(nil, target.Board, target.MCU)
	if err != nil {
		return err
	}

	fmt.Printf("\n  board    %s (%s)\n  version  %s\n  port     %s\n",
		plan.Board, plan.MCU, plan.Version, port)
	fmt.Println("  writes:")
	for _, f := range plan.Files {
		fmt.Printf("    0x%-8x %-52s %6.2f MB\n", f.Offset, f.Name, float64(f.Bytes)/(1<<20))
	}
	fmt.Printf("  %.2f MB downloaded from the Meshtastic release, each file checked\n"+
		"  against the MD5 the release publishes before anything is written.\n",
		float64(plan.TotalBytes())/(1<<20))
	fmt.Println("\n⚠  The board is ERASED first. Anything on it now is gone.")
	if !askYes("Flash it?") {
		return errors.New("stopped — nothing was written")
	}

	dir, err := os.MkdirTemp("", "qp-firmware-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	paths := map[string]string{}
	fmt.Println("\ndownloading…")
	err = plan.Download(nil, func(name string, data []byte) error {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return err
		}
		paths[name] = p
		fmt.Printf("  %s ✓ checksum matches\n", name)
		return nil
	})
	if err != nil {
		return err
	}

	fmt.Println("\nflashing — this takes a few minutes, do not unplug the board:")
	if err := meshtastic.Flash(context.Background(), tool, port, plan, paths, func(l string) {
		fmt.Println(" ", l)
	}); err != nil {
		return err
	}

	// Read-after-write, the same rule the config writer follows: nothing is
	// reported as done until the device itself says so.
	// Watch EVERY port, not the one just written to. A board flashed through
	// its ROM bootloader comes back as a different USB device — the running
	// firmware makes its own — so the port that was flashed is frequently not
	// the port the node appears on. Waiting only on the flashed one reports a
	// failure for a board that worked.
	fmt.Println("\nflashed. Waiting for the board to come up and answer as a Meshtastic node…")
	before := map[string]bool{}
	for _, c := range findCandidates(false) {
		before[c.probe.Port] = true
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		for _, c := range findCandidates(false) {
			if c.probe.Kind != meshtastic.ProbeRadio {
				continue
			}
			if c.probe.Port != port && before[c.probe.Port] {
				continue // somebody else's radio, already there before this
			}
			fmt.Printf("\n✓ %s is now a Meshtastic node (%s)\n", c.probe.Port, c.describe())
			if c.probe.Port != port {
				fmt.Printf("  (it came back on a different port — it was flashed "+
					"through %s)\n", port)
			}
			fmt.Println("\nNext: set the region — the board will not transmit until you do.")
			fmt.Println("  docs/RADIO_SETUP.md, шаг 2")
			return nil
		}
	}
	// The old sentence here guessed "the variant may be wrong", which is the
	// LEAST likely of the three and the most expensive to act on — it sends
	// somebody to erase the board again with a different guess. The first
	// cause is mechanical, and it is the one that has actually happened:
	// a board whose BOOT pin is still held cannot start what was just
	// written to it, however correct the firmware is.
	return errors.New("the board was flashed and verified, but it has not started.\n\n" +
		"Most likely it is STILL HELD IN DOWNLOAD MODE. A BOOT jumper left in\n" +
		"place, or a BOOT button still pressed, keeps the chip in its ROM\n" +
		"bootloader — where it cannot run the firmware it now holds. Move the\n" +
		"jumper back (or just power-cycle with nothing held) and run\n" +
		"`terminal radio list`.\n\n" +
		"Failing that: unplug it, plug it back in, and look again. Only if it\n" +
		"still says nothing is the variant worth questioning — and re-flashing\n" +
		"with a different one erases the board a second time, so it is the last\n" +
		"thing to try, not the first")
}

// chooseTarget asks which board this is. There is deliberately no default:
// the answer cannot be derived from anything we measured.
func chooseTarget(targets []meshtastic.FirmwareTarget) (meshtastic.FirmwareTarget, error) {
	if len(targets) == 0 {
		return meshtastic.FirmwareTarget{}, errors.New("this release has no firmware for that chip")
	}
	in := stdinReader
	shown := targets
	for {
		fmt.Println()
		for i, t := range shown {
			fmt.Printf("  [%2d] %s\n", i+1, t.Board)
		}
		fmt.Print("\nNumber to choose, or text to filter (empty = show all): ")
		line, err := in.ReadString('\n')
		if err != nil {
			return meshtastic.FirmwareTarget{}, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			shown = targets
			continue
		}
		if n, err := strconv.Atoi(line); err == nil {
			if n >= 1 && n <= len(shown) {
				return shown[n-1], nil
			}
			fmt.Printf("there is no [%d] in that list.\n", n)
			continue
		}
		var filtered []meshtastic.FirmwareTarget
		for _, t := range targets {
			if strings.Contains(strings.ToLower(t.Board), strings.ToLower(line)) {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			fmt.Printf("nothing matches %q — showing everything again.\n", line)
			shown = targets
			continue
		}
		shown = filtered
	}
}

func askNumber(prompt string, max int) (int, error) {
	in := stdinReader
	for {
		fmt.Print(prompt)
		line, err := in.ReadString('\n')
		if err != nil {
			return 0, err
		}
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && n >= 1 && n <= max {
			return n, nil
		}
		fmt.Printf("a number from 1 to %d, please.\n", max)
	}
}

func askYes(prompt string) bool {
	in := stdinReader
	fmt.Print(prompt + " type yes to continue: ")
	line, err := in.ReadString('\n')
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(line), "yes")
}

// txWord says whether the radio may transmit, in the loudest available way.
// "off" is not a detail: a receiving-only radio is indistinguishable from a
// radio with nobody in range, and that ambiguity cost nine days.
func txWord(on bool) string {
	if on {
		return "on"
	}
	return "OFF — this radio cannot send"
}

// radioRegion sets the LoRa configuration on an attached radio.
//
// The region is not a preference. It decides which frequencies the device
// transmits on, which is a question of the law where the person is standing
// and of the band their antenna was built for — neither of which this program
// can observe. So it is never defaulted, never guessed from a locale, and a
// name this build does not recognise is an error rather than a zero value
// (see ParseRegion: writing UNSET by accident stops the radio transmitting
// altogether, which is the worst possible way to mistype something).
func radioRegion(args []string) error {
	var port, region, preset, hop, slot, tx string
	quiet := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--tx":
			if i+1 < len(args) {
				tx = args[i+1]
				i++
			}
		case "--slot":
			if i+1 < len(args) {
				slot = args[i+1]
				i++
			}
		case "--quiet-neighbours":
			quiet = true
		case "--port":
			if i+1 < len(args) {
				port = args[i+1]
				i++
			}
		case "--preset":
			if i+1 < len(args) {
				preset = args[i+1]
				i++
			}
		case "--hop":
			if i+1 < len(args) {
				hop = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "--") && region == "" {
				region = args[i]
			}
		}
	}

	if port == "" {
		var err error
		port, err = pickRadioPort()
		if err != nil {
			return err
		}
	}

	// Read first: a person about to change the air their device works on
	// should see what it is set to now, from the device itself.
	radio, err := openRadio(port)
	if err != nil {
		return fmt.Errorf("could not read the radio on %s: %w", port, err)
	}
	cfg := radio.Config()
	radio.Close()
	fmt.Printf("\n%s says it is set to:\n", port)
	if cfg.LoRa != nil {
		fmt.Printf("  region        %s\n  modem preset  %s\n  hop limit     %d\n"+
			"  transmitter   %s\n",
			cfg.LoRa.RegionName(), cfg.LoRa.PresetName(), cfg.LoRa.HopLimit,
			txWord(cfg.LoRa.TxEnabled))
	} else {
		fmt.Println("  (it did not report its LoRa settings — older firmware)")
	}
	if cfg.Device != nil {
		fmt.Printf("  rebroadcast   %s\n", cfg.Device.RebroadcastName())
	}
	if cfg.LoRa != nil && !cfg.LoRa.TxEnabled {
		fmt.Println("\n⚠  This radio's transmitter is OFF. It hears everything and " +
			"sends\n   nothing, which looks exactly like being out of range.")
		fmt.Println("   Earlier versions of this command switched it off as a side " +
			"effect\n   of setting the region. Turn it back on with:  --tx on")
	}

	if region == "" && tx == "" && preset == "" && hop == "" && slot == "" && !quiet {
		fmt.Println("\nRegions this build knows:")
		fmt.Println("  " + strings.Join(meshtastic.RegionNames(), "  "))
		return errors.New("\nname the region you are actually in, e.g. " +
			"`terminal radio region EU_868`.\n" +
			"It must match both the law where you are and the band your antenna\n" +
			"was built for — usually printed on the antenna itself (433 or 868 MHz).\n" +
			"This is not something this program can work out for you.\n" +
			"\nTo change only the transmitter, without touching the region:" +
			"\n  terminal radio region --tx on")
	}

	var set meshtastic.LoRaSetting
	if region != "" {
		r, err := meshtastic.ParseRegion(region)
		if err != nil {
			return err
		}
		set.Region = &r
	}
	switch tx {
	case "":
	case "on":
		v := true
		set.TxEnabled = &v
	case "off":
		v := false
		set.TxEnabled = &v
	default:
		return fmt.Errorf("--tx takes on or off, got %q", tx)
	}
	if slot != "" {
		n, err := strconv.ParseUint(slot, 10, 32)
		if err != nil || n == 0 {
			return fmt.Errorf("--slot must be a frequency slot number from 1 up, got %q", slot)
		}
		v := uint32(n)
		set.ChannelNum = &v
	}
	if quiet {
		v := meshtastic.RebroadcastLocalOnly
		set.Rebroadcast = &v
	}
	if preset != "" {
		p, err := meshtastic.ParsePreset(preset)
		if err != nil {
			return err
		}
		set.ModemPreset = &p
	}
	if hop != "" {
		n, err := strconv.ParseUint(hop, 10, 32)
		if err != nil || n > 7 {
			return fmt.Errorf("--hop must be 0..7, got %q", hop)
		}
		h := uint32(n)
		set.HopLimit = &h
	}

	plan, err := meshtastic.PlanLoRaApply(cfg, set)
	if err != nil {
		return err
	}
	fmt.Println("\nwill write:")
	for _, s := range plan.Summary {
		fmt.Println("  · " + s)
	}
	fmt.Println("\n⚠  Transmitting on the wrong frequencies for where you are can be " +
		"illegal,\n   and transmitting without an antenna attached can damage the board.")
	if !askYes("Write this?") {
		return errors.New("stopped — nothing was written")
	}

	radio, err = openRadio(port)
	if err != nil {
		return err
	}
	// Re-read and re-plan against what the radio holds RIGHT NOW. The patch is
	// only as good as the bytes it starts from, and those came from a session
	// that has since been closed: between the two, the device could have been
	// changed from a phone, or rebooted into something else.
	fresh := radio.Config()
	plan, err = meshtastic.PlanLoRaApply(fresh, set)
	if err != nil {
		radio.Close()
		return err
	}
	err = radio.Apply(plan)
	radio.Close()
	if err != nil {
		return err
	}

	// Read-after-write. Apply deliberately does not verify — the radio is
	// rebooting when it returns — so "what we asked for" and "what actually
	// happened" stay two separate claims, and only the second one is
	// reported as done.
	fmt.Println("\nwritten. Waiting for the radio to reboot and say what it now holds…")
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		back, err := openRadio(port)
		if err != nil {
			continue
		}
		got := back.Config()
		back.Close()
		if got.LoRa == nil {
			continue
		}
		fmt.Printf("\nthe radio now reports:\n  region        %s\n"+
			"  modem preset  %s\n  hop limit     %d\n  transmitter   %s\n",
			got.LoRa.RegionName(), got.LoRa.PresetName(), got.LoRa.HopLimit,
			txWord(got.LoRa.TxEnabled))
		if got.Device != nil {
			fmt.Printf("  rebroadcast   %s\n", got.Device.RebroadcastName())
		}
		// Every field, the ones this write changed and the ones it was obliged
		// to carry across untouched. Checking only the first is how a command
		// printed "✓ region set" while its own writer switched the transmitter
		// off — the asked-for field landed, and it took four others with it.
		if err := plan.Verify(got); err != nil {
			return err
		}
		// A configuration that landed is not a radio that can be heard. Two
		// other settings decide that, and neither is changed by this command —
		// so staying silent about them would report success for a device that
		// cannot reach anybody. Naming them is the difference between "the
		// write worked" and "this radio works".
		var cannot []string
		if !got.LoRa.TxEnabled {
			cannot = append(cannot, "the transmitter is OFF — the radio hears "+
				"everything and sends nothing, which is indistinguishable from "+
				"being out of range. Turn it on: --tx on")
		}
		if !got.LoRa.UsePreset {
			cannot = append(cannot, "the modem preset is OFF — this radio is on "+
				"manual bandwidth/spreading-factor/coding-rate, and a peer set to a "+
				"preset will never hear it. Give it one: --preset LONG_FAST")
		}
		if got.LoRa.HopLimit == 0 {
			cannot = append(cannot, "the hop limit is 0 — nothing relays this "+
				"radio's packets past a direct neighbour. Give it one: --hop 3")
		}
		if len(cannot) > 0 {
			fmt.Println("\n⚠  The write landed, but this radio still cannot match a peer:")
			for _, c := range cannot {
				fmt.Println("   · " + c)
			}
			fmt.Println("\n   Two radios hear each other only when region, preset AND the\n" +
				"   channel key all agree. Where they differ, both transmit happily\n" +
				"   and neither hears anything — with no error on either side.")
			return nil
		}
		// Report what this run actually did. "✓ region set" was printed even
		// when no region was asked for, which is the same class of untruth as
		// the write it used to sit on top of.
		if region != "" {
			fmt.Println("\n✓ region set. Next: give the radio a channel of its own — " +
				"docs/RADIO_SETUP.md, шаг 3")
		} else {
			fmt.Println("\n✓ written, and everything else came back unchanged.")
		}
		return nil
	}
	return errors.New("the radio has not answered since the reboot. Unplug it, " +
		"plug it back in, and run `terminal radio region` with no arguments to " +
		"see what it holds")
}

// pickRadioPort finds attached radios and asks which one, when there is a
// choice to make.
func pickRadioPort() (string, error) {
	fmt.Println("looking for a radio…")
	var live []radioCandidate
	for _, c := range findCandidates(false) {
		if c.probe.Kind == meshtastic.ProbeRadio {
			live = append(live, c)
		}
	}
	switch len(live) {
	case 0:
		return "", errors.New("no Meshtastic radio found. `terminal radio list` " +
			"says what is on each port")
	case 1:
		fmt.Printf("  %s — %s\n", live[0].probe.Port, live[0].describe())
		return live[0].probe.Port, nil
	}
	for i, c := range live {
		fmt.Printf("  [%d] %-30s %s\n", i+1, c.probe.Port, c.describe())
	}
	n, err := askNumber("Which radio? ", len(live))
	if err != nil {
		return "", err
	}
	return live[n-1].probe.Port, nil
}

// radioChannel mints ONE segment channel and puts it on every radio named.
//
// One command for every radio, rather than one run per device, because of
// what a channel key is: two radios are on the same segment only if they hold
// the SAME key, and a key that has to survive between two invocations has to
// be written down somewhere. Minting once and applying to each port in turn
// keeps it in memory for the length of one command and nowhere else. The
// fingerprint is what survives, and a fingerprint identifies a key without
// revealing it.
//
// The LoRa settings ride along in the same batch. A shared channel on radios
// that disagree about region or modem preset is silence with extra steps.
// openRadio opens a radio, asking again when the device merely stayed quiet.
//
// The same distinction the node's attach path draws (SilentHandshake): a
// wrong path or a busy port fails on the spot with its own message, while a
// device that opened and said nothing is worth another try. Without this,
// every one of these commands inherits the native-USB flakiness — measured at
// roughly one answer in three per attempt — and a write that was about to
// succeed reads as a radio that is not there.
func openRadio(port string) (*meshtastic.Radio, error) {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		var radio *meshtastic.Radio
		radio, err = meshtastic.OpenSerial(port)
		if err == nil {
			return radio, nil
		}
		if !meshtastic.SilentHandshake(err) {
			return nil, err
		}
		time.Sleep(700 * time.Millisecond)
	}
	return nil, err
}

func radioChannel(args []string) error {
	var name, region, preset, hop string
	var ports []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port":
			if i+1 < len(args) {
				ports = append(ports, args[i+1])
				i++
			}
		case "--region":
			if i+1 < len(args) {
				region = args[i+1]
				i++
			}
		case "--preset":
			if i+1 < len(args) {
				preset = args[i+1]
				i++
			}
		case "--hop":
			if i+1 < len(args) {
				hop = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "--") && name == "" {
				name = args[i]
			}
		}
	}
	if name == "" || region == "" {
		return errors.New("usage: terminal radio channel <name> --region NAME " +
			"[--preset LONG_FAST] [--hop 3] --port PATH [--port PATH …]\n\n" +
			"Mints one channel with a fresh private key and writes it, plus the\n" +
			"matching LoRa settings, to every radio named. The region is yours to\n" +
			"choose: it decides which frequencies these radios transmit on.")
	}
	if len(ports) == 0 {
		fmt.Println("looking for radios…")
		for _, c := range findCandidates(false) {
			if c.probe.Kind == meshtastic.ProbeRadio {
				ports = append(ports, c.probe.Port)
				fmt.Printf("  %s — %s\n", c.probe.Port, c.describe())
			}
		}
		if len(ports) == 0 {
			return errors.New("no radio answered. `terminal radio list` says what " +
				"is on each port; a busy one is held by a running node")
		}
	}

	reg, err := meshtastic.ParseRegion(region)
	if err != nil {
		return err
	}
	pre := meshtastic.PresetValue("LONG_FAST")
	if preset != "" {
		if pre, err = meshtastic.ParsePreset(preset); err != nil {
			return err
		}
	}
	var hops uint32 = 3
	if hop != "" {
		n, err := strconv.ParseUint(hop, 10, 32)
		if err != nil || n > 7 {
			return fmt.Errorf("--hop must be 0..7, got %q", hop)
		}
		hops = uint32(n)
	}

	ch, err := meshtastic.MintSegmentChannel(name, reg, pre, hops)
	if err != nil {
		return err
	}

	fmt.Printf("\nminted channel %q · fingerprint %s\n", ch.Name, ch.Fingerprint())
	fmt.Printf("will write to %d radio(s):\n", len(ports))
	for _, p := range ports {
		fmt.Printf("  · %s — add the channel, and set region %s, preset %s, hop %d\n",
			p, region, meshtastic.PresetNames()[pre], hops)
	}
	fmt.Println("\nExisting channels are NOT touched — the channel goes in a free slot.")
	fmt.Println("Each radio reboots afterwards and is re-read to check what landed.")
	if !askYes("Write it?") {
		return errors.New("stopped — nothing was written")
	}

	// THE KEY IS SHOWN BEFORE ANYTHING IS WRITTEN, not after everything is.
	//
	// It exists from the moment it is minted, and the first successful write
	// puts it on hardware. Printing it only after every radio succeeded meant
	// that a failure on the SECOND radio destroyed the only copy of a key the
	// FIRST one was already holding — leaving a channel nobody could ever
	// join. Shown here, a partial run is recoverable.
	fmt.Println("\nThis link carries the key. It is shown once and stored nowhere —")
	fmt.Println("treat it like a password, and never send it through the mesh it")
	fmt.Println("unlocks. Any other radio joins this segment with it:")
	fmt.Println("\n  " + ch.URL() + "\n")

	done := 0
	for _, p := range ports {
		if err := applyChannelTo(p, ch); err != nil {
			if done > 0 {
				fmt.Printf("\n%d of %d radios took the channel. The link above still\n"+
					"joins the rest once you have dealt with this:\n", done, len(ports))
			}
			return fmt.Errorf("%s: %w", p, err)
		}
		done++
	}

	fmt.Printf("\n✓ %d radio(s) now share channel %q (fingerprint %s)\n",
		len(ports), ch.Name, ch.Fingerprint())
	return nil
}

// applyChannelTo writes the channel and the LoRa settings to one radio, then
// re-reads the device to confirm what actually landed.
func applyChannelTo(port string, ch *meshtastic.SegmentChannel) error {
	radio, err := openRadio(port)
	if err != nil {
		return err
	}
	cfg := radio.Config()
	slot, ok := meshtastic.FreeChannelSlot(cfg)
	if !ok {
		radio.Close()
		return errors.New("all eight channel slots are in use — free one on the " +
			"radio first; this command never overwrites somebody's channel")
	}
	plan, err := meshtastic.PlanSegmentApply(cfg, ch, slot, true)
	if err != nil {
		radio.Close()
		return err
	}
	fmt.Printf("\n%s — slot %d\n", port, slot)
	for _, s := range plan.Steps() {
		fmt.Println("  · " + s)
	}
	err = radio.Apply(plan)
	radio.Close()
	if err != nil {
		return err
	}

	fmt.Println("  rebooting, then re-reading…")
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		back, err := openRadio(port)
		if err != nil {
			continue
		}
		got := back.Config()
		back.Close()
		info, ok := got.Channel(slot)
		if !ok || got.LoRa == nil {
			continue
		}
		// The fingerprint is the whole point: it says the radio holds the key
		// we minted, without the key ever coming back out of the device.
		if info.KeyFingerprint != ch.Fingerprint() {
			return fmt.Errorf("slot %d came back with fingerprint %q, expected %q "+
				"— the channel did not land", slot, info.KeyFingerprint, ch.Fingerprint())
		}
		if got.LoRa.Region != ch.Region {
			return fmt.Errorf("region came back as %s, expected %s",
				got.LoRa.RegionName(), meshtastic.RegionName(ch.Region))
		}
		// Everything the write was obliged to leave alone, checked too. The
		// region landing says the write reached the device; it says nothing
		// about what the write took with it.
		if err := plan.Verify(got); err != nil {
			return err
		}
		fmt.Printf("  ✓ channel %q at slot %d · region %s · preset %s · hop %d · tx %s\n",
			info.Name, slot, got.LoRa.RegionName(), got.LoRa.PresetName(),
			got.LoRa.HopLimit, txWord(got.LoRa.TxEnabled))
		return nil
	}
	return errors.New("the radio has not answered since the reboot — unplug it, " +
		"plug it back in, and check with `terminal radio list`")
}
