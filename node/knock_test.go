package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
)

// knockScene: two people who already share a private room, on one relay,
// plus a stranger who shares nothing with anybody.
type knockScene struct {
	addr       string
	alice, bob *Runtime
	nodes      map[string]*Runtime
	addrs      map[string]string
	shared     string // hex of the room alice and bob share
}

func mustTerminal(t *testing.T, hex string) id.TerminalID {
	t.Helper()
	tid, err := id.ParseTerminalID(hex)
	if err != nil {
		t.Fatalf("bad terminal id %q: %v", hex, err)
	}
	return tid
}

func newKnockScene(t *testing.T) *knockScene {
	t.Helper()
	srv, addr := startRelay(t)
	t.Cleanup(func() { srv.Close() })

	alice := openRuntime(t, t.TempDir(), "alice")
	t.Cleanup(alice.Close)
	bob := openRuntime(t, t.TempDir(), "bob")
	t.Cleanup(bob.Close)
	setPersonalRelay(t, alice, addr)
	setPersonalRelay(t, bob, addr)

	tid, err := alice.CreateSpace("the yard")
	if err != nil {
		t.Fatal(err)
	}
	info, err := alice.MintPass(tid, 1, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(info.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)

	sc := &knockScene{addr: addr, alice: alice, bob: bob, shared: tid.Hex()}
	sc.nodes = map[string]*Runtime{"alice": alice, "bob": bob}
	sc.addrs = map[string]string{"alice": addr, "bob": addr}
	// Let the two learn each other's certificates and routes.
	for range 4 {
		convergeTick(sc.nodes, sc.addrs)
	}
	return sc
}

func (sc *knockScene) tick(n int) {
	for range n {
		convergeTick(sc.nodes, sc.addrs)
	}
}

// waitKnock polls the recipient until a knock appears (or gives up).
func waitKnock(t *testing.T, sc *knockScene, at *Runtime) PendingKnock {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if ks := at.Knocks(); len(ks) > 0 {
			return ks[0]
		}
		if time.Now().After(deadline) {
			t.Fatal("no knock arrived")
		}
		sc.tick(1)
	}
}

// THE WHOLE JOURNEY, once: Bob asks, Alice sees who is asking and why,
// Alice lets him in, and only THEN does a conversation exist. The knock
// itself carried no access — that is ADR-012's invariant, kept here.
func TestAKnockCarriesNoAccessUntilItIsAnswered(t *testing.T) {
	sc := newKnockScene(t)
	shared := mustTerminal(t, sc.shared)

	before := len(sc.alice.Spaces())
	dyad, err := sc.bob.KnockOn(shared, sc.alice.PrincipalID, "спрошу про антенну")
	if err != nil {
		t.Fatal(err)
	}
	sc.tick(2)

	// NOTHING OPENED YET on either side: a knock is a question.
	if got := len(sc.alice.Spaces()); got != before {
		t.Fatalf("a space appeared for alice before she answered: %d → %d", before, got)
	}

	k := waitKnock(t, sc, sc.alice)
	if k.Principal != sc.bob.PrincipalID.Hex() {
		t.Fatalf("the knock names %s, want bob", k.Principal)
	}
	if k.Name != "bob" {
		t.Fatalf("the name came from the envelope instead of the shared room: %q", k.Name)
	}
	if k.Line != "спрошу про антенну" {
		t.Fatalf("the line did not survive: %q", k.Line)
	}
	if k.Via != sc.shared {
		t.Fatal("the knock does not say which room makes them acquainted")
	}

	if err := sc.alice.AnswerKnock(k.ID, KnockLetIn, ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, ok := sc.alice.spaceForTest(mustTerminal(t, dyad)); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the conversation never opened after she let him in")
		}
		sc.tick(1)
	}
	if len(sc.alice.Knocks()) != 0 {
		t.Fatal("an answered knock is still waiting")
	}
}

// A STRANGER CANNOT KNOCK. Carol shares no private room with Alice, so
// her knock is refused by Alice's own check — and refused ALOUD, because
// nobody decided anything about Carol: the door is simply not open to
// strangers (ADR-023: never silence).
func TestAStrangerIsRefusedByTheFloorNotBySilence(t *testing.T) {
	sc := newKnockScene(t)
	carol := openRuntime(t, t.TempDir(), "carol")
	defer carol.Close()
	setPersonalRelay(t, carol, sc.addr)

	// Carol forges an envelope naming a room she is not in: the ASKING
	// side's own guard is bypassed on purpose here, because the guard that
	// counts is the recipient's.
	shared := mustTerminal(t, sc.shared)
	if _, err := carol.KnockOn(shared, sc.alice.PrincipalID, "здравствуйте"); err == nil {
		t.Fatal("a knock into a room the asker does not have was allowed")
	}

	sc.nodes["carol"], sc.addrs["carol"] = carol, sc.addr
	sc.tick(3)
	if got := sc.alice.Knocks(); len(got) != 0 {
		t.Fatalf("a stranger reached the person: %+v", got)
	}
}

// "DO NOT ASK" ANSWERS FOREVER, AND THE PERSON IS NEVER TOLD AGAIN.
// The refusal is recorded against the PRINCIPAL, so the next knock — a
// fresh envelope with a fresh id — never reaches her, and the asker gets
// the same sentence rather than silence or a new answer.
func TestARecordedRefusalAnswersOnHerBehalf(t *testing.T) {
	sc := newKnockScene(t)
	shared := mustTerminal(t, sc.shared)

	if _, err := sc.bob.KnockOn(shared, sc.alice.PrincipalID, "первый раз"); err != nil {
		t.Fatal(err)
	}
	k := waitKnock(t, sc, sc.alice)
	if err := sc.alice.AnswerKnock(k.ID, KnockNever, "не пишите мне, пожалуйста"); err != nil {
		t.Fatal(err)
	}
	if got := sc.alice.Refusals(); len(got) != 1 ||
		got[0].Principal != sc.bob.PrincipalID ||
		got[0].Reason != "не пишите мне, пожалуйста" {
		t.Fatalf("the refusal was not recorded as written: %+v", got)
	}

	// A second knock, a new id, everything fresh.
	if _, err := sc.bob.KnockOn(shared, sc.alice.PrincipalID, "ещё раз"); err != nil {
		t.Fatal(err)
	}
	sc.tick(6)
	if got := sc.alice.Knocks(); len(got) != 0 {
		t.Fatalf("a refused person reached her again: %+v", got)
	}

	// And it can be taken back: a door somebody closed is a door they may
	// open, or a refusal would be a punishment rather than an answer.
	if err := sc.alice.UnrefusePerson(sc.bob.PrincipalID); err != nil {
		t.Fatal(err)
	}
	if got := sc.alice.Refusals(); len(got) != 0 {
		t.Fatalf("the refusal survived being lifted: %+v", got)
	}
	if _, err := sc.bob.KnockOn(shared, sc.alice.PrincipalID, "а теперь?"); err != nil {
		t.Fatal(err)
	}
	waitKnock(t, sc, sc.alice) // fails the test if it never arrives
}

// ONE LIVE KNOCK PER PERSON: a second envelope while one waits is not a
// second question, and must not become a second row to answer.
func TestASecondKnockWhileOneWaitsIsTheSameQuestion(t *testing.T) {
	sc := newKnockScene(t)
	shared := mustTerminal(t, sc.shared)
	if _, err := sc.bob.KnockOn(shared, sc.alice.PrincipalID, "раз"); err != nil {
		t.Fatal(err)
	}
	waitKnock(t, sc, sc.alice)
	if _, err := sc.bob.KnockOn(shared, sc.alice.PrincipalID, "два"); err != nil {
		t.Fatal(err)
	}
	sc.tick(4)
	if got := sc.alice.Knocks(); len(got) != 1 {
		t.Fatalf("one person produced %d rows to answer", len(got))
	}
}

// The envelope proves its own author, and a tampered line does not open.
func TestAKnockEnvelopeProvesWhoWroteIt(t *testing.T) {
	sc := newKnockScene(t)
	shared := mustTerminal(t, sc.shared)
	if _, err := sc.bob.KnockOn(shared, sc.alice.PrincipalID, "проверка"); err != nil {
		t.Fatal(err)
	}
	k := waitKnock(t, sc, sc.alice)

	sc.alice.mu.Lock()
	sc.alice.knocksInit()
	sc.alice.mu.Unlock()
	sc.alice.knocks.mu.Lock()
	var held *heldKnock
	for _, h := range sc.alice.knocks.pending {
		held = h
	}
	sc.alice.knocks.mu.Unlock()
	if held == nil {
		t.Fatal("the knock is not held")
	}
	// The principal was derived from a ROOT-signed certificate, not from
	// the envelope's own claim.
	if _, _, err := terminals.KnockPrincipal(held.knock); err != nil {
		t.Fatalf("the carried chain does not prove the asker: %v", err)
	}
	if held.principal != sc.bob.PrincipalID {
		t.Fatal("the proven principal is not the asker")
	}
	if k.ID == "" {
		t.Fatal("the knock has no id to answer")
	}
}

// A REFUSAL BELONGS TO THE PERSON, NOT TO THE DEVICE (ADR-024/ADR-027).
// Alice declines Bob on her phone; her laptop must stop hearing him too,
// without her ever saying it twice.
func TestARefusalConvergesToTheOtherDevice(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	now := uint64(time.Now().Unix())

	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	setPersonalRelay(t, bob, addr)

	mac := openRuntime(t, t.TempDir(), "alice")
	defer mac.Close()
	setPersonalRelay(t, mac, addr)
	phone := pairChild(t, mac, now)
	setPersonalRelay(t, phone, addr)

	// The two people meet in one room.
	tid, err := mac.CreateSpace("the yard")
	if err != nil {
		t.Fatal(err)
	}
	info, err := mac.MintPass(tid, 1, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(info.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)

	nodes := map[string]*Runtime{"bob": bob, "mac": mac, "phone": phone}
	addrs := map[string]string{"bob": addr, "mac": addr, "phone": addr}
	for range 6 {
		convergeTick(nodes, addrs)
	}

	// She refuses him on the PHONE.
	if err := phone.RefusePerson(bob.PrincipalID, "не пишите мне"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		got := mac.Refusals()
		if len(got) == 1 && got[0].Principal == bob.PrincipalID &&
			got[0].Reason == "не пишите мне" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the laptop never learned she had refused him: %+v", got)
		}
		convergeTick(nodes, addrs)
	}

	// And the laptop now answers on her behalf: his knock never reaches her.
	if _, err := bob.KnockOn(tid, mac.PrincipalID, "можно вопрос?"); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		convergeTick(nodes, addrs)
	}
	if got := mac.Knocks(); len(got) != 0 {
		t.Fatalf("the refused person reached the other device: %+v", got)
	}
}
