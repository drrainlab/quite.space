package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// pairChild runs the full pairing flow and returns the opened secondary.
func pairChild(t *testing.T, parent *Runtime, now uint64) *Runtime {
	t.Helper()
	host, err := parent.BeginPairing("127.0.0.1:0", now)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	childDir := t.TempDir()
	childErr := make(chan error, 1)
	go func() {
		childErr <- JoinAsPairedDevice(childDir, []byte("test passphrase"), host.OfferBytes(),
			func(string) bool { return true }, now)
	}()
	attempt, err := host.Accept(now)
	if err != nil {
		t.Fatal(err)
	}
	if err := attempt.Approve(now); err != nil {
		t.Fatal(err)
	}
	if err := <-childErr; err != nil {
		t.Fatal(err)
	}
	child := openRuntime(t, childDir, "")
	t.Cleanup(child.Close)
	return child
}

// THE POINT OF REVOCATION (decision 3): a lost second device stops SPEAKING
// at once — permanently, not held — while everything it said before the
// revocation stands (ADR-002:50-52). And because revoking rotates every
// owned space, it stops READING them from that moment too.
func TestRevokedDeviceStopsSpeakingButItsHistoryStands(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	now := uint64(time.Now().Unix())

	parent := openRuntime(t, t.TempDir(), "gleb")
	defer parent.Close()
	setPersonalRelay(t, parent, addr)
	tid, err := parent.CreateSpace("the workshop")
	if err != nil {
		t.Fatal(err)
	}
	child := pairChild(t, parent, now)
	setPersonalRelay(t, child, addr)

	// The child speaks once, legitimately.
	if _, err := child.Say(tid, "said before the phone was lost", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := child.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	waitUntilMsg(t, parent, addr, tid, "said before the phone was lost")

	// THE PHONE IS LOST. The authority revokes it.
	if err := parent.RevokeDevice(child.Device.ID); err != nil {
		t.Fatal(err)
	}

	// The thief keeps typing. The words never land, and they are REFUSED,
	// not held: no future control event un-revokes these bytes.
	if _, err := child.Say(tid, "the thief speaks", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := child.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, _ = parent.PullFromRelay(addr)
		time.Sleep(200 * time.Millisecond)
	}
	if countMsg(t, parent, tid, "the thief speaks") != 0 {
		t.Fatal("a revoked device's message was admitted")
	}
	revokedRefusal := false
	for _, ref := range parent.IngressRefusals() {
		if ref.Reason == "revoked" {
			revokedRefusal = true
		}
	}
	if !revokedRefusal {
		t.Fatal("the refusal was not diagnosed as a revocation")
	}
	if h, err := parent.ingressHold(); err == nil {
		if items, _ := h.List(); len(items) != 0 {
			t.Fatalf("%d revoked frame(s) sit in the hold — revoked must never be held", len(items))
		}
	}
	// What was said before the loss still stands, exactly once.
	if got := countMsg(t, parent, tid, "said before the phone was lost"); got != 1 {
		t.Fatalf("pre-revocation history: %d, want exactly 1", got)
	}

	// The rotation happened: the parent's next words are sealed under an
	// epoch the lost device never receives.
	if _, err := parent.Say(tid, "sealed away from the lost phone", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parent.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, _ = child.PullFromRelay(addr)
		time.Sleep(200 * time.Millisecond)
	}
	if countMsg(t, child, tid, "sealed away from the lost phone") != 0 {
		t.Fatal("a revoked device read a post-rotation message")
	}
}

// Revocation is root authority: a secondary cannot revoke, and nobody can
// revoke the device they are standing on.
func TestRevocationGuards(t *testing.T) {
	now := uint64(time.Now().Unix())
	parent := openRuntime(t, t.TempDir(), "gleb")
	defer parent.Close()
	child := pairChild(t, parent, now)

	if err := child.RevokeDevice(parent.Device.ID); err == nil {
		t.Fatal("a secondary revoked the authority")
	}
	if err := parent.RevokeDevice(parent.Device.ID); err == nil {
		t.Fatal("the authority revoked the device it is standing on")
	}
	if err := parent.RevokeDevice(id.DeviceID{0xEE}); err == nil {
		t.Fatal("revoked a device that was never certified")
	}
	// And the real one still works after all three refusals.
	if err := parent.RevokeDevice(child.Device.ID); err != nil {
		t.Fatal(err)
	}
}
