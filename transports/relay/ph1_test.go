// PH-1 acceptance: draining a mailbox needs a CAPABILITY, not knowledge of
// an address. Before this, "can compute the hint" meant "can empty the box"
// — and for a public space the hint derives from the space id, which is the
// read capability every reader holds. Anyone with a link could silently
// delete other people's contributions and media answers.
package relay

import (
	"bytes"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
)

func TestLegacyCollectIsRefusedNotAnsweredEmpty(t *testing.T) {
	s, cs := testServer()
	cap := make([]byte, CapLen)
	s.store.Put(Item{DestinationHint: string(CollectHint(cap)), Ciphertext: []byte("mail")})

	r := s.handle(&Msg{Type: msgCollect, Hints: [][]byte{CollectHint(cap)}}, cs)
	if r.Type != msgError || !strings.Contains(r.Reason, "capability") {
		t.Fatalf("legacy collect must be refused with a diagnosable reason: %+v", r)
	}
	// The refusal must not be a silent drain: the mail is still there.
	if got := s.handle(&Msg{Type: msgCollectCap, Caps: [][]byte{cap}}, cs); len(got.Items) != 1 {
		t.Fatalf("the refused collect took the mail anyway: %d items left", len(got.Items))
	}
}

func TestOnlyTheCapabilityHolderDrains(t *testing.T) {
	s, cs := testServer()
	mine, err := NewReplyCap()
	if err != nil {
		t.Fatal(err)
	}
	hint := CollectHint(mine)
	s.store.Put(Item{DestinationHint: string(hint), Ciphertext: []byte("for me")})

	// A stranger who OBSERVED the hint — the exact position a relay operator
	// or anyone reading a want bundle is in — cannot turn it into a drain.
	guess := append([]byte(nil), hint...)
	guess = append(guess, make([]byte, CapLen-HintLen)...)
	if r := s.handle(&Msg{Type: msgCollectCap, Caps: [][]byte{guess}}, cs); len(r.Items) != 0 {
		t.Fatal("a hint padded to capability length drained the box")
	}
	// And the holder still gets its mail.
	r := s.handle(&Msg{Type: msgCollectCap, Caps: [][]byte{mine}}, cs)
	if len(r.Items) != 1 || !bytes.Equal(r.Items[0], []byte("for me")) {
		t.Fatalf("the capability holder was denied its own mailbox: %+v", r)
	}
}

func TestMalformedCapabilityIsRefused(t *testing.T) {
	s, cs := testServer()
	// A 16-byte hint passed where a capability belongs is the most likely
	// programming mistake in this migration; it must fail loudly rather than
	// quietly draining nothing.
	r := s.handle(&Msg{Type: msgCollectCap, Caps: [][]byte{make([]byte, HintLen)}}, cs)
	if r.Type != msgError || !strings.Contains(r.Reason, "capability") {
		t.Fatalf("a short capability was accepted: %+v", r)
	}
}

// Every namespace that is COLLECTED must address its box by the capability's
// hash — otherwise a writer's hint and a collector's cap disagree and mail
// silently never arrives.
func TestHintsAddressTheirCapabilities(t *testing.T) {
	tid := id.TerminalID{7}
	dev := id.DeviceID{9}
	root := [32]byte{3}
	cases := []struct {
		name string
		hint []byte
		cap  []byte
	}{
		{"terminal mailbox", Hint(tid, 5), Cap(tid, 5)},
		{"device inbox", HintFor(tid, dev, 5), CapFor(tid, dev, 5)},
		{"public ingress (legacy)", HintPublicIngress(tid, 5, 3), CapPublicIngressLegacy(tid, 5, 3)},
		{"public ingress (sealed)", CollectHint(CapPublicIngress(root, 5, 3)), CapPublicIngress(root, 5, 3)},
	}
	for _, c := range cases {
		if !bytes.Equal(c.hint, CollectHint(c.cap)) {
			t.Errorf("%s: hint does not address its capability", c.name)
		}
		if len(c.cap) != CapLen {
			t.Errorf("%s: capability is %d bytes, want %d", c.name, len(c.cap), CapLen)
		}
	}
}

// The sealed ingress capability must NOT be derivable from public knowledge.
// This is the whole difference between PH-2's derivation and the legacy one.
func TestSealedIngressCapabilityIsNotPublic(t *testing.T) {
	tid := id.TerminalID{7}
	root := [32]byte{3} // the owner's secret, never on the wire
	sealed := CapPublicIngress(root, 5, 3)
	public := CapPublicIngressLegacy(tid, 5, 3)
	if bytes.Equal(sealed, public) {
		t.Fatal("the sealed capability collapsed to the publicly derivable one")
	}
	// A different root gives a different mailbox: the root IS the secret.
	if bytes.Equal(sealed, CapPublicIngress([32]byte{4}, 5, 3)) {
		t.Fatal("the ingress capability ignores its root")
	}
}
