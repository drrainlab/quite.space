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
}

// PlanSegmentApply builds the writes that put a segment channel on a radio.
//
// slot is the free index found by FreeChannelSlot: add-only, so a channel
// somebody already configured is never overwritten. matchLoRa adds the region
// and preset writes, which are only needed when the radio does not already
// agree with the segment.
func PlanSegmentApply(ch *SegmentChannel, slot int, matchLoRa bool) (*ApplyPlan, error) {
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
		lora := appendBoolField(nil, 1, true)                     // use_preset
		lora = appendVarintField(lora, 2, uint64(ch.ModemPreset)) // modem_preset
		lora = appendVarintField(lora, 7, uint64(ch.Region))      // region
		lora = appendVarintField(lora, 8, uint64(ch.HopLimit))    // hop_limit
		lora = appendBoolField(lora, 9, true)                     // tx_enabled
		cfg := appendBytesField(nil, 6, lora)                     // Config.lora
		p.steps = append(p.steps, applyStep{
			what:    "set region, modem preset and hop limit",
			payload: appendBytesField(nil, adminSetConfig, cfg),
		})
		p.Summary = append(p.Summary, fmt.Sprintf(
			"Set region %s, preset %s, hop limit %d.",
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
}

// PlanLoRaApply builds the writes for the LoRa settings alone.
//
// Separate from PlanSegmentApply because the two answer different questions.
// That one puts a segment's CHANNEL on a radio and can match the LoRa
// settings while it is there; this one changes only what it was asked to
// change, which is what "set the region" has to mean — the region decides
// which frequencies a device transmits on, and quietly moving a preset or a
// hop limit alongside it would be changing the radio's behaviour on the air
// beyond what was asked.
func PlanLoRaApply(s LoRaSetting) (*ApplyPlan, error) {
	if s.Region == nil && s.ModemPreset == nil && s.HopLimit == nil {
		return nil, errors.New("meshtastic: nothing to set")
	}
	p := &ApplyPlan{Reboot: true}
	var lora []byte
	if s.ModemPreset != nil {
		lora = appendBoolField(lora, 1, true) // use_preset
		lora = appendVarintField(lora, 2, uint64(*s.ModemPreset))
		p.Summary = append(p.Summary, "Set modem preset "+
			enumName(presetNames, *s.ModemPreset)+".")
	}
	if s.Region != nil {
		lora = appendVarintField(lora, 7, uint64(*s.Region))
		p.Summary = append(p.Summary, "Set region "+
			enumName(regionNames, *s.Region)+
			" — this decides which frequencies the radio transmits on.")
	}
	if s.HopLimit != nil {
		lora = appendVarintField(lora, 8, uint64(*s.HopLimit))
		p.Summary = append(p.Summary, fmt.Sprintf("Set hop limit %d.", *s.HopLimit))
	}
	cfg := appendBytesField(nil, 6, lora) // Config.lora
	p.steps = append(p.steps, applyStep{
		what:    "set LoRa configuration",
		payload: appendBytesField(nil, adminSetConfig, cfg),
	})
	p.Summary = append(p.Summary,
		"Reboot the radio, then re-read it and check that what landed is "+
			"what was asked for.")
	return p, nil
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
