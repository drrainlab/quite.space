// TR-0a — the headless terminal is a NORMAL node, and nothing else.
//
// The whole External Terminal epic stands on one invariant: quite-terminal
// is a host over the existing node/kernel, never a second implementation of
// the protocol. This test is that invariant in motion, with no UI and no
// HTTP anywhere: a runtime opens a data dir, mints its identity, survives a
// restart with the same identity, joins a space through the ordinary pass
// flow, survives another restart, and publishes — and the message reaches
// the other member through the ordinary sync loop, exactly once.
package node

import (
	"testing"
	"time"
)

func TestHeadlessTerminalUsesNormalNodeIdentity(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	setPersonalRelay(t, alice, addr)
	tid, err := alice.CreateSpace("✉ mailbox")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(tid, 1, 1, addr)
	if err != nil {
		t.Fatal(err)
	}

	// The terminal's identity is minted by node.Open itself — no ceremony —
	// and a restart hands back the SAME principal and device.
	dir := t.TempDir()
	term := openRuntime(t, dir, "gateway")
	dev := term.Device.ID
	fp := term.Principal.Fingerprint()
	term.Close()

	term = openRuntime(t, dir, "gateway")
	if term.Device.ID != dev {
		t.Fatal("a restart minted a different device")
	}
	if term.Principal.Fingerprint() != fp {
		t.Fatal("a restart minted a different principal")
	}

	// Join through the ordinary pass flow, headlessly.
	setPersonalRelay(t, term, addr)
	req, err := term.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, term, req, JoinReady)
	term.Close()

	// Restart AFTER the join: membership, epochs and relay settings all come
	// back from the keystore, and the sync loop resumes on its own.
	term = openRuntime(t, dir, "gateway")
	defer term.Close()
	if _, err := term.Say(tid, "from the headless terminal", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 30*time.Second, "the headless terminal's message never arrived", func() bool {
		return countMsg(t, alice, tid, "from the headless terminal") >= 1
	})
	time.Sleep(3 * time.Second)
	if n := countMsg(t, alice, tid, "from the headless terminal"); n != 1 {
		t.Fatalf("the message applied %d times", n)
	}
}
