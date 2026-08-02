package meshtastic

import (
	"math"
	"strings"
	"testing"
	"time"
)

func testTime() time.Time { return time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC) }

// A snapshot must survive the file byte for byte, including the fields this
// build cannot read. Restoring from a snapshot that quietly dropped an
// operator's override frequency would be a second erasure dressed as a
// recovery.
func TestASnapshotSurvivesTheFileExactly(t *testing.T) {
	cfg := NodeConfig{
		NodeNum:   0x043ccd50,
		Firmware:  "2.7.26.54e0d8d",
		LoRaRaw:   realisticLoRaRaw(),
		DeviceRaw: appendVarintField(nil, deviceRebroadcast, uint64(RebroadcastLocalOnly)),
	}
	file := SnapshotFile{Radios: map[string]ConfigSnapshot{
		SnapshotKey(cfg.NodeNum): SnapshotOf(cfg, testTime()),
	}}
	data, err := file.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeSnapshotFile(data)
	if err != nil {
		t.Fatal(err)
	}
	snap, ok := back.Radios[SnapshotKey(cfg.NodeNum)]
	if !ok {
		t.Fatal("the radio is not in the file it was written to")
	}
	got, err := snap.Config()
	if err != nil {
		t.Fatal(err)
	}
	if string(got.LoRaRaw) != string(cfg.LoRaRaw) {
		t.Fatalf("the LoRa bytes changed across the file:\n saved %x\n read  %x",
			cfg.LoRaRaw, got.LoRaRaw)
	}
	if string(got.DeviceRaw) != string(cfg.DeviceRaw) {
		t.Fatal("the device bytes changed across the file")
	}
	if got.Firmware != cfg.Firmware || got.NodeNum != cfg.NodeNum {
		t.Fatalf("the radio's identity changed: %+v", got)
	}
	if snap.TakenAt != "2026-08-02T11:00:00Z" {
		t.Fatalf("taken_at = %q", snap.TakenAt)
	}
}

// An empty file is a first run, not a failure.
func TestAnEmptyStoreIsAFirstRun(t *testing.T) {
	f, err := DecodeSnapshotFile(nil)
	if err != nil {
		t.Fatalf("an empty snapshot store was an error: %v", err)
	}
	if f.Radios == nil {
		t.Fatal("the store has no map, so the first capture would panic")
	}
}

// The whole point, in one test: a radio that was muted by a bad write is put
// back the way it was.
func TestARestorePutsBackWhatAWriteErased(t *testing.T) {
	good := NodeConfig{
		NodeNum: 0x043ccd50,
		LoRaRaw: realisticLoRaRaw(),
	}
	snap := SnapshotOf(good, testTime())

	// What the old writer left behind: the region it was asked for, and
	// nothing else.
	damaged := NodeConfig{
		NodeNum: good.NodeNum,
		LoRaRaw: appendVarintField(nil, loraRegion, uint64(regionRU)),
	}

	plan, cannot, err := PlanRestore(damaged, snap)
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil {
		t.Fatal("a damaged radio produced no restore")
	}
	// override_frequency is a float, and this build cannot write one. It must
	// be NAMED rather than silently left out of a restore reported as done.
	if len(cannot) != 1 || !strings.Contains(cannot[0], "override_frequency") {
		t.Fatalf("the unrestorable float was not reported: %v", cannot)
	}

	got, ok := decodeLoRaConfig(plan.expectLoRa)
	if !ok {
		t.Fatal("the restored configuration does not decode")
	}
	if !got.TxEnabled {
		t.Fatal("the transmitter was not turned back on — this is the exact " +
			"state two real boards were left in, and the reason this file exists")
	}
	if got.TxPower != 20 || got.ChannelNum != 7 || got.HopLimit != 3 || !got.UsePreset {
		t.Fatalf("the restore did not put everything back: %+v", got)
	}
	fields, err := fieldsOf(plan.expectLoRa)
	if err != nil {
		t.Fatal(err)
	}
	if fields[200].String() != "42" {
		t.Fatal("a field from a newer firmware was not restored")
	}
}

// A setting the snapshot does NOT have, which the radio has acquired since,
// goes back to its default — otherwise "restore" would mean "restore some of
// it and keep whichever surprises arrived in the meantime".
func TestARestoreRemovesWhatWasNotThereBefore(t *testing.T) {
	before := appendVarintField(nil, loraRegion, uint64(regionRU))
	before = appendVarintField(before, loraTxEnabled, 1)
	snap := SnapshotOf(NodeConfig{LoRaRaw: before}, testTime())

	now := NodeConfig{LoRaRaw: appendVarintField(
		appendVarintField(before, loraChannelNum, 5), loraHopLimit, 7)}

	plan, _, err := PlanRestore(now, snap)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := decodeLoRaConfig(plan.expectLoRa)
	if got.ChannelNum != 0 || got.HopLimit != 0 {
		t.Fatalf("settings absent from the snapshot survived the restore: %+v", got)
	}
	if !got.TxEnabled || got.Region != regionRU {
		t.Fatalf("the restore lost what the snapshot did hold: %+v", got)
	}
}

// Nothing to do is nothing to do — a restore of an unchanged radio must not
// produce a write, and certainly not a reboot.
func TestRestoringAnUnchangedRadioWritesNothing(t *testing.T) {
	cfg := NodeConfig{LoRaRaw: realisticLoRaRaw()}
	plan, cannot, err := PlanRestore(cfg, SnapshotOf(cfg, testTime()))
	if err != nil {
		t.Fatal(err)
	}
	if plan != nil {
		t.Fatalf("an unchanged radio was going to be written to: %v", plan.Steps())
	}
	if len(cannot) != 0 {
		t.Fatalf("an unchanged radio reported unrestorable fields: %v", cannot)
	}
}

// A drift is named field by field, not as "something changed".
func TestADiffNamesEverySettingThatMoved(t *testing.T) {
	// RebroadcastAll is 0, and proto3 omits a default — so "was ALL, is now
	// LOCAL_ONLY" is a field that was absent and is now present. The role
	// keeps the message non-empty so both sides are genuinely reported.
	saved := NodeConfig{
		LoRaRaw:   realisticLoRaRaw(),
		DeviceRaw: appendVarintField(nil, deviceRole, 2),
	}
	now := NodeConfig{
		// Only the region survived: this is what the old writer left behind.
		LoRaRaw: appendFixed32Field(
			appendVarintField(nil, loraRegion, uint64(regionEU868)),
			14, math.Float32bits(869.525)),
		DeviceRaw: appendVarintField(
			appendVarintField(nil, deviceRole, 2),
			deviceRebroadcast, uint64(RebroadcastLocalOnly)),
	}

	diff, err := DiffConfig(saved, now)
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, m := range diff {
		named[m.Name] = true
	}
	for _, want := range []string{"region", "tx_enabled", "tx_power",
		"channel_num", "hop_limit", "rebroadcast_mode"} {
		if !named[want] {
			t.Fatalf("%s moved and the diff did not say so: %v", want, diff)
		}
	}
	if named["override_frequency"] {
		t.Fatal("a float that did NOT change was reported as changed")
	}
}

// A radio that has stopped reporting a whole sub-message must not read as
// "nothing moved". That is the loudest possible drift, and comparing only
// where both sides happen to be present would skip it entirely.
func TestADiffSaysWhenTheRadioStoppedReporting(t *testing.T) {
	saved := NodeConfig{
		LoRaRaw:   realisticLoRaRaw(),
		DeviceRaw: appendVarintField(nil, deviceRole, 2),
	}
	now := NodeConfig{LoRaRaw: realisticLoRaRaw()} // no device message at all

	diff, err := DiffConfig(saved, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) != 1 || !strings.Contains(diff[0].String(), "no longer reporting") {
		t.Fatalf("a vanished sub-message was not reported: %v", diff)
	}

	// And the reverse is not a drift: a message we never captured cannot
	// have moved, and saying it did sends somebody looking for nothing.
	back, err := DiffConfig(now, saved)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 0 {
		t.Fatalf("a message that was never captured was reported as changed: %v", back)
	}
}
