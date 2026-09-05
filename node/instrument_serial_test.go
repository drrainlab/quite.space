package node

// The resident USB stand (QI-M4), gated off-hardware: a net.Pipe plays
// the serial wire and the externalDevice fixture plays the firmware, so
// the whole grammar — TIME, PRINCIPAL, ENROLL?, ENROLLMENT, PROVISION,
// FRAME — runs against a real runtime with no board on the desk. The
// doctrinal pin rides along: DevIngest stays FALSE for the entire test,
// because the resident stand enters beside the flag, not through the
// tokened HTTP door the flag guards.

import (
	"bufio"
	"encoding/hex"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/enrollment"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
)

func TestResidentStandSpeaksTheWireGrammar(t *testing.T) {
	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	tid, err := owner.CreateSpace("workbench")
	if err != nil {
		t.Fatal(err)
	}
	if owner.DevIngest {
		t.Fatal("the pin is broken before it is set: DevIngest must start false")
	}

	wire, board := net.Pipe()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		owner.standSession(stop, wire, tid)
		close(done)
	}()
	defer func() { close(stop); board.Close(); <-done }()

	sc := bufio.NewScanner(board)
	sc.Buffer(make([]byte, 1<<16), 1<<16)
	readLine := func() string {
		if !sc.Scan() {
			t.Fatalf("the wire went quiet: %v", sc.Err())
		}
		return strings.TrimSpace(sc.Text())
	}

	// The stand speaks first, and in the CLI stand's exact order: the
	// clock floor, then who the controller is, then the invitation.
	if l := readLine(); !strings.HasPrefix(l, "QI TIME ") {
		t.Fatalf("first line is not time: %q", l)
	}
	if l := readLine(); l != "QI PRINCIPAL "+owner.PrincipalID.Hex() {
		t.Fatalf("principal line wrong: %q", l)
	}
	if l := readLine(); l != "QI ENROLL?" {
		t.Fatalf("no invitation: %q", l)
	}

	// The board answers with an enrollment minted on the device.
	ext := newExternalDevice(t, owner.PrincipalID, tid, "Heltec")
	if _, err := board.Write([]byte("QI ENROLLMENT " + hex.EncodeToString(ext.enroll) + "\n")); err != nil {
		t.Fatal(err)
	}
	provLine := readLine()
	if !strings.HasPrefix(provLine, "QI PROVISION ") {
		t.Fatalf("enrollment did not earn a provision: %q", provLine)
	}
	prov, err := hex.DecodeString(strings.TrimPrefix(provLine, "QI PROVISION "))
	if err != nil {
		t.Fatal(err)
	}
	ext.provision(t, prov)

	st := owner.SerialInstrumentStatus()
	if st.InstrumentID != ext.part.TerminalID.Hex() {
		t.Fatalf("status does not carry the enrolled instrument: %+v", st)
	}

	// A reading crosses the wire and lands in the reducer — with the
	// HTTP door still shut.
	frame := ext.reading(t, 217, uint64(time.Now().Unix()))
	if _, err := board.Write([]byte("QI FRAME " + hex.EncodeToString(frame) + "\n")); err != nil {
		t.Fatal(err)
	}
	// Delivered means acknowledged: the stand names the frame it applied
	// by event id, and only that line lets the board clear its owed frame.
	eid := id.EventIDOf(frame)
	if l := readLine(); l != "QI ACK "+hex.EncodeToString(eid[:]) {
		t.Fatalf("the applied frame was not acknowledged by event id: %q", l)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if mag, ok := guestTemperature(t, owner, tid, ext.part.TerminalID); ok {
			if mag != 217 {
				t.Fatalf("reducer holds %d, wire carried 217", mag)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the frame never reached the reducer")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if owner.DevIngest {
		t.Fatal("the resident stand must not have opened the HTTP door")
	}

	// A reboot that kept the USB session: the board asks for its clock,
	// and a boot marker earns the whole greeting again.
	if _, err := board.Write([]byte("QI TIME?\n")); err != nil {
		t.Fatal(err)
	}
	if l := readLine(); !strings.HasPrefix(l, "QI TIME ") {
		t.Fatalf("a clockless board's question went unanswered: %q", l)
	}
	if _, err := board.Write([]byte("QI NOTE SensorTerminal ready\n")); err != nil {
		t.Fatal(err)
	}
	if l := readLine(); !strings.HasPrefix(l, "QI TIME ") {
		t.Fatalf("no clock after a boot marker: %q", l)
	}
	if l := readLine(); l != "QI PRINCIPAL "+owner.PrincipalID.Hex() {
		t.Fatalf("no principal after a boot marker: %q", l)
	}
	if l := readLine(); l != "QI ENROLL?" {
		t.Fatalf("no invitation after a boot marker: %q", l)
	}
}

// Arming persists and re-arms: the settings blob carries (port, space)
// across a close/open, and the explicit re-arm call — never Open itself —
// brings the stand back. The port is deliberately nonexistent: waiting
// for an absent board is a STATE, not an error.
func TestResidentStandArmingSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	owner := openRuntime(t, dir, "owner")
	tid, err := owner.CreateSpace("workbench")
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.AttachSerialInstrument(tid, "/dev/cu.does-not-exist"); err != nil {
		t.Fatal(err)
	}
	st := owner.SerialInstrumentStatus()
	if !st.Armed || st.Space != tid.Hex() {
		t.Fatalf("stand not armed after attach: %+v", st)
	}
	owner.Close()

	again := openRuntime(t, dir, "owner")
	defer again.Close()
	if s := again.SerialInstrumentStatus(); s.Armed {
		t.Fatal("Open must not arm the stand by itself — hosts do, explicitly")
	}
	again.ArmInstrumentSerialFromSettings()
	st = again.SerialInstrumentStatus()
	if !st.Armed || st.Space != tid.Hex() || st.Port != "/dev/cu.does-not-exist" {
		t.Fatalf("re-arm lost the persisted choice: %+v", st)
	}
	if err := again.DetachSerialInstrument(); err != nil {
		t.Fatal(err)
	}
	if s := again.SerialInstrumentStatus(); s.Armed {
		t.Fatal("detach left the stand armed")
	}
	if cfg := again.GetSettings().InstrumentSerial; cfg != nil {
		t.Fatal("detach left the arming persisted")
	}
}

// The external half of public telemetry (QI-B1 Ф3): a real board's
// enrollment lands in a BROADCAST space. The simulated path learned this
// fork long ago; the external path rotated an epoch that cannot exist in
// a plaintext space and died on "no members". Now both halves fork the
// same way — no plane, no key to turn, an attested writer binding
// instead — and the provision carries NO epoch frames, honestly. (The C
// core as shipped still refuses an epochless provision: plaintext
// emission from a real board is QI-B2's named slice, and this test
// deliberately claims only enrollment.)
func TestAnExternalSensorEnrollsIntoABroadcastSpace(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "owner")
	defer rt.Close()
	tid, err := rt.CreateSpaceWithOptions("balcony", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ext := newExternalDevice(t, rt.PrincipalID, tid, "Heltec")
	prov, iid, err := rt.AttachInstrumentByEnrollment(tid, ext.enroll, 1000)
	if err != nil {
		t.Fatalf("broadcast enrollment refused: %v", err)
	}
	if iid != ext.part.TerminalID {
		t.Fatal("instrument id is not the device's terminal")
	}
	p, err := enrollment.DecodeProvision(prov)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.EpochFrames) != 0 {
		t.Fatalf("a plaintext space handed out %d epoch frames — no plane exists to key", len(p.EpochFrames))
	}
	if len(p.CertFrame) == 0 {
		t.Fatal("provision carries no certificate")
	}

	pol := policyOf(t, rt, tid)
	if !pol.AllowsWriter(rt.PrincipalID, ext.dev.ID) {
		t.Fatal("the board's device is not an attested writer")
	}
	if !pol.AllowsWriter(rt.PrincipalID, rt.Device.ID) {
		t.Fatal("binding the board cost the owner their own binding")
	}
	_ = rt.withSpace(tid, func(st *spaceState) error {
		if st.space.InstrumentCount() != 0 {
			t.Fatalf("a plaintext space grew an instrument plane: %d devices", st.space.InstrumentCount())
		}
		return nil
	})

	// Idempotent (owner's amendment 5): the same freight again earns a
	// fresh provision, no second binding.
	prov2, _, err := rt.AttachInstrumentByEnrollment(tid, ext.enroll, 1001)
	if err != nil {
		t.Fatalf("re-enrollment refused: %v", err)
	}
	if _, err := enrollment.DecodeProvision(prov2); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, w := range policyOf(t, rt, tid).Writers {
		if w.Device == ext.dev.ID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("re-enrollment left %d bindings for one device", n)
	}

	// Detach is the plaintext analogue of the key turning: the binding
	// goes back.
	if err := rt.DetachInstrument(tid, iid); err != nil {
		t.Fatal(err)
	}
	if policyOf(t, rt, tid).AllowsWriter(rt.PrincipalID, ext.dev.ID) {
		t.Fatal("detach left the board an attested writer")
	}
	// And the card is gone from every screen — the manifest stays in the
	// log, but membership is the binding, not the manifest.
	for _, c := range mustMembers(t, rt, tid) {
		if c.Terminal == iid {
			t.Fatal("a detached instrument still has a card")
		}
	}
}

func mustProvision(t *testing.T, rt *Runtime, tid id.TerminalID, ext *externalDevice) []byte {
	t.Helper()
	prov, _, err := rt.AttachInstrumentByEnrollment(tid, ext.enroll, 1001) // idempotent: a fresh provision
	if err != nil {
		t.Fatal(err)
	}
	return prov
}

func mustMembers(t *testing.T, rt *Runtime, tid id.TerminalID) []terminals.MemberCard {
	t.Helper()
	cards, err := rt.Members(tid)
	if err != nil {
		t.Fatal(err)
	}
	return cards
}

// A GHOST CAN BE ENDED. A card whose keystore record is gone — an identity
// re-minted by a wiped board, a card synced from elsewhere — still has its
// manifest in the log; detaching it turns the key on the manifest's
// device, and the card leaves with the epoch. The owner met the old
// refusal as "unknown instrument" beside a card nothing could end.
func TestAGhostInstrumentCanBeDetachedWithoutARecord(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "owner")
	defer rt.Close()
	tid, err := rt.CreateSpace("workbench")
	if err != nil {
		t.Fatal(err)
	}
	ext := newExternalDevice(t, rt.PrincipalID, tid, "Heltec")
	if _, _, err := rt.AttachInstrumentByEnrollment(tid, ext.enroll, 1000); err != nil {
		t.Fatal(err)
	}
	// Before its first reading an instrument has no card: membership is
	// speaking under the epoch, and it has not spoken yet.
	for _, c := range mustMembers(t, rt, tid) {
		if c.Terminal == ext.part.TerminalID {
			t.Fatal("an instrument that never spoke has a card")
		}
	}
	// It speaks once — the frame teaches every replica which device it
	// is, and the card appears.
	ext.provision(t, mustProvision(t, rt, tid, ext))
	rt.DevIngest = true
	if _, err := rt.IngestInstrumentFrames(tid, ext.part.TerminalID,
		[][]byte{ext.reading(t, 200, uint64(time.Now().Unix()))}); err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, c := range mustMembers(t, rt, tid) {
		if c.Terminal == ext.part.TerminalID {
			seen = true
		}
	}
	if !seen {
		t.Fatal("a speaking instrument has no card")
	}
	// The record vanishes (a wiped board's old life), the card stays.
	rt.mu.Lock()
	rt.ks.Instruments = nil
	rt.mu.Unlock()
	if err := rt.DetachInstrument(tid, ext.part.TerminalID); err != nil {
		t.Fatalf("a ghost could not be ended: %v", err)
	}
	for _, c := range mustMembers(t, rt, tid) {
		if c.Terminal == ext.part.TerminalID {
			t.Fatal("the ghost's card survived its detachment")
		}
	}
	// Ending it twice is answered, not refused: the manifest is still
	// there to name the device, and turning an already-turned key is safe.
	if err := rt.DetachInstrument(tid, ext.part.TerminalID); err != nil {
		t.Fatalf("a second detach was refused: %v", err)
	}
}
