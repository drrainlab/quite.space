// The sealed channel (MD-1): what a CONFIRMED ceremony carries.
//
// Everything before confirmation travelled as channel-bound MACs — proofs,
// never content. The freight is content, so it travels sealed: XChaCha20-
// Poly1305 under the freight key, which exists only after both humans said
// yes, and which binds secret, session and transcript at once. TLS already
// encrypts the wire; this layer is not about the wire — it is about making
// the freight OPENABLE ONLY BY THE CONFIRMED CEREMONY, so a bug that hands
// these packets to any other party (a logger, a proxy, a copy of the
// stream) hands them ciphertext under a key that party cannot have.
//
// Each direction has its own AAD label and its own message counter, so a
// packet cannot be replayed, reordered, or reflected back at its sender —
// within a ceremony that will exchange a handful of messages, a counter is
// the whole anti-replay story.
package pairing

import (
	"crypto/rand"
	"encoding/binary"
	"errors"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	msgChildSealed  = 5
	msgParentSealed = 6

	aadChildSealed  = "qs.pair.sealed.child.v1"
	aadParentSealed = "qs.pair.sealed.parent.v1"
)

var errNotConfirmed = errors.New("pairing: no sealed channel before both humans confirm")

// sealOne seals one message in one direction, stamping the direction label
// and the per-direction sequence into the AAD.
func sealOne(key []byte, label string, transcript []byte, seq uint64, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	box := aead.Seal(nil, nonce, plaintext, sealedAAD(label, transcript, seq))
	return append(nonce, box...), nil
}

func openOne(key []byte, label string, transcript []byte, seq uint64, sealed []byte) ([]byte, error) {
	if len(sealed) < chacha20poly1305.NonceSizeX+1 {
		return nil, errBadProof
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce, box := sealed[:chacha20poly1305.NonceSizeX], sealed[chacha20poly1305.NonceSizeX:]
	pt, err := aead.Open(nil, nonce, box, sealedAAD(label, transcript, seq))
	if err != nil {
		return nil, errors.New("pairing: sealed message does not open — tampered, replayed, or out of order")
	}
	return pt, nil
}

func sealedAAD(label string, transcript []byte, seq uint64) []byte {
	aad := make([]byte, 0, len(label)+len(transcript)+8)
	aad = append(aad, label...)
	aad = append(aad, transcript...)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], seq)
	return append(aad, n[:]...)
}

// SendSealed seals bytes for the child. Only a confirmed ceremony has the
// key — before that, there is deliberately nothing to send with.
func (s *ParentSession) SendSealed(plaintext []byte) error {
	key, err := s.FreightKey() // refuses before Confirm
	if err != nil {
		return errNotConfirmed
	}
	sealed, err := sealOne(key, aadParentSealed, s.transcript, s.sendSeq, plaintext)
	if err != nil {
		return err
	}
	s.sendSeq++
	return s.conn.Send(encodeMsg(msgParentSealed, sealed))
}

// AwaitSealed opens the child's next sealed message.
func (s *ParentSession) AwaitSealed() ([]byte, error) {
	key, err := s.FreightKey()
	if err != nil {
		return nil, errNotConfirmed
	}
	sealed, err := recv(s.conn, msgChildSealed)
	if err != nil {
		return nil, err
	}
	pt, err := openOne(key, aadChildSealed, s.transcript, s.recvSeq, sealed)
	if err != nil {
		return nil, err
	}
	s.recvSeq++
	return pt, nil
}

// SendSealed seals bytes for the parent, after the parent has confirmed.
func (s *ChildSession) SendSealed(plaintext []byte) error {
	key, err := s.FreightKey() // refuses before AwaitParentConfirm
	if err != nil {
		return errNotConfirmed
	}
	sealed, err := sealOne(key, aadChildSealed, s.transcript, s.sendSeq, plaintext)
	if err != nil {
		return err
	}
	s.sendSeq++
	return s.conn.Send(encodeMsg(msgChildSealed, sealed))
}

// AwaitSealed opens the parent's next sealed message — the freight arrives
// here.
func (s *ChildSession) AwaitSealed() ([]byte, error) {
	key, err := s.FreightKey()
	if err != nil {
		return nil, errNotConfirmed
	}
	sealed, err := recv(s.conn, msgParentSealed)
	if err != nil {
		return nil, err
	}
	pt, err := openOne(key, aadParentSealed, s.transcript, s.recvSeq, sealed)
	if err != nil {
		return nil, err
	}
	s.recvSeq++
	return pt, nil
}
