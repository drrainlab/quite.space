package meshtastic

import (
	"math"
	"strings"
	"testing"
)

// realisticLoRaRaw is a LoRaConfig as a device actually sends one: the fields
// this build reads, a FLOAT this build has never been able to read, and a
// field number from a firmware newer than this code.
//
// The last two are the point. A configuration is not the ten varints
// LoRaSettings keeps; it is whatever the device holds, and a write replaces
// all of it.
func realisticLoRaRaw() []byte {
	b := appendVarintField(nil, loraUsePreset, 1)
	b = appendVarintField(b, loraModemPreset, 0) // LONG_FAST, omitted as a default
	b = appendVarintField(b, loraRegion, uint64(regionRU))
	b = appendVarintField(b, loraHopLimit, 3)
	b = appendVarintField(b, loraTxEnabled, 1)
	b = appendVarintField(b, loraTxPower, 20)
	b = appendVarintField(b, loraChannelNum, 7)
	// override_frequency, a float — decodeLoRaConfig skips it by design and
	// says so in its own comment.
	b = appendFixed32Field(b, 14, math.Float32bits(869.525))
	// Something a firmware released after this build added.
	b = appendVarintField(b, 200, 42)
	return b
}

// THE headline test of this gate.
//
// Setting the region must change the region and NOTHING else — not as a
// property of the summary text, which is all the previous test checked, but
// as a property of the bytes that reach the device. The old writer passed a
// summary test happily while erasing tx_enabled, tx_power, channel_num and
// the operator's override frequency on every run.
func TestSettingTheRegionPreservesEverythingElse(t *testing.T) {
	before := realisticLoRaRaw()
	cur := NodeConfig{LoRaRaw: before}
	eu := regionEU868

	plan, err := PlanLoRaApply(cur, LoRaSetting{Region: &eu})
	if err != nil {
		t.Fatal(err)
	}
	after, err := fieldsOf(plan.expectLoRa)
	if err != nil {
		t.Fatal(err)
	}
	original, err := fieldsOf(before)
	if err != nil {
		t.Fatal(err)
	}

	if got := after[loraRegion].String(); got != "3" {
		t.Fatalf("region = %s, want 3 (EU_868) — the one field asked for", got)
	}
	for field, want := range original {
		if field == loraRegion {
			continue
		}
		got, present := after[field]
		if !present {
			t.Fatalf("field %d (%s) was ERASED by a write that only asked for a "+
				"region. set_config replaces the whole message; a field left out "+
				"of the patch is a field switched off on the device",
				field, loraFieldNames[field])
		}
		if got.wire != want.wire || string(got.raw) != string(want.raw) {
			t.Fatalf("field %d (%s) changed from %s to %s and nobody asked",
				field, loraFieldNames[field], want.String(), got.String())
		}
	}

	// Named individually, because each one is a real failure this gate exists
	// to stop rather than an abstract invariant.
	if after[loraTxEnabled].String() != "1" {
		t.Fatal("tx_enabled did not survive — this is the defect that muted two " +
			"real boards and voided nine days of measurements")
	}
	if after[200].String() != "42" {
		t.Fatal("a field from a newer firmware was dropped: this build must " +
			"carry across what it cannot read")
	}
	if after[14].wire != wireFixed32 {
		t.Fatal("override_frequency, a float, did not survive — decoding into " +
			"LoRaSettings and re-encoding would have lost exactly this")
	}
}

// A patch needs bytes to start from. A node that reported no LoRaConfig gives
// us nothing to preserve, so a write would be a blind full replacement — and
// among the fields it would silently zero is whether the radio may transmit.
// Refusing is the only honest answer; guessing at the rest is not.
func TestAWriteIsRefusedWhenThereIsNothingToPreserve(t *testing.T) {
	eu := regionEU868
	_, err := PlanLoRaApply(NodeConfig{}, LoRaSetting{Region: &eu})
	if err == nil {
		t.Fatal("a node that reported no LoRa settings was written to anyway")
	}
	if !strings.Contains(err.Error(), "transmit") {
		t.Fatalf("the refusal does not say what is at stake: %v", err)
	}

	// The same rule for the device sub-message, which carries the rebroadcast
	// mode `--quiet-neighbours` writes.
	local := RebroadcastLocalOnly
	if _, err := PlanLoRaApply(NodeConfig{LoRaRaw: realisticLoRaRaw()},
		LoRaSetting{Rebroadcast: &local}); err == nil {
		t.Fatal("the rebroadcast mode was written to a node that never reported " +
			"its device settings")
	}
}

// Verification must be about the WHOLE configuration, not the field that was
// asked for. A radio that comes back with the right region and a dead
// transmitter has not applied the write; it has survived it.
func TestVerifyCatchesAFieldTheWriteErased(t *testing.T) {
	cur := NodeConfig{LoRaRaw: realisticLoRaRaw()}
	eu := regionEU868
	plan, err := PlanLoRaApply(cur, LoRaSetting{Region: &eu})
	if err != nil {
		t.Fatal(err)
	}

	// What the radio came back with: the region landed, tx_enabled did not.
	muted := appendVarintField(nil, loraUsePreset, 1)
	muted = appendVarintField(muted, loraRegion, uint64(regionEU868))
	muted = appendVarintField(muted, loraHopLimit, 3)
	muted = appendVarintField(muted, loraTxPower, 20)
	muted = appendVarintField(muted, loraChannelNum, 7)
	muted = appendFixed32Field(muted, 14, math.Float32bits(869.525))
	muted = appendVarintField(muted, 200, 42)

	err = plan.Verify(NodeConfig{LoRaRaw: muted})
	if err == nil {
		t.Fatal("a radio that came back muted was reported as verified — this " +
			"is exactly what `✓ region set` printed for nine days")
	}
	var invalid *ConfigInvalidError
	if !asConfigInvalid(err, &invalid) {
		t.Fatalf("the failure is not the named state: %v", err)
	}
	if !strings.Contains(err.Error(), "tx_enabled") {
		t.Fatalf("the report does not name the field that differs: %v", err)
	}
}

// The device re-encodes what it stores, so the bytes that come back are never
// the bytes that went out: field order is the encoder's choice, and a field at
// its default is omitted entirely. Comparing bytes would make every write
// fail; comparing MEANING is the only comparison worth making.
func TestVerifyAcceptsTheEncodersOwnChoices(t *testing.T) {
	cur := NodeConfig{LoRaRaw: realisticLoRaRaw()}
	eu := regionEU868
	plan, err := PlanLoRaApply(cur, LoRaSetting{Region: &eu})
	if err != nil {
		t.Fatal(err)
	}

	// The same configuration, written backwards, with the two default-valued
	// fields left out the way proto3 permits.
	shuffled := appendVarintField(nil, 200, 42)
	shuffled = appendFixed32Field(shuffled, 14, math.Float32bits(869.525))
	shuffled = appendVarintField(shuffled, loraChannelNum, 7)
	shuffled = appendVarintField(shuffled, loraTxPower, 20)
	shuffled = appendVarintField(shuffled, loraTxEnabled, 1)
	shuffled = appendVarintField(shuffled, loraHopLimit, 3)
	shuffled = appendVarintField(shuffled, loraRegion, uint64(regionEU868))
	shuffled = appendVarintField(shuffled, loraUsePreset, 1)

	if err := plan.Verify(NodeConfig{LoRaRaw: shuffled}); err != nil {
		t.Fatalf("the same configuration in a different order was called a "+
			"mismatch: %v", err)
	}
}

// A field the device never reported is APPENDED, not lost — otherwise a
// setting could never be turned on for the first time.
func TestAnAbsentFieldCanStillBeSet(t *testing.T) {
	// A radio whose slot has never been set explicitly.
	raw := appendVarintField(nil, loraRegion, uint64(regionRU))
	raw = appendVarintField(raw, loraTxEnabled, 1)

	slot := uint32(5)
	plan, err := PlanLoRaApply(NodeConfig{LoRaRaw: raw}, LoRaSetting{ChannelNum: &slot})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decodeLoRaConfig(plan.expectLoRa)
	if !ok {
		t.Fatal("the patched configuration no longer decodes")
	}
	if got.ChannelNum != 5 {
		t.Fatalf("channel_num = %d, want 5", got.ChannelNum)
	}
	if !got.TxEnabled || got.Region != regionRU {
		t.Fatalf("setting the slot disturbed the rest: %+v", got)
	}
}

// Setting a field to its default REMOVES it, because in proto3 that is the
// same instruction — and it is what the device itself will encode, so a
// verification comparing the two must agree.
func TestSettingAFieldToItsDefaultLeavesNoTrace(t *testing.T) {
	raw := appendVarintField(nil, loraRegion, uint64(regionRU))
	raw = appendVarintField(raw, loraTxEnabled, 1)

	off := false
	plan, err := PlanLoRaApply(NodeConfig{LoRaRaw: raw}, LoRaSetting{TxEnabled: &off})
	if err != nil {
		t.Fatal(err)
	}
	fields, err := fieldsOf(plan.expectLoRa)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := fields[loraTxEnabled]; present {
		t.Fatal("tx_enabled=false was written explicitly; proto3 omits a default " +
			"and the device will too, so verification would then disagree with " +
			"itself")
	}
	// And the device omitting it must still verify.
	back := appendVarintField(nil, loraRegion, uint64(regionRU))
	if err := plan.Verify(NodeConfig{LoRaRaw: back}); err != nil {
		t.Fatalf("a radio reporting the default was called a mismatch: %v", err)
	}
}

// The rebroadcast mode has been WRITTEN by `--quiet-neighbours` since the day
// that flag shipped, and until now nothing could read it back: the decoder
// returned only for Config.lora and dropped every other Config on the floor.
// A write nobody can read is a write nobody can verify.
func TestTheRebroadcastModeCanBeReadBack(t *testing.T) {
	dev := appendVarintField(nil, deviceRole, 1) // CLIENT_MUTE
	dev = appendVarintField(dev, deviceRebroadcast, uint64(RebroadcastLocalOnly))
	dev = appendVarintField(dev, 99, 7) // something newer
	cfgMsg := appendBytesField(nil, configDevice, dev)

	var cfg NodeConfig
	if !cfg.absorbConfig(5, cfgMsg) {
		t.Fatal("a DeviceConfig frame was not absorbed at all")
	}
	if cfg.Device == nil {
		t.Fatal("the node reported its device settings and we kept nothing")
	}
	if cfg.Device.Rebroadcast != RebroadcastLocalOnly {
		t.Fatalf("rebroadcast = %s, want LOCAL_ONLY", cfg.Device.RebroadcastName())
	}
	if cfg.Device.RoleName() != "CLIENT_MUTE" {
		t.Fatalf("role = %s", cfg.Device.RoleName())
	}

	// And a patch of it preserves the rest, same rule as LoRa.
	all := RebroadcastAll
	plan, err := PlanLoRaApply(cfg, LoRaSetting{Rebroadcast: &all})
	if err != nil {
		t.Fatal(err)
	}
	got, err := fieldsOf(plan.expectDevice)
	if err != nil {
		t.Fatal(err)
	}
	if got[deviceRole].String() != "1" {
		t.Fatal("the device role did not survive a rebroadcast-mode write")
	}
	if got[99].String() != "7" {
		t.Fatal("an unknown device field did not survive")
	}
}

// "Reported nothing" and "reported a message that is entirely defaults" are
// different facts, and only the first forbids a write.
//
// A device with a CLIENT role and ALL rebroadcasting holds a DeviceConfig in
// which every field is at its proto3 default, so it arrives as ZERO BYTES.
// Reading that as "this node never reported its device settings" would refuse
// to change the one setting such a device has.
func TestAnAllDefaultSubMessageIsStillAReport(t *testing.T) {
	empty := appendBytesField(nil, configDevice, nil)
	var cfg NodeConfig
	if !cfg.absorbConfig(5, empty) {
		t.Fatal("an all-default DeviceConfig was not absorbed")
	}
	if cfg.DeviceRaw == nil {
		t.Fatal("an empty-but-present sub-message was recorded as absent — the " +
			"radio DID answer, and every setting in it is now unchangeable")
	}
	local := RebroadcastLocalOnly
	plan, err := PlanLoRaApply(cfg, LoRaSetting{Rebroadcast: &local})
	if err != nil {
		t.Fatalf("a device holding only defaults refused a write: %v", err)
	}
	got, _ := decodeDeviceConfig(plan.expectDevice)
	if got.Rebroadcast != RebroadcastLocalOnly {
		t.Fatalf("rebroadcast = %d", got.Rebroadcast)
	}
}

// A radio may hold a value whose field number this build has misidentified.
// There is no safe way through: writing to it corrupts a field we do not
// understand, and skipping it quietly performs a write that does not do what
// it said. So the whole patch is refused.
func TestAFieldOfTheWrongShapeStopsTheWrite(t *testing.T) {
	// Field 7 as a length-delimited value: not what region looks like here.
	raw := appendBytesField(nil, loraRegion, []byte("not a varint"))
	if _, err := patchVarints(raw, map[int]uint64{loraRegion: 3}); err == nil {
		t.Fatal("a field whose wire type we did not expect was written to " +
			"anyway — this build has the number wrong, and that is corruption")
	}
	// And nothing is quietly appended alongside it either: a patch that
	// cannot be applied must not turn into a write with two contradictory
	// copies of the same field.
	out, err := patchVarints(raw, map[int]uint64{loraHopLimit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if fields, _ := fieldsOf(out); fields[loraRegion].wire != wireBytes {
		t.Fatal("an unrelated patch disturbed the field it could not read")
	}
}

// Small helper so the test above reads as one thought.
func asConfigInvalid(err error, out **ConfigInvalidError) bool {
	for err != nil {
		if e, ok := err.(*ConfigInvalidError); ok {
			*out = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
