// Space Pass = Join Pass (ADR-012): a pass carries the authority to REQUEST
// entry, never epoch keys. Opening a pass decrypts nothing; the owner's
// device validates the request, admits the device into the space's
// cryptographic circle, and only then does the newcomer receive an epoch.
//
// This file holds the pure format + crypto: mint, decode, build/open the
// sealed join request and the sealed acceptance. The stateful, idempotent
// acceptance saga and the relay rendezvous live in package node.
//
// Note on identity: a space's TerminalID IS its Ed25519 public key
// (ADR-001), so `space_id` is itself the pass-verification key — no separate
// signing-pub field is needed in this system.
package terminals

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"github.com/drrainlab/quiet_places/kernel/crypto"
	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// PassProfileMember is the fixed v1 membership profile (rights are not yet
// granularly enforced, so the pass promises exactly what it delivers).
const PassProfileMember = "member.v1"

const passVersion = 1

// Pass is the decoded, verified pass body.
type Pass struct {
	Space          id.TerminalID
	AcceptorDevice id.DeviceID
	AcceptorXpub   [32]byte
	Rendezvous     [16]byte
	PassID         [16]byte
	IssuedAt       uint64
	ExpiresAt      uint64
	MaxUses        uint64
	Profile        string
	frame          []byte // signed body bytes, verbatim
}

// Frame returns the signed body bytes (for the owner's records).
func (p *Pass) Frame() []byte { return p.frame }

// pass body key table.
const (
	pkVersion   = 1
	pkSpace     = 2
	pkAcceptDev = 3
	pkAcceptX   = 4
	pkRendez    = 5
	pkPassID    = 6
	pkIssued    = 7
	pkExpires   = 8
	pkMaxUses   = 9
	pkProfile   = 10
	pkSignature = 11
)

func passIDFromSecret(secret []byte) [16]byte {
	h := sha256.Sum256(append([]byte("qs.pass.id.v1"), secret...))
	var out [16]byte
	copy(out[:], h[:16])
	return out
}

func (p *Pass) signingBody() []byte {
	buf := codec.AppendMap(nil, 10)
	buf = codec.AppendUint(buf, pkVersion)
	buf = codec.AppendUint(buf, passVersion)
	buf = codec.AppendUint(buf, pkSpace)
	buf = codec.AppendBytes(buf, p.Space[:])
	buf = codec.AppendUint(buf, pkAcceptDev)
	buf = codec.AppendBytes(buf, p.AcceptorDevice[:])
	buf = codec.AppendUint(buf, pkAcceptX)
	buf = codec.AppendBytes(buf, p.AcceptorXpub[:])
	buf = codec.AppendUint(buf, pkRendez)
	buf = codec.AppendBytes(buf, p.Rendezvous[:])
	buf = codec.AppendUint(buf, pkPassID)
	buf = codec.AppendBytes(buf, p.PassID[:])
	buf = codec.AppendUint(buf, pkIssued)
	buf = codec.AppendUint(buf, p.IssuedAt)
	buf = codec.AppendUint(buf, pkExpires)
	buf = codec.AppendUint(buf, p.ExpiresAt)
	buf = codec.AppendUint(buf, pkMaxUses)
	buf = codec.AppendUint(buf, p.MaxUses)
	buf = codec.AppendUint(buf, pkProfile)
	buf = codec.AppendText(buf, p.Profile)
	return buf
}

// NewPass mints a pass for a space, signed by its terminal key. Returns the
// shareable string (base64url) and the decoded Pass with its bearer secret.
// The controller (space.priv) must be present.
func (s *Space) NewPass(maxUses, ttlSeconds, nowUnix uint64) (string, *Pass, []byte, error) {
	if s.priv == nil {
		return "", nil, nil, errors.New("terminals: only the controller can mint a pass")
	}
	if s.priv2 == nil || s.priv2.self == nil {
		return "", nil, nil, errors.New("terminals: space has no acceptor device")
	}
	if maxUses == 0 {
		maxUses = 1
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", nil, nil, err
	}
	p := &Pass{
		Space:          s.ID,
		AcceptorDevice: s.priv2.self.ID,
		AcceptorXpub:   s.priv2.self.X25519Pub,
		PassID:         passIDFromSecret(secret),
		IssuedAt:       nowUnix,
		ExpiresAt:      nowUnix + ttlSeconds,
		MaxUses:        maxUses,
		Profile:        PassProfileMember,
	}
	if _, err := rand.Read(p.Rendezvous[:]); err != nil {
		return "", nil, nil, err
	}
	body := p.signingBody()
	sig := ed25519.Sign(s.priv, body)
	// frame = body map extended with signature entry (key 11 sorts last).
	frame := codec.AppendMap(nil, 11)
	frame = append(frame, body[1:]...) // strip 1-byte map(10) header
	frame = codec.AppendUint(frame, pkSignature)
	frame = codec.AppendBytes(frame, sig)
	p.frame = frame

	env := codec.AppendMap(nil, 2)
	env = codec.AppendUint(env, 1)
	env = codec.AppendBytes(env, frame)
	env = codec.AppendUint(env, 2)
	env = codec.AppendBytes(env, secret)
	return base64.RawURLEncoding.EncodeToString(env), p, secret, nil
}

// DecodePass parses and verifies a shared pass string: signature by the
// space terminal key, and pass_id == H(bearer_secret). Expiry is checked by
// the caller against a trusted clock (the newcomer's clock is advisory).
func DecodePass(shared string) (*Pass, []byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(shared)
	if err != nil {
		return nil, nil, errors.New("terminals: pass is not valid base64url")
	}
	d := codec.NewDecoder(raw)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, nil, err
	}
	var frame, secret []byte
	for {
		k, ok, er := m.Next()
		if er != nil {
			return nil, nil, er
		}
		if !ok {
			break
		}
		switch k {
		case 1:
			frame, er = d.ReadBytes()
		case 2:
			secret, er = d.ReadBytes()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, nil, err
	}
	if frame == nil || len(secret) != 32 {
		return nil, nil, errors.New("terminals: malformed pass envelope")
	}
	p, sig, err := decodePassFrame(frame)
	if err != nil {
		return nil, nil, err
	}
	if !ed25519.Verify(ed25519.PublicKey(p.Space[:]), p.signingBody(), sig) {
		return nil, nil, errors.New("terminals: pass signature invalid")
	}
	if p.PassID != passIDFromSecret(secret) {
		return nil, nil, errors.New("terminals: pass secret does not match pass id")
	}
	if p.Profile != PassProfileMember {
		return nil, nil, errors.New("terminals: unsupported pass profile")
	}
	return p, append([]byte(nil), secret...), nil
}

func decodePassFrame(frame []byte) (*Pass, []byte, error) {
	d := codec.NewDecoder(frame)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, nil, err
	}
	p := &Pass{frame: append([]byte(nil), frame...)}
	var sig []byte
	read := func(dst []byte) error {
		b, e := d.ReadBytes()
		if e != nil {
			return e
		}
		if len(b) != len(dst) {
			return errors.New("terminals: pass field wrong size")
		}
		copy(dst, b)
		return nil
	}
	for {
		k, ok, er := m.Next()
		if er != nil {
			return nil, nil, er
		}
		if !ok {
			break
		}
		switch k {
		case pkVersion:
			var v uint64
			v, er = d.ReadUint()
			if v != passVersion {
				return nil, nil, errors.New("terminals: unsupported pass version")
			}
		case pkSpace:
			er = read(p.Space[:])
		case pkAcceptDev:
			er = read(p.AcceptorDevice[:])
		case pkAcceptX:
			er = read(p.AcceptorXpub[:])
		case pkRendez:
			er = read(p.Rendezvous[:])
		case pkPassID:
			er = read(p.PassID[:])
		case pkIssued:
			p.IssuedAt, er = d.ReadUint()
		case pkExpires:
			p.ExpiresAt, er = d.ReadUint()
		case pkMaxUses:
			p.MaxUses, er = d.ReadUint()
		case pkProfile:
			p.Profile, er = d.ReadText()
		case pkSignature:
			sig, er = d.ReadBytes()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, nil, err
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, nil, errors.New("terminals: pass missing signature")
	}
	return p, sig, nil
}

// ---- Join request (sealed to the acceptor device) ----

// JoinRequest is what a newcomer sends the owner.
type JoinRequest struct {
	RequestID   [32]byte
	PassID      [16]byte
	Secret      []byte
	Device      id.DeviceID
	DeviceXpub  [32]byte
	DisplayName string
}

const (
	jrRequestID = 1
	jrPassID    = 2
	jrSecret    = 3
	jrDevice    = 4
	jrDeviceX   = 5
	jrName      = 6
	jrSignature = 7
)

func (r *JoinRequest) signingBody() []byte {
	buf := codec.AppendMap(nil, 6)
	buf = codec.AppendUint(buf, jrRequestID)
	buf = codec.AppendBytes(buf, r.RequestID[:])
	buf = codec.AppendUint(buf, jrPassID)
	buf = codec.AppendBytes(buf, r.PassID[:])
	buf = codec.AppendUint(buf, jrSecret)
	buf = codec.AppendBytes(buf, r.Secret)
	buf = codec.AppendUint(buf, jrDevice)
	buf = codec.AppendBytes(buf, r.Device[:])
	buf = codec.AppendUint(buf, jrDeviceX)
	buf = codec.AppendBytes(buf, r.DeviceXpub[:])
	buf = codec.AppendUint(buf, jrName)
	buf = codec.AppendText(buf, r.DisplayName)
	return buf
}

func joinInfo(space id.TerminalID, passID [16]byte) []byte {
	info := append([]byte("qs.pass.join.v1"), space[:]...)
	return append(info, passID[:]...)
}

// BuildJoinRequestWithSecret builds and HPKE-seals a join request to the
// pass's acceptor device. The bearer secret proves possession of the pass;
// request_id = SHA256(pass_id ‖ device_id ‖ nonce) so retries are idempotent.
func BuildJoinRequestWithSecret(pass *Pass, secret []byte, dev *identity.Device,
	displayName string, nonce []byte, devicePriv ed25519.PrivateKey) (reqID [32]byte, sealed []byte, err error) {

	reqID = sha256.Sum256(append(append(pass.PassID[:], dev.ID[:]...), nonce...))
	r := &JoinRequest{
		RequestID: reqID, PassID: pass.PassID, Secret: append([]byte(nil), secret...),
		Device: dev.ID, DeviceXpub: dev.X25519Pub, DisplayName: displayName,
	}
	body := r.signingBody()
	sig := ed25519.Sign(devicePriv, body)
	full := codec.AppendMap(nil, 7)
	full = append(full, body[1:]...)
	full = codec.AppendUint(full, jrSignature)
	full = codec.AppendBytes(full, sig)

	enc, ct, err := crypto.SealTo(pass.AcceptorXpub, joinInfo(pass.Space, pass.PassID), full)
	if err != nil {
		return reqID, nil, err
	}
	sealed = codec.AppendMap(nil, 2)
	sealed = codec.AppendUint(sealed, 1)
	sealed = codec.AppendBytes(sealed, enc)
	sealed = codec.AppendUint(sealed, 2)
	sealed = codec.AppendBytes(sealed, ct)
	return reqID, sealed, nil
}

// OpenJoinRequest opens a sealed request with the acceptor device's X25519
// private scalar and verifies the requester's device signature.
func OpenJoinRequest(space id.TerminalID, passID [16]byte, xpriv [32]byte, sealed []byte) (*JoinRequest, error) {
	enc, ct, err := splitSealed(sealed)
	if err != nil {
		return nil, err
	}
	plain, err := crypto.OpenFrom(xpriv, joinInfo(space, passID), enc, ct)
	if err != nil {
		return nil, errors.New("terminals: cannot open join request")
	}
	r, sig, err := decodeJoinRequest(plain)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(ed25519.PublicKey(r.Device[:]), r.signingBody(), sig) {
		return nil, errors.New("terminals: join request signature invalid")
	}
	if r.PassID != passID || passIDFromSecret(r.Secret) != passID {
		return nil, errors.New("terminals: join request does not match this pass")
	}
	return r, nil
}

func decodeJoinRequest(b []byte) (*JoinRequest, []byte, error) {
	d := codec.NewDecoder(b)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, nil, err
	}
	r := &JoinRequest{}
	var sig []byte
	read := func(dst []byte) error {
		v, e := d.ReadBytes()
		if e != nil {
			return e
		}
		if len(v) != len(dst) {
			return errors.New("terminals: join field wrong size")
		}
		copy(dst, v)
		return nil
	}
	for {
		k, ok, er := m.Next()
		if er != nil {
			return nil, nil, er
		}
		if !ok {
			break
		}
		switch k {
		case jrRequestID:
			er = read(r.RequestID[:])
		case jrPassID:
			er = read(r.PassID[:])
		case jrSecret:
			var v []byte
			v, er = d.ReadBytes()
			r.Secret = append([]byte(nil), v...)
		case jrDevice:
			er = read(r.Device[:])
		case jrDeviceX:
			er = read(r.DeviceXpub[:])
		case jrName:
			r.DisplayName, er = d.ReadText()
		case jrSignature:
			sig, er = d.ReadBytes()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, nil, err
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, nil, errors.New("terminals: join request missing signature")
	}
	return r, sig, nil
}

// ---- Acceptance response (sealed to the newcomer device) ----

// Accepted carries the space manifest and the newcomer's first epoch.
// History carries PAST epoch keys when the space's memory policy shares
// them ("everything"/"manual"); under private_history it stays empty and
// the past remains sealed — enforced by keys, not UI (LR-4 fix: the pass
// path now honors the same policy as invites).
type Accepted struct {
	RequestID     [32]byte
	ManifestFrame []byte
	EpochN        uint64
	EpochKey      [32]byte
	History       []crypto.EpochKey
}

func acceptInfo(space id.TerminalID, reqID [32]byte) []byte {
	info := append([]byte("qs.pass.accepted.v1"), space[:]...)
	return append(info, reqID[:]...)
}

// BuildAccepted seals the acceptance to the newcomer's device X25519 key.
func BuildAccepted(space id.TerminalID, deviceXpub [32]byte, a *Accepted) ([]byte, error) {
	n := 4
	if len(a.History) > 0 {
		n++
	}
	body := codec.AppendMap(nil, n)
	body = codec.AppendUint(body, 1)
	body = codec.AppendBytes(body, a.RequestID[:])
	body = codec.AppendUint(body, 2)
	body = codec.AppendBytes(body, a.ManifestFrame)
	body = codec.AppendUint(body, 3)
	body = codec.AppendUint(body, a.EpochN)
	body = codec.AppendUint(body, 4)
	body = codec.AppendBytes(body, a.EpochKey[:])
	if len(a.History) > 0 {
		body = codec.AppendUint(body, 5)
		body = codec.AppendArray(body, len(a.History))
		for _, e := range a.History {
			body = codec.AppendArray(body, 2)
			body = codec.AppendUint(body, e.N)
			body = codec.AppendBytes(body, e.Key[:])
		}
	}
	enc, ct, err := crypto.SealTo(deviceXpub, acceptInfo(space, a.RequestID), body)
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

// OpenAccepted opens an acceptance with the newcomer's device X25519 scalar.
func OpenAccepted(space id.TerminalID, reqID [32]byte, xpriv [32]byte, sealed []byte) (*Accepted, error) {
	enc, ct, err := splitSealed(sealed)
	if err != nil {
		return nil, err
	}
	plain, err := crypto.OpenFrom(xpriv, acceptInfo(space, reqID), enc, ct)
	if err != nil {
		return nil, errors.New("terminals: cannot open acceptance")
	}
	d := codec.NewDecoder(plain)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	a := &Accepted{}
	for {
		k, ok, er := m.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case 1:
			var v []byte
			v, er = d.ReadBytes()
			if er == nil && len(v) == 32 {
				copy(a.RequestID[:], v)
			}
		case 2:
			var v []byte
			v, er = d.ReadBytes()
			a.ManifestFrame = append([]byte(nil), v...)
		case 3:
			a.EpochN, er = d.ReadUint()
		case 4:
			var v []byte
			v, er = d.ReadBytes()
			if er == nil && len(v) == 32 {
				copy(a.EpochKey[:], v)
			}
		case 5:
			var cnt int
			cnt, er = d.ReadArray()
			if er != nil {
				return nil, er
			}
			for range cnt {
				if _, er = d.ReadArray(); er != nil {
					return nil, er
				}
				var e crypto.EpochKey
				if e.N, er = d.ReadUint(); er != nil {
					return nil, er
				}
				var kb []byte
				if kb, er = d.ReadBytes(); er != nil {
					return nil, er
				}
				if len(kb) != 32 {
					return nil, errors.New("terminals: bad history key")
				}
				copy(e.Key[:], kb)
				a.History = append(a.History, e)
			}
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if a.EpochN == 0 || len(a.ManifestFrame) == 0 {
		return nil, errors.New("terminals: malformed acceptance")
	}
	return a, nil
}

func splitSealed(sealed []byte) (enc, ct []byte, err error) {
	d := codec.NewDecoder(sealed)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, nil, err
	}
	for {
		k, ok, er := m.Next()
		if er != nil {
			return nil, nil, er
		}
		if !ok {
			break
		}
		switch k {
		case 1:
			enc, er = d.ReadBytes()
		case 2:
			ct, er = d.ReadBytes()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, nil, err
	}
	if len(enc) == 0 || len(ct) == 0 {
		return nil, nil, errors.New("terminals: malformed sealed envelope")
	}
	return enc, ct, nil
}

// ---- Rendezvous hints (relay dead-drop addresses) ----

// ReqCap is the OWNER's drain capability for the request dead-drop, and
// ReqHint is the address newcomers write to. Both come from the rendezvous
// secret, which only pass holders have — so the capability protects the
// drop from strangers exactly as the pass protects the space.
func ReqCap(rendezvous [16]byte) []byte {
	h := sha256.Sum256(append([]byte("qs.pass.req.v1"), rendezvous[:]...))
	return h[:]
}

// ReqHint is where the newcomer drops its sealed request.
func ReqHint(rendezvous [16]byte) []byte {
	return relayCollectHint(ReqCap(rendezvous))
}

// RespCap is the NEWCOMER's drain capability for its own acceptance.
func RespCap(rendezvous [16]byte, reqID [32]byte) []byte {
	h := sha256.Sum256(append(append([]byte("qs.pass.resp.v1"), rendezvous[:]...), reqID[:]...))
	return h[:]
}

// RespHint is where the owner drops the sealed acceptance for one request.
func RespHint(rendezvous [16]byte, reqID [32]byte) []byte {
	return relayCollectHint(RespCap(rendezvous, reqID))
}

// FreshNonce returns 16 random bytes for request_id derivation.
func FreshNonce() ([]byte, error) {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	return b, err
}

// AcceptIntoSpace performs the membership half of pass acceptance
// (controller only): register the device, rotate the epoch so the new epoch
// is wrapped to it, publish the canonical membership.member_added.v1 event
// so all participants learn the join, and return the new epoch (n + key) and
// the space manifest frame for the sealed acceptance. History starts here.
func (s *Space) AcceptIntoSpace(self *Participant, device id.DeviceID, xpub [32]byte,
	name string, acceptedBy id.PrincipalID, at uint64) (epochN uint64, epochKey [32]byte, manifest []byte, err error) {

	if s.priv == nil {
		return 0, epochKey, nil, errors.New("terminals: only the controller can accept")
	}
	// Already in? Then this is the same person asking again — a second pass,
	// a re-sent request, a link handed out twice — and admitting them again
	// would mean re-keying the space for EVERYONE and writing another
	// member_added into a log that keeps everything forever. Neither is a
	// thing that should happen because somebody clicked twice.
	//
	// The current epoch still goes back, so a guest who lost their copy can
	// converge. That discloses nothing: this device is a member and already
	// holds that key.
	if s.HasMember(device) {
		n := s.priv2.current
		return n, s.priv2.epochs[n].Key, append([]byte(nil), s.ManifestFrame...), nil
	}
	s.AddMember(device, xpub)
	if _, err := self.RotateEpoch(s); err != nil {
		return 0, epochKey, nil, err
	}
	epochN = s.priv2.current
	epochKey = s.priv2.epochs[epochN].Key

	payload := (&schemas.MemberAddedPayload{Device: device, Name: name, AcceptedBy: acceptedBy, AcceptedAt: at}).Encode()
	if _, err := self.Emit(s, schemas.MemberAdded, payload, self.DefaultAuthorship(), at); err != nil {
		return 0, epochKey, nil, err
	}
	return epochN, epochKey, append([]byte(nil), s.ManifestFrame...), nil
}

// relayCollectHint mirrors relay.CollectHint. terminals must not import
// transports, so the derivation is restated here and pinned by a test that
// compares the two.
func relayCollectHint(cap []byte) []byte {
	h := sha256.New()
	h.Write([]byte("qp-relay-collect-v1:"))
	h.Write(cap)
	return h.Sum(nil)[:16]
}

// DecodePassFrame parses and VERIFIES a signed pass frame on its own —
// no bearer secret, because an owner reloading its own journal never needs
// one. The space id IS the verification key (ADR-001), so a frame that came
// back from disk is checked exactly as one that came off the wire: a plain
// file is not a reason to trust bytes.
func DecodePassFrame(frame []byte) (*Pass, error) {
	p, sig, err := decodePassFrame(frame)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(ed25519.PublicKey(p.Space[:]), p.signingBody(), sig) {
		return nil, errors.New("terminals: pass signature invalid")
	}
	if p.Profile != PassProfileMember {
		return nil, errors.New("terminals: unsupported pass profile")
	}
	return p, nil
}
