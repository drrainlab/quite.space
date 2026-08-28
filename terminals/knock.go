// The knock (ADR-027): asking a PERSON, not a link, for a conversation.
//
// A knock is a sealed envelope left in the recipient's mailbox — never an
// event in the room the two share. Writing "I would like to talk to you
// privately" into a shared log would tell everyone in that room that you
// asked, and that fact belongs to two people.
//
// It carries no keys. The pass inside is authority to REQUEST entry
// (ADR-012 invariant 1), so a stranger holding this envelope gains nothing
// until the recipient decides. The answer is the ordinary Decision — the
// same sealed sentence the door already uses, with its own HPKE info
// string, so no parser can read a decline as a grant.
//
// Three things ride here and each earns its place:
//
//	the CERTIFICATE CHAIN — so the recipient learns who is asking from a
//	  root signature rather than from a name somebody typed;
//	the SHARED SPACE — the acquaintance claim, which the recipient checks
//	  against its OWN log and never believes on the knock's word;
//	the LINE — one short sentence, rendered as somebody else's words.
package terminals

import (
	"crypto/ed25519"
	"errors"
	"strings"

	"github.com/drrainlab/quiet_places/kernel/crypto"
	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

const (
	knockVersion = 1

	// MaxKnockLine bounds a stranger's text. It is shown to a person who
	// has not agreed to hear from this author yet, so it is short enough
	// to read at a glance and cannot become a channel of its own.
	MaxKnockLine = 140
	// MaxKnockCerts bounds the chain a knocker may hand over — the same
	// shape as a join request's certified set.
	MaxKnockCerts = 8
)

// Knock is what one person sends to another to ask for a conversation.
type Knock struct {
	// ID is the knock's identity, derived by the sender from its own
	// randomness; it is what a decision answers and what the recipient
	// keys a pending entry on.
	ID [32]byte
	// From is the ASKING device; Principal is what its certificate says.
	// Both travel so the recipient can verify the chain without a lookup.
	From      id.DeviceID
	Principal id.PrincipalID
	// Certs are root-signed certificates for From (and, harmlessly, the
	// asker's other devices). Each is verified on its own signature.
	Certs [][]byte
	// Via is the space both people already belong to — the acquaintance
	// CLAIM. The recipient checks it against its own membership and its
	// own log; a knock that names a room the recipient is not in, or in
	// which the asker is not a member, is not admitted.
	Via id.TerminalID
	// Line is the reason, in the asker's own words. Never rendered as the
	// product's voice.
	Line string
	// Pass is the share link for a fresh one-to-one space. Authority to
	// request entry, nothing more.
	Pass string
	// Reply is the rendezvous the answer goes to — the asker's own, so a
	// decision can travel back without the recipient learning any route.
	// Sixteen bytes because it feeds RespHint, the same dead-drop the
	// door's decisions already use.
	Reply [16]byte
	// ExpiresAt is when the asking stops (unix seconds).
	ExpiresAt uint64
	// XPub is the asking device's X25519 public key: where the sealed
	// answer goes. It also appears inside the certificate, and the
	// recipient uses the CERTIFICATE's copy — this one is a convenience
	// for building the reply before the chain is walked.
	XPub [32]byte

	// Signature covers everything above, by the asking device's key.
	Signature []byte
}

const (
	knKeyVersion = 1
	knKeyID      = 2
	knKeyFrom    = 3
	knKeyPrin    = 4
	knKeyCerts   = 5
	knKeyVia     = 6
	knKeyLine    = 7
	knKeyPass    = 8
	knKeyReply   = 9
	knKeyExpires = 10
	knKeyXPub    = 11
	knKeySig     = 12
)

func knockBody(k *Knock) []byte {
	buf := codec.AppendMap(nil, 11)
	buf = codec.AppendUint(buf, knKeyVersion)
	buf = codec.AppendUint(buf, knockVersion)
	buf = codec.AppendUint(buf, knKeyID)
	buf = codec.AppendBytes(buf, k.ID[:])
	buf = codec.AppendUint(buf, knKeyFrom)
	buf = codec.AppendBytes(buf, k.From[:])
	buf = codec.AppendUint(buf, knKeyPrin)
	buf = codec.AppendBytes(buf, k.Principal[:])
	buf = codec.AppendUint(buf, knKeyCerts)
	buf = codec.AppendArray(buf, len(k.Certs))
	for _, c := range k.Certs {
		buf = codec.AppendBytes(buf, c)
	}
	buf = codec.AppendUint(buf, knKeyVia)
	buf = codec.AppendBytes(buf, k.Via[:])
	buf = codec.AppendUint(buf, knKeyLine)
	buf = codec.AppendText(buf, k.Line)
	buf = codec.AppendUint(buf, knKeyPass)
	buf = codec.AppendText(buf, k.Pass)
	buf = codec.AppendUint(buf, knKeyReply)
	buf = codec.AppendBytes(buf, k.Reply[:])
	buf = codec.AppendUint(buf, knKeyExpires)
	buf = codec.AppendUint(buf, k.ExpiresAt)
	buf = codec.AppendUint(buf, knKeyXPub)
	buf = codec.AppendBytes(buf, k.XPub[:])
	return buf
}

// knockInfo binds the seal to the recipient device: an envelope opened
// with one device's key cannot be replayed at another's mailbox.
func knockInfo(recipient id.DeviceID) []byte {
	return append([]byte("qs.knock.v1"), recipient[:]...)
}

// SealKnock signs and seals a knock to one recipient device.
func SealKnock(k *Knock, recipient id.DeviceID, recipientXpub [32]byte,
	signKey ed25519.PrivateKey) ([]byte, error) {

	if err := ValidateKnockLine(k.Line); err != nil {
		return nil, err
	}
	if len(k.Certs) > MaxKnockCerts {
		return nil, errors.New("terminals: too many certificates in a knock")
	}
	if k.ExpiresAt == 0 {
		return nil, errors.New("terminals: a knock must say when it stops asking")
	}
	body := knockBody(k)
	sig := ed25519.Sign(signKey, append([]byte("qs.knock.sig.v1"), body...))
	full := codec.AppendMap(nil, 12)
	full = append(full, body[1:]...) // splice: same pairs, one more to come
	full = codec.AppendUint(full, knKeySig)
	full = codec.AppendBytes(full, sig)

	enc, ct, err := crypto.SealTo(recipientXpub, knockInfo(recipient), full)
	if err != nil {
		return nil, err
	}
	out := codec.AppendMap(nil, 2)
	out = codec.AppendUint(out, 1)
	out = codec.AppendBytes(out, enc)
	out = codec.AppendUint(out, 2)
	out = codec.AppendBytes(out, ct)
	return out, nil
}

// OpenKnock opens an envelope addressed to this device and verifies the
// asking device's signature. It proves the ENVELOPE — that these bytes were
// written by the device that claims to have written them and were meant
// for this mailbox. Everything else a knock claims (the principal behind
// the device, the shared room) is checked by the caller against its own
// state; this function deliberately does not know how to.
func OpenKnock(recipient id.DeviceID, xpriv [32]byte, sealed []byte) (*Knock, error) {
	enc, ct, err := splitSealed(sealed)
	if err != nil {
		return nil, err
	}
	plain, err := crypto.OpenFrom(xpriv, knockInfo(recipient), enc, ct)
	if err != nil {
		return nil, errors.New("terminals: this knock is not addressed to this device")
	}
	k, sig, err := decodeKnock(plain)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(ed25519.PublicKey(k.From[:]),
		append([]byte("qs.knock.sig.v1"), knockBody(k)...), sig) {
		return nil, errors.New("terminals: knock signature invalid")
	}
	if err := ValidateKnockLine(k.Line); err != nil {
		return nil, err
	}
	return k, nil
}

// KnockPrincipal walks the carried certificates and returns the principal
// the asking device belongs to, proven by a ROOT signature — not by the
// knock's own word for it. The certificate for the asking device must be
// present; anything else in the chain is ignored here.
func KnockPrincipal(k *Knock) (id.PrincipalID, [32]byte, error) {
	var none id.PrincipalID
	var xpub [32]byte
	for _, raw := range k.Certs {
		c, err := identity.DecodeCertificate(raw)
		if err != nil {
			continue
		}
		if c.Device != k.From {
			continue
		}
		if err := c.Verify(); err != nil {
			continue
		}
		if c.Principal != k.Principal {
			return none, xpub, errors.New("terminals: the knock names a principal its certificate does not")
		}
		return c.Principal, c.X25519Pub, nil
	}
	return none, xpub, errors.New("terminals: the knock carries no certificate for the device that signed it")
}

// ValidateKnockLine keeps a stranger's sentence a sentence.
func ValidateKnockLine(line string) error {
	if len([]rune(line)) > MaxKnockLine {
		return errors.New("terminals: a knock's line is at most 140 characters")
	}
	if strings.ContainsAny(line, "\n\r\t") {
		return errors.New("terminals: a knock's line is one line")
	}
	return nil
}

func decodeKnock(b []byte) (*Knock, []byte, error) {
	bad := errors.New("terminals: malformed knock")
	d := codec.NewDecoder(b)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, nil, bad
	}
	k := &Knock{}
	var sig []byte
	read := func(dst []byte) error {
		v, e := d.ReadBytes()
		if e != nil || len(v) != len(dst) {
			return bad
		}
		copy(dst, v)
		return nil
	}
	for {
		key, ok, er := m.Next()
		if er != nil {
			return nil, nil, bad
		}
		if !ok {
			break
		}
		switch key {
		case knKeyVersion:
			v, e := d.ReadUint()
			if e != nil || v != knockVersion {
				return nil, nil, bad
			}
		case knKeyID:
			er = read(k.ID[:])
		case knKeyFrom:
			er = read(k.From[:])
		case knKeyPrin:
			er = read(k.Principal[:])
		case knKeyCerts:
			n, e := d.ReadArray()
			if e != nil || n > MaxKnockCerts {
				return nil, nil, bad
			}
			for range n {
				c, e := d.ReadBytes()
				if e != nil {
					return nil, nil, bad
				}
				k.Certs = append(k.Certs, append([]byte(nil), c...))
			}
		case knKeyVia:
			er = read(k.Via[:])
		case knKeyLine:
			k.Line, er = d.ReadText()
		case knKeyPass:
			k.Pass, er = d.ReadText()
		case knKeyReply:
			er = read(k.Reply[:])
		case knKeyExpires:
			k.ExpiresAt, er = d.ReadUint()
		case knKeyXPub:
			er = read(k.XPub[:])
		case knKeySig:
			sig, er = d.ReadBytes()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, nil, bad
		}
	}
	if err := d.Done(); err != nil {
		return nil, nil, bad
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, nil, errors.New("terminals: knock carries no signature")
	}
	return k, sig, nil
}
