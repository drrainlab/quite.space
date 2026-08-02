package meshtastic

import (
	"strings"
	"testing"
)

// The boot banner is the only evidence an unflashed board gives, so reading
// it correctly is the whole basis for narrowing the variant list.
func TestBootBannerNamesTheChipAndNothingElse(t *testing.T) {
	// Captured from a real Heltec V3 after a reset pulse.
	const real = "ESP-ROM:esp32s3-20210327\r\n" +
		"Build:Mar 27 2021\r\n" +
		"rst:0x1 (POWERON),boot:0x9 (SPI_FAST_FLASH_BOOT)\r\n" +
		"select external 32K RTC\r\nESP32ChipID=08EAFCF61B44\r\n"
	b := ParseBootBanner(real)
	if b.Chip != "esp32s3" {
		t.Fatalf("chip = %q, want esp32s3", b.Chip)
	}
	if b.Reset != "0x1 (POWERON)" {
		t.Fatalf("reset = %q", b.Reset)
	}
	// It must not claim to know the BOARD. Nothing in this struct can.
	if strings.Contains(strings.ToLower(b.Chip), "heltec") {
		t.Fatal("the chip field named a board — a banner cannot know that")
	}
}

func TestSilenceIsNotAChip(t *testing.T) {
	b := ParseBootBanner("")
	if b.Chip != "" || b.Spoke() {
		t.Fatalf("silence produced %+v — a device that said nothing must not "+
			"be described as anything", b)
	}
}

// The install recipe comes from the release, so a wrong reading of it is a
// wrong offset on somebody's hardware. This is the manifest a real release
// publishes for heltec-v3, trimmed to the shape that matters.
const heltecV3Manifest = `{
  "version": "2.7.26.54e0d8d",
  "platformioTarget": "heltec-v3",
  "mcu": "esp32s3",
  "files": [
    {"name": "firmware-heltec-v3-2.7.26.54e0d8d.elf", "md5": "3b44", "bytes": 22268396},
    {"name": "firmware-heltec-v3-2.7.26.54e0d8d.bin", "md5": "d7e2", "bytes": 2109248, "part_name": "app0"},
    {"name": "firmware-heltec-v3-2.7.26.54e0d8d.factory.bin", "md5": "3c65", "bytes": 2174784},
    {"name": "littlefs-heltec-v3-2.7.26.54e0d8d.bin", "md5": "1a35", "bytes": 1572864, "part_name": "spiffs"},
    {"name": "mt-esp32s3-ota.bin", "md5": "497d", "bytes": 636544, "part_name": "app1"}
  ],
  "part": [
    {"name": "nvs", "offset": "0x9000"},
    {"name": "otadata", "offset": "0xe000"},
    {"name": "app0", "offset": "0x10000"},
    {"name": "app1", "offset": "0x340000"},
    {"name": "spiffs", "offset": "0x670000"}
  ]
}`

func TestAFirstInstallWritesTheFactoryImageAndTheFilesystem(t *testing.T) {
	plan, err := ParseInstallManifest([]byte(heltecV3Manifest))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Board != "heltec-v3" || plan.MCU != "esp32s3" {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.Files) != 2 {
		t.Fatalf("a first install writes the factory image and the filesystem, "+
			"got %d files: %+v", len(plan.Files), plan.Files)
	}
	// The factory image carries bootloader + partition table + app, so it
	// goes at zero. Reading this wrong is the difference between a working
	// board and a dead one.
	if plan.Files[0].Offset != 0 ||
		!strings.HasSuffix(plan.Files[0].Name, ".factory.bin") {
		t.Fatalf("first write = %+v, want the factory image at 0x0", plan.Files[0])
	}
	// The filesystem offset must come from the release's own partition
	// table, never from a constant in our code.
	if plan.Files[1].Offset != 0x670000 {
		t.Fatalf("littlefs offset = 0x%x, want the 0x670000 the manifest states",
			plan.Files[1].Offset)
	}
	// The OTA slot and the .elf are not part of a first install.
	for _, f := range plan.Files {
		if strings.HasSuffix(f.Name, ".elf") || strings.Contains(f.Name, "-ota") {
			t.Fatalf("%s does not belong in a first install", f.Name)
		}
	}
}

// If the release ever ships a file naming a partition its own table does not
// describe, the honest answer is a refusal — not a plausible offset.
func TestAnUnknownPartitionRefusesRatherThanGuesses(t *testing.T) {
	broken := strings.Replace(heltecV3Manifest,
		`{"name": "spiffs", "offset": "0x670000"}`,
		`{"name": "somethingelse", "offset": "0x670000"}`, 1)
	if _, err := ParseInstallManifest([]byte(broken)); err == nil {
		t.Fatal("a file whose partition is not in the table was accepted — " +
			"that is a guessed offset on real hardware")
	}
}

// Narrowing by chip must never collapse to a single answer: the chip is
// shared by dozens of boards with different radios and pin maps.
func TestChipNarrowsTheListButNeverPicksFromIt(t *testing.T) {
	rel := FirmwareRelease{Targets: []FirmwareTarget{
		{Board: "heltec-v3", MCU: "esp32s3"},
		{Board: "tbeam-s3-core", MCU: "esp32s3"},
		{Board: "tlora-t3s3-v1", MCU: "esp32s3"},
		{Board: "tbeam", MCU: "esp32"},
	}}
	got := rel.TargetsForChip("esp32s3")
	if len(got) != 3 {
		t.Fatalf("esp32s3 narrowed to %d, want 3", len(got))
	}
	for _, tg := range got {
		if tg.MCU != "esp32s3" {
			t.Fatalf("%s is not an esp32s3 board", tg.Board)
		}
	}
	// An unknown chip must show everything rather than silently hide options.
	if all := rel.TargetsForChip(""); len(all) != 4 {
		t.Fatalf("unknown chip narrowed the list to %d — a board we could not "+
			"identify must not have its options hidden", len(all))
	}
}

// The commands are shown to a person before they run, so they must be
// reproducible by hand exactly as printed.
func TestFlashCommandsEraseFirstAndPlaceEveryFile(t *testing.T) {
	plan, err := ParseInstallManifest([]byte(heltecV3Manifest))
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for _, f := range plan.Files {
		paths[f.Name] = "/tmp/" + f.Name
	}
	cmds := FlashCommands("esptool", "/dev/cu.usbserial-0001", plan, paths)
	if len(cmds) != 2 {
		t.Fatalf("want erase then write, got %d commands", len(cmds))
	}
	if !strings.Contains(strings.Join(cmds[0], " "), "erase-flash") {
		t.Fatalf("the first command is not an erase: %v", cmds[0])
	}
	write := strings.Join(cmds[1], " ")
	for _, f := range plan.Files {
		if !strings.Contains(write, paths[f.Name]) {
			t.Fatalf("%s is in the plan but not in the write command", f.Name)
		}
	}
	if !strings.Contains(write, "0x0 ") || !strings.Contains(write, "0x670000") {
		t.Fatalf("offsets missing from the write command: %s", write)
	}
}

// A mistyped region must be an ERROR, not a zero value.
//
// RegionValue answers 0 for anything it cannot resolve, which suits a dev
// tool that would rather show an obviously wrong radio than refuse to start.
// It is the wrong answer for a person setting a region: 0 is UNSET, and UNSET
// is the one value that stops the radio transmitting at all. A typo would
// silently produce a radio that looks configured and is mute.
func TestAMistypedRegionIsRefusedRatherThanWrittenAsUnset(t *testing.T) {
	if _, err := ParseRegion("EU868"); err == nil {
		t.Fatal("a misspelt region was accepted — it would have been written " +
			"as UNSET, which silences the radio")
	}
	got, err := ParseRegion("EU_868")
	if err != nil || got != regionEU868 {
		t.Fatalf("EU_868 = (%d, %v)", got, err)
	}
	// The forgiving helper still behaves as its own comment promises.
	if RegionValue("EU868") != 0 {
		t.Fatal("RegionValue stopped answering 0 for the unresolvable")
	}
}

// Setting the region must change the region and nothing else. A person who
// asked for one thing must not have their preset or hop limit rewritten to
// whatever this build happens to default to — those decide how the radio
// behaves on shared air.
func TestSettingTheRegionTouchesNothingElse(t *testing.T) {
	r := regionEU868
	plan, err := PlanLoRaApply(LoRaSetting{Region: &r})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.Summary, " ")
	if !strings.Contains(joined, "EU_868") {
		t.Fatalf("the summary does not name the region: %q", joined)
	}
	for _, word := range []string{"preset", "hop limit"} {
		if strings.Contains(joined, word) {
			t.Fatalf("asked only for a region, but the plan also sets the %s: %q",
				word, joined)
		}
	}
	if _, err := PlanLoRaApply(LoRaSetting{}); err == nil {
		t.Fatal("a plan that changes nothing was accepted")
	}
}

// The radio tells us how much room it has, and we must believe the number it
// gave MINUS what we handed over since. The report is a snapshot; every
// packet sent against it ages it.
func TestCreditCountsWhatWeSentSinceTheReport(t *testing.T) {
	r := &Radio{opts: Options{}.withDefaults()}

	// Before the radio has said anything: not zero, UNKNOWN. A sender must
	// be allowed to proceed, because the first message is usually what
	// makes the firmware report at all.
	if c := r.Credit(); c.Known {
		t.Fatalf("a radio that has not reported claimed to know: %+v", c)
	}

	r.absorb(&FromRadioMsg{Queue: &QueueStatus{Free: 3, Maxlen: 16}})
	if c := r.Credit(); !c.Known || c.Packets != 3 {
		t.Fatalf("after a report of 3 free, credit = %+v", c)
	}

	// Two packets handed over, no new report: two fewer than the radio said.
	r.inFlight = 2
	if c := r.Credit(); c.Packets != 1 {
		t.Fatalf("credit = %d after sending 2 against a report of 3, want 1", c.Packets)
	}

	// Never negative, and a full queue says when to come back rather than
	// leaving the sender to guess.
	r.inFlight = 9
	c := r.Credit()
	if c.Packets != 0 {
		t.Fatalf("credit = %d, want 0 — a queue cannot be less than empty", c.Packets)
	}
	if c.RetryAfter == 0 {
		t.Fatal("a full radio gave no hint when to ask again; at ~2s of airtime " +
			"per packet a sender that guesses will just burn wakeups")
	}
}

// A refusal is the one thing the old counters could not see: txCount rose
// whether or not the firmware took the packet.
func TestARefusedPacketIsCountedAsRefused(t *testing.T) {
	r := &Radio{opts: Options{}.withDefaults()}
	r.absorb(&FromRadioMsg{Queue: &QueueStatus{Res: 0, Free: 5, Maxlen: 16}})
	r.absorb(&FromRadioMsg{Queue: &QueueStatus{Res: 3, Free: 0, Maxlen: 16}})

	free, maxlen, refused, known := r.QueueState()
	if !known || free != 0 || maxlen != 16 {
		t.Fatalf("queue state = (%d, %d, known %v)", free, maxlen, known)
	}
	if refused != 1 {
		t.Fatalf("refused = %d, want 1 — a packet the firmware declined must "+
			"not read as one that went on the air", refused)
	}
}

// The wire shape, from the reference protobufs: res=1, free=2, maxlen=3,
// mesh_packet_id=4, carried in FromRadio field 11.
func TestQueueStatusIsReadFromFieldEleven(t *testing.T) {
	inner := []byte{
		0x08, 0x00, // res = 0
		0x10, 0x07, // free = 7
		0x18, 0x10, // maxlen = 16
		0x20, 0x2a, // mesh_packet_id = 42
	}
	frame := append([]byte{0x5a, byte(len(inner))}, inner...) // field 11, wire type 2
	msg, err := DecodeFromRadio(frame)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Queue == nil {
		t.Fatal("field 11 was skipped — this is the report that says the radio " +
			"is full, and skipping it is what let us flood it")
	}
	if msg.Queue.Free != 7 || msg.Queue.Maxlen != 16 || msg.Queue.PacketID != 42 {
		t.Fatalf("decoded %+v", msg.Queue)
	}
	if !msg.Queue.Accepted() {
		t.Fatal("res 0 should read as accepted")
	}
	for _, f := range msg.Skipped {
		if f == 11 {
			t.Fatal("field 11 is still listed as skipped")
		}
	}
}
