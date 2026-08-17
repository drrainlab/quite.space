package pairing

import (
	"bytes"
	"testing"
	"time"
)

// confirmedPair runs a full ceremony over real TLS and returns both
// confirmed sessions — the state from which the sealed channel exists.
func confirmedPair(t *testing.T) (*ParentSession, *ChildSession) {
	t.Helper()
	now := uint64(time.Now().Unix())
	parent, heard := mintForTest(t, now)
	pair := dialPair(t)

	childErr := make(chan error, 1)
	css := make(chan *ChildSession, 1)
	go func() {
		cs, err := RunChildCeremony(heard, pair.child, now)
		if err == nil {
			err = cs.Confirm()
		}
		if err == nil {
			err = cs.AwaitParentConfirm()
		}
		css <- cs
		childErr <- err
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
	if err := <-childErr; err != nil {
		t.Fatal(err)
	}
	return ps, <-css
}

// The freight rides a SEALED channel that only a confirmed ceremony has:
// AEAD under the freight key, both directions, over the same live session.
func TestSealedChannelCarriesBytesBothWays(t *testing.T) {
	ps, cs := confirmedPair(t)

	if err := cs.SendSealed([]byte("the child's public halves")); err != nil {
		t.Fatal(err)
	}
	got, err := ps.AwaitSealed()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("the child's public halves")) {
		t.Fatalf("child→parent bytes changed: %q", got)
	}
	if err := ps.SendSealed([]byte("the identity freight")); err != nil {
		t.Fatal(err)
	}
	back, err := cs.AwaitSealed()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, []byte("the identity freight")) {
		t.Fatalf("parent→child bytes changed: %q", back)
	}
}

// One byte flipped is a different freight — the AEAD refuses, it does not
// repair.
func TestATamperedSealedMessageIsRefused(t *testing.T) {
	ps, cs := confirmedPair(t)
	tap := &tamperConn{inner: ps.conn}
	ps.conn = tap
	if err := cs.SendSealed([]byte("precious")); err != nil {
		t.Fatal(err)
	}
	tap.flip = true
	if _, err := ps.AwaitSealed(); err == nil {
		t.Fatal("a tampered sealed message was opened")
	}
}

// The sealed channel does not exist before mutual confirmation: the freight
// must never be sendable to a ceremony the humans have not finished.
func TestNoSealedChannelBeforeConfirmation(t *testing.T) {
	now := uint64(time.Now().Unix())
	parent, heard := mintForTest(t, now)
	pair := dialPair(t)

	css := make(chan *ChildSession, 1)
	go func() {
		cs, _ := RunChildCeremony(heard, pair.child, now)
		css <- cs
	}()
	ps, err := parent.Run(pair.parent, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ps.SendSealed([]byte("too early")); err == nil {
		t.Fatal("the parent sealed freight before confirming")
	}
	if cs := <-css; cs != nil {
		if err := cs.SendSealed([]byte("too early")); err == nil {
			t.Fatal("the child sealed bytes before the parent confirmed")
		}
	}
}

// tamperConn flips one bit of the next polled packet.
type tamperConn struct {
	inner CeremonyConn
	flip  bool
}

func (c *tamperConn) Send(pkt []byte) error { return c.inner.Send(pkt) }
func (c *tamperConn) SessionBinding(label string) ([]byte, bool) {
	return c.inner.SessionBinding(label)
}
func (c *tamperConn) Poll() [][]byte {
	pkts := c.inner.Poll()
	if c.flip && len(pkts) > 0 && len(pkts[0]) > 0 {
		pkts[0][len(pkts[0])-1] ^= 0x01
		c.flip = false
	}
	return pkts
}
