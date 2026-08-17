package pairing

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/lan"
)

// The ceremony tests run over REAL TLS — transports/lan on loopback — so the
// session binding under the digits is the genuine RFC-9266-style export of a
// live handshake, not a fixture.

type tlsPair struct {
	parent *lan.Conn
	child  *lan.Conn
}

func dialPair(t *testing.T) tlsPair {
	t.Helper()
	parentNode, err := lan.NewNode()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { parentNode.Close() })
	accepted := make(chan *lan.Conn, 1)
	port, err := parentNode.Listen("127.0.0.1:0", func(c *lan.Conn) { accepted <- c })
	if err != nil {
		t.Fatal(err)
	}
	childNode, err := lan.NewNode()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { childNode.Close() })
	cc, err := childNode.Dial(fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case pc := <-accepted:
		return tlsPair{parent: pc, child: cc}
	case <-time.After(5 * time.Second):
		t.Fatal("the listener never saw the dial")
		return tlsPair{}
	}
}

func mintForTest(t *testing.T, now uint64) (*ParentCeremony, *Offer) {
	t.Helper()
	offer, err := NewOffer(rand.Reader, "127.0.0.1:0", now)
	if err != nil {
		t.Fatal(err)
	}
	// The child receives the offer out of band (sound, QR): simulate with a
	// round trip through the wire form.
	heard, err := DecodeOffer(offer.Encode())
	if err != nil {
		t.Fatal(err)
	}
	return NewParentCeremony(offer), heard
}

// The happy path, end to end: one live session, the same six digits on both
// screens, mutual confirmation, and ONE freight key on both ends.
func TestCeremonyOverRealTLSAgreesOnDigitsAndFreightKey(t *testing.T) {
	now := uint64(time.Now().Unix())
	parent, heard := mintForTest(t, now)
	pair := dialPair(t)

	var (
		wg           sync.WaitGroup
		ps           *ParentSession
		cs           *ChildSession
		pErr, cErr   error
		pKey, cKey   []byte
		pkErr, ckErr error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		ps, pErr = parent.Run(pair.parent, now)
		if pErr != nil {
			return
		}
		// The humans compared the screens and both said yes.
		pErr = ps.AwaitChildConfirm()
		if pErr != nil {
			return
		}
		pErr = ps.Confirm()
		if pErr == nil {
			pKey, pkErr = ps.FreightKey()
		}
	}()
	go func() {
		defer wg.Done()
		cs, cErr = RunChildCeremony(heard, pair.child, now)
		if cErr != nil {
			return
		}
		cErr = cs.Confirm()
		if cErr != nil {
			return
		}
		cErr = cs.AwaitParentConfirm()
		if cErr == nil {
			cKey, ckErr = cs.FreightKey()
		}
	}()
	wg.Wait()
	if pErr != nil || cErr != nil {
		t.Fatalf("ceremony failed: parent=%v child=%v", pErr, cErr)
	}
	if ps.Digits != cs.Digits || len(ps.Digits) != 6 {
		t.Fatalf("the two screens disagree: parent=%q child=%q", ps.Digits, cs.Digits)
	}
	if pkErr != nil || ckErr != nil {
		t.Fatalf("freight key: parent=%v child=%v", pkErr, ckErr)
	}
	if !bytes.Equal(pKey, cKey) {
		t.Fatal("one confirmed ceremony produced two different freight keys")
	}
}

// THE CAPABILITY IS SPENT ON CONFIRMATION, NEVER ON CONNECT. A stranger who
// heard the tones' ADDRESS but not the secret dials and babbles; the parent
// refuses — and the offer still works for the real child afterwards.
// Burning it at first TCP accept would hand any neighbour a one-packet
// denial of service.
func TestAStrangerCannotSpendTheOffer(t *testing.T) {
	now := uint64(time.Now().Unix())
	parent, heard := mintForTest(t, now)

	// The stranger: a real TLS dial, a garbage hello.
	strangerPair := dialPair(t)
	go func() {
		_ = strangerPair.child.Send([]byte("i heard tones and i am guessing"))
	}()
	if _, err := parent.Run(strangerPair.parent, now); err == nil {
		t.Fatal("a stranger without the secret got a ceremony")
	}

	// The real child arrives next, on a fresh session, and succeeds.
	pair := dialPair(t)
	done := make(chan error, 1)
	go func() {
		cs, err := RunChildCeremony(heard, pair.child, now)
		if err == nil {
			err = cs.Confirm()
		}
		done <- err
	}()
	ps, err := parent.Run(pair.parent, now)
	if err != nil {
		t.Fatalf("the offer was spent by a stranger's failed attempt: %v", err)
	}
	if err := ps.AwaitChildConfirm(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A LIVE person-in-the-middle: two REAL TLS sessions with the attacker
// terminating both, bytes piped faithfully between them. The child's hello
// is MAC-bound to ITS session's binding; the parent verifies against a
// DIFFERENT session's binding — so the ceremony dies at hello, before any
// digits exist to compare, and the offer is not spent.
func TestAManInTheMiddleCannotEvenReachTheDigits(t *testing.T) {
	// The parent answers a relayed hello with SILENCE — refusing to oracle —
	// so the child's failure is a timeout, shortened here to keep the suite
	// honest about what it waits for.
	old := ceremonyWait
	ceremonyWait = 500 * time.Millisecond
	t.Cleanup(func() { ceremonyWait = old })
	now := uint64(time.Now().Unix())
	parent, heard := mintForTest(t, now)

	legToParent := dialPair(t) // attacker → parent
	legToChild := dialPair(t)  // child → attacker (attacker is the listener)
	// The attacker pipes bytes both ways, faithfully.
	pipe := func(from, to *lan.Conn) {
		for {
			for _, pkt := range from.Poll() {
				if to.Send(pkt) != nil {
					return
				}
			}
			if closed, _ := from.Closed(); closed {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	go pipe(legToChild.parent, legToParent.child)
	go pipe(legToParent.child, legToChild.parent)

	childDone := make(chan error, 1)
	go func() {
		_, err := RunChildCeremony(heard, legToChild.child, now)
		childDone <- err
	}()
	if _, err := parent.Run(legToParent.parent, now); err == nil {
		t.Fatal("a relayed hello crossed two TLS sessions and was believed")
	}
	if err := <-childDone; err == nil {
		t.Fatal("the child completed a ceremony through an interceptor")
	}
}

// The offer's sixty seconds are BOTH sides' law.
func TestAnExpiredOfferRefusesTheCeremony(t *testing.T) {
	now := uint64(time.Now().Unix())
	parent, heard := mintForTest(t, now)
	late := now + OfferTTLSeconds + 1

	pair := dialPair(t)
	if _, err := RunChildCeremony(heard, pair.child, late); err == nil {
		t.Fatal("the child dialled with an expired offer")
	}
	if _, err := parent.Run(pair.parent, late); err == nil {
		t.Fatal("the parent ran a ceremony on an expired offer")
	}
}

// SINGLE USE: a confirmed ceremony is the offer's one life. The second
// ceremony — same secret, fresh session — is refused by the minter.
func TestConfirmSpendsTheOfferExactlyOnce(t *testing.T) {
	now := uint64(time.Now().Unix())
	parent, heard := mintForTest(t, now)

	pair := dialPair(t)
	done := make(chan error, 1)
	go func() {
		cs, err := RunChildCeremony(heard, pair.child, now)
		if err == nil {
			err = cs.Confirm()
		}
		done <- err
	}()
	ps, err := parent.Run(pair.parent, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ps.AwaitChildConfirm(); err != nil {
		t.Fatal(err)
	}
	if err := ps.Confirm(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	second := dialPair(t)
	go func() {
		_, _ = RunChildCeremony(heard, second.child, now)
	}()
	if _, err := parent.Run(second.parent, now); err == nil {
		t.Fatal("a spent offer ran a second ceremony")
	}
}
