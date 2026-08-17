// The pairing ceremony (MD-1): two devices, one live TLS session, six
// digits, and a mutual confirmation that spends the offer.
//
//	child   dials the offer's address; both export the session binding
//	child → HELLO   HMAC(secret, label ‖ binding)   proves live possession
//	parent → HELLO  its own channel-bound proof     mutual, same session
//	both    show ConfirmDigits(secret, binding); the HUMANS compare
//	child → CONFIRM HMAC over binding ‖ transcript
//	parent → CONFIRM, and only now is the offer SPENT
//
// Every proof is bound to the SESSION, never just to the secret: a hello
// relayed by an interceptor across two TLS sessions verifies against the
// wrong binding and dies before any digits exist to compare — the MITM is
// refused by arithmetic before it can even be shown to a human.
//
// WHAT A FAILED ATTEMPT COSTS: nothing. The offer is spent on the parent's
// CONFIRM — after both proofs and both humans — never on connect and never
// on a bad hello, or any neighbour who heard the tones could burn the
// ceremony with one packet. A stranger's babble is that stranger's problem;
// the parent keeps listening for the real child inside the same window.
//
// This file is TRANSPORT-BLIND: it speaks through CeremonyConn, which
// transports/lan.Conn satisfies as it stands. The freight itself — what
// travels under the key this ceremony yields — is the next seam, not this
// one.
package pairing

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

// CeremonyConn is the slice of a live connection the ceremony needs. It is
// exactly the shape of transports/lan.Conn on purpose.
type CeremonyConn interface {
	Send(pkt []byte) error
	Poll() [][]byte
	SessionBinding(label string) ([]byte, bool)
}

// Wire message types (append-only).
const (
	msgChildHello    = 1
	msgParentHello   = 2
	msgChildConfirm  = 3
	msgParentConfirm = 4
)

// MAC labels, one per message, so no proof can be replayed as another.
const (
	labelChildHello    = "qs.pair.hello.child.v1"
	labelParentHello   = "qs.pair.hello.parent.v1"
	labelChildConfirm  = "qs.pair.confirm.child.v1"
	labelParentConfirm = "qs.pair.confirm.parent.v1"
)

// ceremonyWait bounds every single await. A ceremony is two humans holding
// two devices in one room; ten seconds of silence per step is an attempt
// that failed, not one that needs patience. A var so tests that PROVE the
// silence (the parent deliberately says nothing to a bad hello, rather than
// oracle to an interceptor) need not sit through the human-scale wait.
var ceremonyWait = 10 * time.Second

var (
	// ErrOfferSpent is the single-use rule: a confirmed ceremony is the
	// offer's one life.
	ErrOfferSpent = errors.New("pairing: this offer has already paired a device")
	// ErrOfferExpired is the sixty-second rule, enforced by both sides.
	ErrOfferExpired = errors.New("pairing: the offer's window has closed")
	errNoSession    = errors.New("pairing: the connection exports no session binding")
	errBadProof     = errors.New("pairing: the peer could not prove it holds this offer on this session")
	errSilence      = errors.New("pairing: the peer went silent mid-ceremony")
)

// mac computes one channel-bound proof. Everything after the label is what
// the proof attests: the live session always, the transcript once there is
// one worth attesting.
func mac(secret [32]byte, label string, binding, transcript []byte) []byte {
	h := hmac.New(sha256.New, secret[:])
	h.Write([]byte(label))
	h.Write(binding)
	h.Write(transcript)
	return h.Sum(nil)
}

func encodeMsg(typ uint64, proof []byte) []byte {
	var buf []byte
	buf = codec.AppendArray(buf, 2)
	buf = codec.AppendUint(buf, typ)
	buf = codec.AppendBytes(buf, proof)
	return buf
}

func decodeMsg(pkt []byte) (typ uint64, proof []byte, err error) {
	d := codec.NewDecoder(pkt)
	n, err := d.ReadArray()
	if err != nil || n < 2 {
		return 0, nil, errBadProof
	}
	if typ, err = d.ReadUint(); err != nil {
		return 0, nil, errBadProof
	}
	if proof, err = d.ReadBytes(); err != nil {
		return 0, nil, errBadProof
	}
	return typ, proof, nil
}

// recv waits for the next packet of the wanted type, bounded by
// ceremonyWait. Wrong-type packets fail the ceremony rather than being
// skipped: the protocol has exactly one legal next message at every step,
// and tolerance for others is room for confusion attacks.
func recv(conn CeremonyConn, wantType uint64) ([]byte, error) {
	deadline := time.Now().Add(ceremonyWait)
	for time.Now().Before(deadline) {
		for _, pkt := range conn.Poll() {
			typ, proof, err := decodeMsg(pkt)
			if err != nil || typ != wantType {
				return nil, errBadProof
			}
			return proof, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil, errSilence
}

// ParentCeremony is the MINTER's side and the single-use bookkeeping: one
// value of this type is one offer's whole life, however many attempts dial
// in during the window.
type ParentCeremony struct {
	offer *Offer
	spent bool
}

func NewParentCeremony(offer *Offer) *ParentCeremony {
	return &ParentCeremony{offer: offer}
}

// ParentSession is one live attempt that survived the hellos. Digits are
// for the parent's screen; Confirm is for after the human says yes.
type ParentSession struct {
	Digits string

	parent     *ParentCeremony
	conn       CeremonyConn
	binding    []byte
	transcript []byte
}

// Run admits one dial-in through the hello exchange. A failed attempt costs
// the offer nothing; a spent or expired offer refuses before touching the
// connection.
func (p *ParentCeremony) Run(conn CeremonyConn, nowUnix uint64) (*ParentSession, error) {
	if p.spent {
		return nil, ErrOfferSpent
	}
	if p.offer.Expired(nowUnix) {
		return nil, ErrOfferExpired
	}
	binding, ok := conn.SessionBinding(BindingLabel)
	if !ok {
		return nil, errNoSession
	}
	proof, err := recv(conn, msgChildHello)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal(proof, mac(p.offer.Secret, labelChildHello, binding, nil)) {
		return nil, errBadProof
	}
	if err := conn.Send(encodeMsg(msgParentHello,
		mac(p.offer.Secret, labelParentHello, binding, nil))); err != nil {
		return nil, err
	}
	digits, err := ConfirmDigits(p.offer.Secret, binding)
	if err != nil {
		return nil, err
	}
	// The transcript is what both sides can compute identically: the offer
	// as it travelled, and the session both proofs were bound to.
	transcript := TranscriptHash(p.offer.Encode(), binding)
	return &ParentSession{
		Digits: digits, parent: p, conn: conn,
		binding: binding, transcript: transcript,
	}, nil
}

// AwaitChildConfirm blocks until the child's human has said yes.
func (s *ParentSession) AwaitChildConfirm() error {
	proof, err := recv(s.conn, msgChildConfirm)
	if err != nil {
		return err
	}
	if !hmac.Equal(proof, mac(s.parent.offer.Secret, labelChildConfirm, s.binding, s.transcript)) {
		return errBadProof
	}
	return nil
}

// Confirm is the parent's human saying yes — and the moment the offer is
// SPENT. Everything before this was refusable for free.
func (s *ParentSession) Confirm() error {
	if err := s.conn.Send(encodeMsg(msgParentConfirm,
		mac(s.parent.offer.Secret, labelParentConfirm, s.binding, s.transcript))); err != nil {
		return err
	}
	s.parent.spent = true
	return nil
}

// FreightKey yields the key the identity freight travels under. Only a
// confirmed ceremony has one.
func (s *ParentSession) FreightKey() ([]byte, error) {
	if !s.parent.spent {
		return nil, fmt.Errorf("pairing: no freight key before the parent confirms")
	}
	return FreightKey(s.parent.offer.Secret, s.binding, s.transcript)
}

// ChildSession is the dialling side after its hello was answered.
type ChildSession struct {
	Digits string

	offer      *Offer
	conn       CeremonyConn
	binding    []byte
	transcript []byte
	confirmed  bool
}

// RunChildCeremony dials into the ceremony: sends the child hello, verifies
// the parent's answering proof, and surfaces the digits for the screen.
func RunChildCeremony(offer *Offer, conn CeremonyConn, nowUnix uint64) (*ChildSession, error) {
	if offer.Expired(nowUnix) {
		return nil, ErrOfferExpired
	}
	binding, ok := conn.SessionBinding(BindingLabel)
	if !ok {
		return nil, errNoSession
	}
	if err := conn.Send(encodeMsg(msgChildHello,
		mac(offer.Secret, labelChildHello, binding, nil))); err != nil {
		return nil, err
	}
	proof, err := recv(conn, msgParentHello)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal(proof, mac(offer.Secret, labelParentHello, binding, nil)) {
		return nil, errBadProof
	}
	digits, err := ConfirmDigits(offer.Secret, binding)
	if err != nil {
		return nil, err
	}
	return &ChildSession{
		Digits: digits, offer: offer, conn: conn,
		binding: binding, transcript: TranscriptHash(offer.Encode(), binding),
	}, nil
}

// Confirm is the child's human saying yes.
func (s *ChildSession) Confirm() error {
	return s.conn.Send(encodeMsg(msgChildConfirm,
		mac(s.offer.Secret, labelChildConfirm, s.binding, s.transcript)))
}

// AwaitParentConfirm blocks until the parent's human has said yes too.
func (s *ChildSession) AwaitParentConfirm() error {
	proof, err := recv(s.conn, msgParentConfirm)
	if err != nil {
		return err
	}
	if !hmac.Equal(proof, mac(s.offer.Secret, labelParentConfirm, s.binding, s.transcript)) {
		return errBadProof
	}
	s.confirmed = true
	return nil
}

// FreightKey yields the child's copy of the freight key, after mutual
// confirmation.
func (s *ChildSession) FreightKey() ([]byte, error) {
	if !s.confirmed {
		return nil, fmt.Errorf("pairing: no freight key before the parent confirms")
	}
	return FreightKey(s.offer.Secret, s.binding, s.transcript)
}
