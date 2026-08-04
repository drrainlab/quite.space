package quicklink

import (
	"crypto/rand"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

// The slow derivation. These are deliberately four times the cost of the
// keystore's parameters (kernel/identity/recovery.go), and the reason is that
// the inputs are not comparable: a passphrase is chosen by a person and may
// carry far more than 55 bits, while a quick link carries exactly 55 and
// never more. When the secret is small and fixed, the KDF is the only part of
// the budget you can still buy.
//
// N = 2^17 costs roughly half a second and 128 MiB per guess. That is
// invisible to someone doing it once and ruinous to someone doing it 2^55
// times, and the memory is the part that hurts an attacker with GPUs.
const (
	scryptN = 1 << 17
	scryptR = 8
	scryptP = 1
)

// Sealed payload field numbers. Append-only, ascending on the wire.
const (
	sealKeyVersion = 1
	sealKeyNonce   = 2
	sealKeyBox     = 3
)

// Plaintext field numbers, inside the box.
const (
	innerKeyPassLink = 1
	innerKeyFrom     = 2 // display name, so the guest sees who before joining
	innerKeySpace    = 3 // space title, same reason
	// The link's INTENT, so a guest can see what they are walking into
	// before they walk in. Strictly a description: the owner's own record
	// is what actually admits or refuses, and the two disagreeing means the
	// owner wins and the guest is told what really happened.
	innerKeyMaxUses  = 4
	innerKeyApproval = 5
	innerKeyExpires  = 6
	// The radio segment this node is on, so automatic failover has something
	// to fail over TO. See segment.go for what it is and what it is not.
	//
	// SealVersion does NOT move for this. The inner map is tag-keyed with a
	// SkipItem default, so an older build reads such a link and simply does
	// not see the segment — while the OUTER version check is a hard refusal,
	// which a person would read as "you typed the words wrong".
	innerKeySegment = 7
)

// SealVersion is bumped only when the sealed layout changes incompatibly.
const SealVersion = 1

const sealAD = "qs.quicklink.seal.v1"

// Payload is what a quick link ultimately delivers: an ordinary Space Pass,
// plus just enough context for the guest to see what they are accepting
// before they accept it. Nothing here is trusted — the pass verifies itself
// against the space's own key, and these two strings are only ever shown as
// "this is what the link claims".
type Payload struct {
	PassLink string
	From     string
	Space    string
	// MaxUses, Approval and ExpiresAt are CLAIMS about the link's intent —
	// exactly as untrusted as From and Space. They exist so a person can
	// decide whether to enter; the right to enter is decided elsewhere.
	MaxUses   uint64
	Approval  string
	ExpiresAt uint64
	// Segment is the radio segment the inviter is on, so the two devices are
	// ready for each other's air BEFORE they ever need it. Absent is the
	// ordinary case and never an error.
	//
	// Unlike the fields above it is not merely a claim: acting on it costs
	// nothing that can be taken back, and a wrong one produces a radio that
	// hears nobody — which is why every part of it is validated on decode
	// rather than believed.
	Segment RadioSegment
}

// Seal encrypts a payload under a key derived from the token.
//
// The salt is derived from the token rather than random, because the guest
// has nothing but the words: a random salt would have to travel alongside the
// ciphertext, which is the same thing as deriving it, only with more moving
// parts. The relay sees a 16-byte hint and an opaque box, exactly as it does
// for every other thing it holds.
//
// This is slow on purpose. Call it off whatever thread is drawing the UI.
func Seal(t Token, p Payload) ([]byte, error) {
	if p.PassLink == "" {
		return nil, errors.New("quicklink: nothing to seal")
	}
	n := 3
	if p.MaxUses > 0 {
		n++
	}
	if p.Approval != "" {
		n++
	}
	if p.ExpiresAt > 0 {
		n++
	}
	if p.Segment.Present() {
		n++
	}
	inner := codec.AppendMap(nil, n)
	inner = codec.AppendUint(inner, innerKeyPassLink)
	inner = codec.AppendText(inner, p.PassLink)
	inner = codec.AppendUint(inner, innerKeyFrom)
	inner = codec.AppendText(inner, p.From)
	inner = codec.AppendUint(inner, innerKeySpace)
	inner = codec.AppendText(inner, p.Space)
	if p.MaxUses > 0 {
		inner = codec.AppendUint(inner, innerKeyMaxUses)
		inner = codec.AppendUint(inner, p.MaxUses)
	}
	if p.Approval != "" {
		inner = codec.AppendUint(inner, innerKeyApproval)
		inner = codec.AppendText(inner, p.Approval)
	}
	if p.ExpiresAt > 0 {
		inner = codec.AppendUint(inner, innerKeyExpires)
		inner = codec.AppendUint(inner, p.ExpiresAt)
	}
	if p.Segment.Present() {
		inner = codec.AppendUint(inner, innerKeySegment)
		inner = appendSegment(inner, p.Segment)
	}

	aead, err := tokenAEAD(t)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("quicklink: no randomness available: %w", err)
	}
	box := aead.Seal(nil, nonce, inner, []byte(sealAD))

	out := codec.AppendMap(nil, 3)
	out = codec.AppendUint(out, sealKeyVersion)
	out = codec.AppendUint(out, SealVersion)
	out = codec.AppendUint(out, sealKeyNonce)
	out = codec.AppendBytes(out, nonce)
	out = codec.AppendUint(out, sealKeyBox)
	out = codec.AppendBytes(out, box)
	return out, nil
}

// ErrWrongToken means the box did not open. It is deliberately the SAME error
// for a wrong token and for corrupted bytes: telling those apart would tell a
// guesser that they had the right words but the wrong payload, which is a
// distinction worth exactly 55 bits.
var ErrWrongToken = errors.New("quicklink: this link does not open that payload")

// Open decrypts a sealed payload. Slow, for the same reason Seal is.
func Open(t Token, sealed []byte) (Payload, error) {
	d := codec.NewDecoder(sealed)
	m, err := d.ReadMapHeader()
	if err != nil {
		return Payload{}, ErrWrongToken
	}
	var (
		version uint64
		nonce   []byte
		box     []byte
	)
	for {
		k, ok, err := m.Next()
		if err != nil {
			return Payload{}, ErrWrongToken
		}
		if !ok {
			break
		}
		switch k {
		case sealKeyVersion:
			version, err = d.ReadUint()
		case sealKeyNonce:
			nonce, err = d.ReadBytes()
		case sealKeyBox:
			box, err = d.ReadBytes()
		default:
			err = d.SkipItem() // must-ignore
		}
		if err != nil {
			return Payload{}, ErrWrongToken
		}
	}
	if version != SealVersion {
		return Payload{}, fmt.Errorf(
			"quicklink: this link was made by a newer version (format %d, this build understands %d)",
			version, SealVersion)
	}
	if len(nonce) != chacha20poly1305.NonceSizeX || len(box) == 0 {
		return Payload{}, ErrWrongToken
	}

	aead, err := tokenAEAD(t)
	if err != nil {
		return Payload{}, err
	}
	inner, err := aead.Open(nil, nonce, box, []byte(sealAD))
	if err != nil {
		return Payload{}, ErrWrongToken
	}

	di := codec.NewDecoder(inner)
	im, err := di.ReadMapHeader()
	if err != nil {
		return Payload{}, ErrWrongToken
	}
	var p Payload
	for {
		k, ok, err := im.Next()
		if err != nil {
			return Payload{}, ErrWrongToken
		}
		if !ok {
			break
		}
		switch k {
		case innerKeyPassLink:
			p.PassLink, err = di.ReadText()
		case innerKeyFrom:
			p.From, err = di.ReadText()
		case innerKeySpace:
			p.Space, err = di.ReadText()
		case innerKeyMaxUses:
			p.MaxUses, err = di.ReadUint()
		case innerKeyApproval:
			p.Approval, err = di.ReadText()
		case innerKeyExpires:
			p.ExpiresAt, err = di.ReadUint()
		case innerKeySegment:
			p.Segment, err = readSegment(di)
		default:
			err = di.SkipItem()
		}
		if err != nil {
			return Payload{}, ErrWrongToken
		}
	}
	if p.PassLink == "" {
		return Payload{}, ErrWrongToken
	}
	return p, nil
}

func tokenAEAD(t Token) (interface {
	Seal(dst, nonce, plaintext, ad []byte) []byte
	Open(dst, nonce, ciphertext, ad []byte) ([]byte, error)
}, error) {
	key, err := scrypt.Key(t.Secret[:], t.KDFSalt(), scryptN, scryptR, scryptP,
		chacha20poly1305.KeySize)
	if err != nil {
		return nil, fmt.Errorf("quicklink: key derivation failed: %w", err)
	}
	return chacha20poly1305.NewX(key)
}
