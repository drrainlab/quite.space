package node

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/pairing"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/lan"
	"time"
)

// THE LIVE T7 GATE: one person, two devices, different keys, different
// chains — the user story the whole MD wave was for. A parent runtime pairs
// a fresh data dir over real TLS; the child then opens as a SECONDARY —
// no root seed, principal read from its certificate — and the two devices
// exchange messages through a relay as one person.
func TestPairingCreatesASecondDeviceThatSpeaksAsThePerson(t *testing.T) {
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
	if _, err := parent.Say(tid, "written before the second device existed", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	// The person's OWN name for the room rides to the person's other device.
	if err := parent.SetLocalTitle(tid, "my workshop, my name"); err != nil {
		t.Fatal(err)
	}

	host, err := parent.BeginPairing("127.0.0.1:0", now)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()

	childDir := t.TempDir()
	childDigits := make(chan string, 1)
	childErr := make(chan error, 1)
	go func() {
		childErr <- JoinAsPairedDevice(childDir, []byte("test passphrase"), host.OfferBytes(),
			func(digits string) bool { childDigits <- digits; return true }, now)
	}()

	attempt, err := host.Accept(now)
	if err != nil {
		t.Fatal(err)
	}
	// BOTH SCREENS, one number — the human check, asserted mechanically.
	if got := <-childDigits; got != attempt.Digits {
		t.Fatalf("the two screens disagree: parent=%q child=%q", attempt.Digits, got)
	}
	if err := attempt.Approve(now); err != nil {
		t.Fatal(err)
	}
	if err := <-childErr; err != nil {
		t.Fatal(err)
	}

	// The child opens as a SECONDARY of the same person.
	child := openRuntime(t, childDir, "")
	defer child.Close()
	if child.PrincipalID != parent.PrincipalID {
		t.Fatal("the paired device is not the same person")
	}
	if child.Principal != nil {
		t.Fatal("the paired device holds a root keypair — the seed travelled")
	}
	if child.Device.ID == parent.Device.ID {
		t.Fatal("the paired device shares the parent's device key")
	}
	if _, known := child.ident.certificateFor(child.Device.ID); !known {
		t.Fatal("the child does not trust its own certificate")
	}
	if _, known := parent.ident.certificateFor(child.Device.ID); !known {
		t.Fatal("the parent never learned the device it just certified")
	}
	if got := child.ks.Spaces[tid].LocalTitle; got != "my workshop, my name" {
		t.Fatalf("the person's own name did not reach the second device: %q", got)
	}

	// The space came in the freight, with POST-ROTATION epochs: the child
	// reads what the parent writes from now on, through an ordinary relay.
	setPersonalRelay(t, child, addr)
	if _, err := parent.Say(tid, "hello, second device", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parent.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	waitUntilMsg(t, child, addr, tid, "hello, second device")
	if countMsg(t, child, tid, "written before the second device existed") != 1 {
		t.Fatal("history did not reach the second device")
	}

	// And the person answers FROM the second device, over its own chain.
	if _, err := child.Say(tid, "hello from the person's other hand", SayOptions{}); err != nil {
		t.Fatalf("the secondary cannot speak: %v", err)
	}
	if _, _, err := child.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	waitUntilMsg(t, parent, addr, tid, "hello from the person's other hand")
}

// The freight is the ONE artifact that crosses machines during pairing, so
// what must never travel is asserted against its actual bytes: no root
// seed, no controller seeds, no self-terminal seed, no device secrets.
func TestFreightCarriesNoRootSecrets(t *testing.T) {
	parent := openRuntime(t, t.TempDir(), "gleb")
	defer parent.Close()
	if _, err := parent.CreateSpace("owned room"); err != nil {
		t.Fatal(err)
	}
	parent.mu.Lock()
	doc := parent.buildFreightLocked()
	secrets := [][]byte{parent.ks.PrincipalSeed, parent.ks.DeviceSeed, parent.ks.SelfTerminalSeed}
	for _, seed := range parent.ks.TerminalSeeds {
		secrets = append(secrets, seed)
	}
	parent.mu.Unlock()
	for i, s := range secrets {
		if len(s) == 0 {
			t.Fatalf("secret %d is empty — the assertion would prove nothing", i)
		}
		if bytes.Contains(doc, s) {
			t.Fatalf("secret %d travelled in the freight", i)
		}
	}
}

// Pairing writes an identity, and an identity must never be written over an
// existing one — the same rule restore already enforces, for the same
// reason.
func TestPairedChildRefusesANonEmptyDataDir(t *testing.T) {
	dir := t.TempDir()
	old := openRuntime(t, dir, "somebody")
	old.Close()
	err := JoinAsPairedDevice(dir, []byte("test passphrase"), nil,
		func(string) bool { return true }, uint64(time.Now().Unix()))
	if err == nil {
		t.Fatal("pairing into a data dir that already holds a keystore")
	}
}

// A secondary holds no root seed, so it cannot mint pairing offers: the
// authority to add devices does not spread by pairing (MD-2 draws that
// line; delegation is its own future wave).
func TestASecondaryCannotMintPairingOffers(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	_ = addr
	now := uint64(time.Now().Unix())

	parent := openRuntime(t, t.TempDir(), "gleb")
	defer parent.Close()
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
	defer child.Close()
	if _, err := child.BeginPairing("127.0.0.1:0", now); err == nil {
		t.Fatal("a secondary minted a pairing offer without a root seed")
	}
}

// waitUntilMsg pulls from the relay until the text lands, or fails loudly.
func waitUntilMsg(t *testing.T, rt *Runtime, addr string, tid id.TerminalID, text string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, _ = rt.PullFromRelay(addr)
		if countMsg(t, rt, tid, text) >= 1 {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("%q never arrived", text)
}

// DHCP DOES NOT CARE ABOUT CEREMONIES: the offer names an address that no
// longer answers — the SAME bytes on both sides, as reality has it — and
// the child finds the parent anyway, by the beacon only the offer's holder
// can derive. Loopback UDP, like every LAN test.
func TestBeaconFindsAParentWhoseAddressMoved(t *testing.T) {
	now := uint64(time.Now().Unix())
	parent := openRuntime(t, t.TempDir(), "gleb")
	defer parent.Close()

	// The move, simulated honestly: the offer was minted for an address
	// nothing listens on any more, while the parent's REAL door lives at a
	// port the offer never heard of. Both sides hold identical offer bytes.
	offer, err := pairing.NewOffer(rand.Reader, "127.0.0.1:9", now)
	if err != nil {
		t.Fatal(err)
	}
	node, err := lan.NewNode()
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	conns := make(chan *lan.Conn, 4)
	port, err := node.Listen("127.0.0.1:0", func(c *lan.Conn) { conns <- c })
	if err != nil {
		t.Fatal(err)
	}
	host := &PairingHost{
		r: parent, node: node, ceremony: pairing.NewParentCeremony(offer),
		offer: offer.Encode(), conns: conns, port: port,
		beacon: pairing.BeaconID(offer.Secret), stop: make(chan struct{}),
	}
	defer host.Close()
	udp := "127.0.0.1:47999"
	host.Announce(udp)

	childDir := t.TempDir()
	childErr := make(chan error, 1)
	go func() {
		childErr <- JoinAsPairedDeviceVia(childDir, []byte("test passphrase"), host.OfferBytes(),
			udp, "", func(string) bool { return true }, now)
	}()
	attempt, err := host.Accept(now)
	if err != nil {
		t.Fatal(err)
	}
	if err := attempt.Approve(now); err != nil {
		t.Fatal(err)
	}
	if err := <-childErr; err != nil {
		t.Fatalf("the beacon did not rescue the moved address: %v", err)
	}
}

// A FRESHLY PAIRED DEVICE HEARS OTHER PEOPLE. In a space somebody ELSE owns
// the phone has authored nothing, so no peer learns its device from a frame
// and the owner's Members() does not list it — the two ways recipients used
// to be found. The person's own certified devices are recipients of
// everything the person is in, by the fact of being the person: measured
// on the first paired phone as "no history, no reactions, no member cards".
func TestAPairedDeviceHearsOtherPeopleInASpaceItDidNotCreate(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	now := uint64(time.Now().Unix())

	// Alice owns the room; Gleb is a member on his laptop.
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	setPersonalRelay(t, alice, addr)
	tid, err := alice.CreateSpace("alice's room")
	if err != nil {
		t.Fatal(err)
	}
	laptop := openRuntime(t, t.TempDir(), "gleb")
	defer laptop.Close()
	setPersonalRelay(t, laptop, addr)
	invite, err := alice.MintInvite(tid, laptop.Device.ID, laptop.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := laptop.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "alice, before the phone existed", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := alice.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	waitUntilMsg(t, laptop, addr, tid, "alice, before the phone existed")

	// Gleb pairs a phone. The phone has never written a byte anywhere.
	phone := pairChild(t, laptop, now)
	setPersonalRelay(t, phone, addr)

	// The laptop's ordinary push must now address the phone too, with the
	// WHOLE log — including alice's words the phone never saw.
	if _, _, err := laptop.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	waitUntilMsg(t, phone, addr, tid, "alice, before the phone existed")
	if len(phoneMembers(t, phone, tid)) < 2 {
		t.Fatal("the phone does not see the other person as a member")
	}
}

func phoneMembers(t *testing.T, rt *Runtime, tid id.TerminalID) []terminals.MemberCard {
	t.Helper()
	cards, err := rt.Members(tid)
	if err != nil {
		t.Fatal(err)
	}
	return cards
}
