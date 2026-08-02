// Writing configuration to a radio (RB-2 stretch). The "prepare this device"
// button: add the segment's channel, set the LoRa settings, reboot, done.
//
// Three invariants the plan fixed in advance, and why each one is here:
//
//   - READ-AFTER-WRITE. Nothing is reported as applied until the radio has
//     been re-read and says so itself. An acknowledged write is not a
//     verified one; firmware versions differ and our protobuf subset is
//     hand-rolled.
//   - THE KEY NEVER COMES BACK. We write a key we already hold; we never read
//     one out. The reader hashes keys where it decodes them (config.go), and
//     this file does not change that.
//   - PARTIAL FAILURE MUST NOT LEAVE AN UNKNOWN CONFIGURATION. Every change
//     goes inside begin_edit_settings / commit_edit_settings, so the device
//     applies the batch or none of it. If anything fails before the commit,
//     we simply never commit, and the radio keeps the configuration it had.
//
// Admin messages here are LOCAL only: addressed to the node's own number over
// the direct USB or TCP link we already hold. Remote administration of
// somebody else's node is a different trust question and is not implemented.
package meshtastic

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Admin protocol constants, from the reference protobufs (admin.proto,
// portnums.proto). Verified against a real device before use.
const (
	portAdminApp = 6

	adminSetChannel    = 33
	adminSetConfig     = 34
	adminBeginEdit     = 64
	adminCommitEdit    = 65
	adminRebootSeconds = 97

	// Data fields (mesh.proto).
	dataWantResponse = 3
)

// commitSettleTime is how long the device is given to persist a committed
// edit before it is asked to reboot. See the note in Apply: too short and the
// write is lost intermittently.
var commitSettleTime = 3 * time.Second

// applyStep is one write in the batch, kept as a named thing so a failure can
// say which step it was rather than "apply failed".
type applyStep struct {
	what    string
	payload []byte
}

// ApplyPlan is everything that will be written, in order. Built before
// anything is sent so the caller can show it and so a refusal costs nothing.
type ApplyPlan struct {
	steps []applyStep
	// Reboot is needed for LoRa changes to take effect. Channel-only changes
	// do not strictly need one, but a reboot makes the result unambiguous.
	Reboot bool
	// Summary is what to show a person before they commit to it.
	Summary []string
	// expectLoRa and expectDevice are the byte-exact sub-messages this plan
	// intends the radio to be holding afterwards — the fields it changes AND
	// every field it is obliged to carry across untouched. Verify compares
	// the radio's own report against them.
	//
	// Nil means the plan does not write that sub-message, and Verify makes no
	// claim about it. That distinction matters: silence about a message we
	// never wrote is honest, silence about one we did is how a muted
	// transmitter went unnoticed.
	expectLoRa   []byte
	expectDevice []byte
}

// ErrConfigInvalid names the state where a radio came back holding something
// other than what a transaction intended.
//
// It is a STATE, not a hiccup. The device is now in a configuration nobody
// chose, and the honest thing to report is which field differs — not "the
// command seemed to work", which is what `radio region` printed for nine days
// while its own writer was switching the transmitter off.
var ErrConfigInvalid = errors.New("radio_config_invalid")

// ConfigInvalidError carries what disagreed, so a caller can name the field
// rather than telling somebody to go and look.
type ConfigInvalidError struct{ Mismatches []ConfigMismatch }

func (e *ConfigInvalidError) Error() string {
	if len(e.Mismatches) == 0 {
		return ErrConfigInvalid.Error()
	}
	parts := make([]string, 0, len(e.Mismatches))
	for _, m := range e.Mismatches {
		parts = append(parts, m.String())
	}
	return ErrConfigInvalid.Error() + ": " + strings.Join(parts, "; ")
}

func (e *ConfigInvalidError) Unwrap() error { return ErrConfigInvalid }

// Verify compares what the radio reports NOW against what the plan intended
// to leave it holding.
//
// Both halves matter and neither is optional: the fields the plan CHANGED
// prove the write took, and the fields it PRESERVED prove the write did not
// take anything else with it. Checking only the first is exactly the mistake
// that made `radio region` report success on a radio it had just muted.
func (p *ApplyPlan) Verify(after NodeConfig) error {
	var bad []ConfigMismatch
	if p.expectLoRa != nil {
		if after.LoRaRaw == nil {
			return fmt.Errorf("%w: the radio did not report its LoRa settings "+
				"back, so nothing about this write is confirmed", ErrConfigInvalid)
		}
		diff, err := compareConfig(p.expectLoRa, after.LoRaRaw, loraFieldNames)
		if err != nil {
			return err
		}
		bad = append(bad, diff...)
	}
	if p.expectDevice != nil {
		if after.DeviceRaw == nil {
			return fmt.Errorf("%w: the radio did not report its device settings "+
				"back, so the rebroadcast mode is unconfirmed", ErrConfigInvalid)
		}
		diff, err := compareConfig(p.expectDevice, after.DeviceRaw, deviceFieldNames)
		if err != nil {
			return err
		}
		bad = append(bad, diff...)
	}
	if len(bad) > 0 {
		return &ConfigInvalidError{Mismatches: bad}
	}
	return nil
}

// planConfigWrite patches one Config sub-message and records both the step
// and what the result is expected to be.
//
// current is the byte-exact sub-message the radio reported. Without it there
// is nothing to preserve, and a write would be a blind full replacement —
// which is refused rather than guessed at, because the fields at risk include
// the one that decides whether the radio transmits at all.
func planConfigWrite(configField int, current []byte, set map[int]uint64,
	what string) (payload, expect []byte, err error) {
	if current == nil {
		return nil, nil, fmt.Errorf("meshtastic: this node never reported its %s, "+
			"so changing one setting would replace the whole message and erase "+
			"whatever else it holds — including whether the radio may transmit. "+
			"Re-read the radio first; if it still reports nothing, its firmware is "+
			"too old for this to be done safely", what)
	}
	patched, err := patchVarints(current, set)
	if err != nil {
		return nil, nil, err
	}
	cfg := appendBytesField(nil, configField, patched)
	return appendBytesField(nil, adminSetConfig, cfg), patched, nil
}

// PlanSegmentApply builds the writes that put a segment channel on a radio.
//
// slot is the free index found by FreeChannelSlot: add-only, so a channel
// somebody already configured is never overwritten. matchLoRa adds the region
// and preset writes, which are only needed when the radio does not already
// agree with the segment — and when it does, cur must carry that radio's own
// reported LoRa settings so everything else survives the write.
func PlanSegmentApply(cur NodeConfig, ch *SegmentChannel, slot int, matchLoRa bool) (*ApplyPlan, error) {
	if ch == nil {
		return nil, errors.New("meshtastic: nothing to apply")
	}
	if slot < 0 || slot > 7 {
		return nil, fmt.Errorf("meshtastic: channel slot %d is out of range", slot)
	}
	p := &ApplyPlan{Reboot: true}

	// Channel{index, settings{psk, name}, role=SECONDARY}. Slot 0 is the
	// PRIMARY and is never what we add to.
	settings := appendBytesField(nil, 2, ch.Key)              // psk
	settings = appendBytesField(settings, 3, []byte(ch.Name)) // name
	channel := appendVarintField(nil, 1, uint64(slot))        // index
	channel = appendBytesField(channel, 2, settings)          // settings
	channel = appendVarintField(channel, 3, uint64(ChannelSecondary))
	p.steps = append(p.steps, applyStep{
		what:    fmt.Sprintf("add channel %q at slot %d", ch.Name, slot),
		payload: appendBytesField(nil, adminSetChannel, channel),
	})
	p.Summary = append(p.Summary,
		fmt.Sprintf("Add channel %q with a new private key at slot %d "+
			"(slots already in use are not touched).", ch.Name, slot))

	if matchLoRa {
		// A patch, not a fresh message: this used to encode five fields and
		// thereby zero channel_num, tx_power and the manual bandwidth /
		// spreading-factor / coding-rate triple, along with anything a newer
		// firmware holds that this build cannot read.
		payload, expect, err := planConfigWrite(configLoRa, cur.LoRaRaw, map[int]uint64{
			loraUsePreset:   1,
			loraModemPreset: uint64(ch.ModemPreset),
			loraRegion:      uint64(ch.Region),
			loraHopLimit:    uint64(ch.HopLimit),
			loraTxEnabled:   1,
		}, "LoRa settings")
		if err != nil {
			return nil, err
		}
		p.steps = append(p.steps, applyStep{
			what:    "set region, modem preset and hop limit",
			payload: payload,
		})
		p.expectLoRa = expect
		p.Summary = append(p.Summary, fmt.Sprintf(
			"Set region %s, preset %s, hop limit %d, and leave every other LoRa "+
				"setting exactly as it is.",
			enumName(regionNames, ch.Region), enumName(presetNames, ch.ModemPreset),
			ch.HopLimit))
	}
	p.Summary = append(p.Summary,
		"Reboot the radio so the settings take effect, then re-read it and "+
			"check that what landed is what was asked for.")
	return p, nil
}

// Steps returns a human-readable list of what will be written.
func (p *ApplyPlan) Steps() []string {
	out := make([]string, 0, len(p.steps))
	for _, s := range p.steps {
		out = append(out, s.what)
	}
	return out
}

// adminPacket wraps an AdminMessage for the node's own address.
func (r *Radio) adminPacket(payload []byte, packetID uint32) []byte {
	data := appendVarintField(nil, 1, portAdminApp) // portnum
	data = appendBytesField(data, 2, payload)       // payload
	data = appendBoolField(data, dataWantResponse, true)

	pkt := appendFixed32Field(nil, 2, r.nodeNum) // to = ourselves
	pkt = appendVarintField(pkt, 3, 0)           // admin rides the primary channel
	pkt = appendBytesField(pkt, 4, data)
	pkt = appendFixed32Field(pkt, 6, packetID)
	return appendBytesField(nil, 1, pkt)
}

// Apply writes the plan to the radio, atomically as far as the firmware
// allows, and reboots it.
//
// It does NOT verify: the radio is rebooting when this returns, so there is
// nothing to read yet. Verification belongs to the caller, after the link has
// come back — which is exactly what the supervised link already does on its
// own. Splitting it this way keeps "what we asked for" and "what actually
// happened" as two separate claims, which is the whole point.
func (r *Radio) Apply(plan *ApplyPlan) error {
	if plan == nil || len(plan.steps) == 0 {
		return errors.New("meshtastic: nothing to apply")
	}
	if r.nodeNum == 0 {
		return errors.New("meshtastic: this radio never reported its node " +
			"number, so an admin message cannot be addressed to it")
	}
	if closed, err := r.Closed(); closed {
		return fmt.Errorf("meshtastic: the radio link is down: %w", err)
	}

	// begin_edit_settings … commit_edit_settings brackets the batch, so a
	// failure part-way through leaves the radio on its previous settings
	// rather than half of ours.
	if err := r.sendAdmin(appendBoolField(nil, adminBeginEdit, true)); err != nil {
		return fmt.Errorf("meshtastic: could not begin a settings edit "+
			"(nothing was changed): %w", err)
	}
	for i, s := range plan.steps {
		if err := r.sendAdmin(s.payload); err != nil {
			// Deliberately NOT committing: the device discards an uncommitted
			// edit, so the radio keeps the configuration it already had.
			return fmt.Errorf("meshtastic: step %d of %d (%s) failed; the edit "+
				"was not committed and the radio keeps its previous settings: %w",
				i+1, len(plan.steps), s.what, err)
		}
	}
	if err := r.sendAdmin(appendVarintField(nil, adminCommitEdit, 1)); err != nil {
		return fmt.Errorf("meshtastic: the settings were not committed, so the "+
			"radio keeps its previous configuration: %w", err)
	}
	if plan.Reboot {
		// The commit has to reach FLASH before the reset, and that is slower
		// than it looks. With half a second the write landed sometimes and
		// vanished other times — an intermittent failure that looks exactly
		// like a protocol bug and is really a race against the device's own
		// storage. Observed on a T3-S3 at firmware 2.7.15.
		time.Sleep(commitSettleTime)
		// A later reset instant gives the device its own margin on top.
		if err := r.sendAdmin(appendVarintField(nil, adminRebootSeconds, 5)); err != nil {
			return fmt.Errorf("meshtastic: settings were committed but the "+
				"reboot request failed; power-cycle the radio to apply them: %w", err)
		}
	}
	return nil
}

func (r *Radio) sendAdmin(payload []byte) error {
	var idBytes [4]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return err
	}
	id := uint32(idBytes[0])<<24 | uint32(idBytes[1])<<16 |
		uint32(idBytes[2])<<8 | uint32(idBytes[3])
	frame := r.adminPacket(payload, id)
	if err := writeFrame(r.conn, frame); err != nil {
		r.fail(err)
		return err
	}
	// The firmware processes admin messages in order on one link; a short
	// gap keeps a burst from overrunning its queue on slower devices.
	time.Sleep(250 * time.Millisecond)
	if closed, err := r.Closed(); closed {
		return fmt.Errorf("the link dropped while writing: %w", err)
	}
	return nil
}

// LoRaSetting is one field of the radio's LoRa configuration. A nil pointer
// means "leave this alone": a person setting the region must not silently
// have their modem preset rewritten to whatever this build defaults to.
type LoRaSetting struct {
	Region      *uint32
	ModemPreset *uint32
	HopLimit    *uint32
	// ChannelNum picks the frequency SLOT explicitly (LoRaConfig field 11:
	// "A channel number between 1 and NUM_CHANNELS. If ZERO then use the
	// old channel name hash-based algorithm").
	//
	// This is how a segment gets off a busy frequency without moving. By
	// default the slot is a hash of the PRIMARY channel's name, and every
	// secondary channel rides the primary's frequency — so a segment that
	// keeps the stock public primary transmits on precisely the frequency
	// its neighbours are using, however private its own channel is.
	ChannelNum *uint32
	// Rebroadcast decides whether this radio repeats other people's
	// traffic (DeviceConfig field 6). The default is ALL — "rebroadcast
	// any observed message ... or from another mesh with the same lora
	// params" — which on a busy band spends a segment's airtime relaying
	// strangers. LOCAL_ONLY keeps the mesh working for its own channels
	// and stops carrying the neighbourhood.
	Rebroadcast *uint32
	// TxEnabled turns the transmitter on or off (LoRaConfig field 9).
	//
	// It is here because this file switched it OFF on real hardware and left
	// it that way: set_config replaces the whole sub-message, so every write
	// that omitted this field set it to its proto3 default, false. The patch
	// now preserves it — but a board already muted needs a way back, and an
	// explicit setting is the only honest one. Nothing here turns a
	// transmitter on as a side effect of some other command.
	TxEnabled *bool
}

// Rebroadcast modes (config.proto RebroadcastMode). Only the two a segment
// realistically chooses between are named; the rest are passed as numbers
// by whoever needs them.
const (
	RebroadcastAll       uint32 = 0
	RebroadcastLocalOnly uint32 = 2
)

// PlanLoRaApply builds the writes for the LoRa settings alone.
//
// Separate from PlanSegmentApply because the two answer different questions.
// That one puts a segment's CHANNEL on a radio and can match the LoRa
// settings while it is there; this one changes only what it was asked to
// change, which is what "set the region" has to mean — the region decides
// which frequencies a device transmits on, and quietly moving a preset or a
// hop limit alongside it would be changing the radio's behaviour on the air
// beyond what was asked.
//
// cur is the radio's OWN last report, and it is required rather than
// optional. "Change only what was asked" is not something this function can
// promise on its own: the write it produces replaces a whole sub-message, so
// every setting it does not carry across is erased on the device. The
// promise lives in the bytes it starts from.
func PlanLoRaApply(cur NodeConfig, s LoRaSetting) (*ApplyPlan, error) {
	if s.Region == nil && s.ModemPreset == nil && s.HopLimit == nil &&
		s.ChannelNum == nil && s.Rebroadcast == nil && s.TxEnabled == nil {
		return nil, errors.New("meshtastic: nothing to set")
	}
	p := &ApplyPlan{Reboot: true}
	lora := map[int]uint64{}
	if s.ModemPreset != nil {
		lora[loraUsePreset] = 1
		lora[loraModemPreset] = uint64(*s.ModemPreset)
		p.Summary = append(p.Summary, "Set modem preset "+
			enumName(presetNames, *s.ModemPreset)+".")
	}
	if s.Region != nil {
		lora[loraRegion] = uint64(*s.Region)
		p.Summary = append(p.Summary, "Set region "+
			enumName(regionNames, *s.Region)+
			" — this decides which frequencies the radio transmits on.")
	}
	if s.HopLimit != nil {
		lora[loraHopLimit] = uint64(*s.HopLimit)
		p.Summary = append(p.Summary, fmt.Sprintf("Set hop limit %d.", *s.HopLimit))
	}
	if s.ChannelNum != nil {
		lora[loraChannelNum] = uint64(*s.ChannelNum)
		p.Summary = append(p.Summary, fmt.Sprintf(
			"Set frequency slot %d — this moves the radio off the slot its "+
				"primary channel's name would have chosen.", *s.ChannelNum))
	}
	if s.TxEnabled != nil {
		lora[loraTxEnabled] = boolVarint(*s.TxEnabled)
		if *s.TxEnabled {
			p.Summary = append(p.Summary, "Enable the transmitter.")
		} else {
			p.Summary = append(p.Summary,
				"DISABLE the transmitter — the radio will still receive, and "+
					"nothing it is asked to send will leave the board.")
		}
	}
	if len(lora) > 0 {
		payload, expect, err := planConfigWrite(configLoRa, cur.LoRaRaw, lora,
			"LoRa settings")
		if err != nil {
			return nil, err
		}
		p.steps = append(p.steps, applyStep{
			what:    "set LoRa configuration",
			payload: payload,
		})
		p.expectLoRa = expect
	}
	if s.Rebroadcast != nil {
		payload, expect, err := planConfigWrite(configDevice, cur.DeviceRaw,
			map[int]uint64{deviceRebroadcast: uint64(*s.Rebroadcast)},
			"device settings")
		if err != nil {
			return nil, err
		}
		p.steps = append(p.steps, applyStep{
			what:    "set rebroadcast mode",
			payload: payload,
		})
		p.expectDevice = expect
		word := "ALL (repeat everything observed)"
		if *s.Rebroadcast == RebroadcastLocalOnly {
			word = "LOCAL_ONLY (stop repeating other meshes' traffic)"
		}
		p.Summary = append(p.Summary, "Set rebroadcast mode "+word+".")
	}
	p.Summary = append(p.Summary,
		"Leave every other setting on the radio exactly as it is — including "+
			"whether it may transmit.")
	p.Summary = append(p.Summary,
		"Reboot the radio, then re-read it and check that what landed is "+
			"what was asked for, AND that nothing else moved.")
	return p, nil
}

func boolVarint(v bool) uint64 {
	if v {
		return 1
	}
	return 0
}

// ParseRegion resolves a region NAME, and refuses one it does not know.
//
// RegionValue exists for dev tools and answers 0 (UNSET) for anything it
// cannot resolve. That is the wrong answer here: a typo in a region name
// would silently write "no region", which is the one value that stops a
// radio transmitting at all. A person setting a region gets an error and the
// list instead.
func ParseRegion(name string) (uint32, error) {
	return resolveEnum(regionNames, name, "region")
}

// ParsePreset is the same rule for the modem preset.
func ParsePreset(name string) (uint32, error) {
	return resolveEnum(presetNames, name, "modem preset")
}

// RegionNames and PresetNames list what this build understands, for a
// command that has to show the choices.
func RegionNames() []string { return sortedNames(regionNames) }
func PresetNames() []string { return sortedNames(presetNames) }
