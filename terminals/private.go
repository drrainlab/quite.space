// Private-space support (M1, ADR-005): epoch state on the replica,
// encrypted emit/absorb, epoch rotation on membership change, and signed
// capability invites. Undecryptable events are counted, never hidden —
// a non-member replica honestly reports how much it cannot read.
package terminals

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"sort"

	"github.com/drrainlab/quiet_places/kernel/crypto"
	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// privateState lives on a Space replica that participates in encryption.
type privateState struct {
	epochs  map[uint64]crypto.EpochKey
	current uint64
	self    *identity.Device // whose X25519 key unwraps epoch events
	// members is maintained only on the controller's replica: it is the
	// wrap list for the next rotation.
	members map[id.DeviceID][32]byte
}

// EnablePrivate marks the space as encrypted and binds the local device
// used for unwrapping epochs.
func (s *Space) EnablePrivate(self *identity.Device) {
	s.Private = true
	if s.priv2 == nil {
		s.priv2 = &privateState{
			epochs:  map[uint64]crypto.EpochKey{},
			members: map[id.DeviceID][32]byte{},
		}
	}
	s.priv2.self = self
}

// CurrentEpoch returns the highest epoch number seen in the log.
func (s *Space) CurrentEpoch() uint64 {
	if s.priv2 == nil {
		return 0
	}
	return s.priv2.current
}

// KnowsEpoch reports whether this replica can decrypt the given epoch.
func (s *Space) KnowsEpoch(n uint64) bool {
	if s.priv2 == nil {
		return false
	}
	_, ok := s.priv2.epochs[n]
	return ok
}

// AddMember records a member device for future rotations (controller side).
func (s *Space) AddMember(dev id.DeviceID, xpub [32]byte) {
	if s.priv2 == nil {
		s.priv2 = &privateState{
			epochs:  map[uint64]crypto.EpochKey{},
			members: map[id.DeviceID][32]byte{},
		}
	}
	s.priv2.members[dev] = xpub
}

// RemoveMember drops a device from future rotations (controller side). The
// caller must rotate afterwards; removal without rotation changes nothing.
func (s *Space) RemoveMember(dev id.DeviceID) {
	if s.priv2 != nil {
		delete(s.priv2.members, dev)
	}
}

// RotateEpoch (controller only): mints epoch current+1 for the present
// member set and publishes the wraps as a membership.epoch.v1 event in the
// security lane. Every membership change calls this (ADR-005).
func (p *Participant) RotateEpoch(s *Space) (eventlog.Applied, error) {
	if s.priv2 == nil || len(s.priv2.members) == 0 {
		return eventlog.Applied{}, errors.New("terminals: no members to rotate for")
	}
	n := s.priv2.current + 1
	key, err := crypto.NewEpochKey(n)
	if err != nil {
		return eventlog.Applied{}, err
	}
	wraps, err := crypto.WrapEpoch(s.ID, key, s.priv2.members)
	if err != nil {
		return eventlog.Applied{}, err
	}
	payload, err := (&schemas.EpochPayload{N: n, Wraps: wraps}).Encode()
	if err != nil {
		return eventlog.Applied{}, err
	}
	// The controller knows the key it just minted, regardless of wraps.
	s.priv2.epochs[n] = key
	s.priv2.current = n
	return p.Emit(s, schemas.MembershipEpoch, payload, p.DefaultAuthorship(), 0)
}

// DefaultAuthorship maps the participant's declared agency to its honest
// authorship mark.
func (p *Participant) DefaultAuthorship() signal.Authorship {
	switch p.Manifest.AgencyMode {
	case manifest.AgencyHuman:
		return signal.AuthorshipHuman
	case manifest.AgencyDeterministic:
		return signal.AuthorshipDeterministicBot
	case manifest.AgencyAIAgent:
		return signal.AuthorshipAIAgent
	default:
		return signal.AuthorshipUnknown
	}
}

// absorbEpoch handles a membership.epoch.v1 event on any replica: track the
// current epoch number, and learn the key if a wrap addresses our device.
func (s *Space) absorbEpoch(env *signal.Envelope) {
	ep, err := schemas.DecodeEpochPayload(env.Payload)
	if err != nil {
		return
	}
	if s.priv2 == nil {
		s.priv2 = &privateState{
			epochs:  map[uint64]crypto.EpochKey{},
			members: map[id.DeviceID][32]byte{},
		}
	}
	if ep.N > s.priv2.current {
		s.priv2.current = ep.N
	}
	if s.priv2.self == nil {
		return
	}
	key, err := crypto.UnwrapEpoch(s.ID, ep.N, ep.Wraps, s.priv2.self.ID, s.priv2.self.X25519Priv())
	if err != nil {
		return // not addressed: normal for non-members and removed members
	}
	s.priv2.epochs[ep.N] = key
}

// EpochHistory returns every epoch key this replica holds, ascending by
// epoch. Used by pass acceptance when the space's memory policy shares
// history ("everything"/"manual"); private_history spaces never call it.
func (s *Space) EpochHistory() []crypto.EpochKey {
	if s.priv2 == nil {
		return nil
	}
	out := make([]crypto.EpochKey, 0, len(s.priv2.epochs))
	for _, k := range s.priv2.epochs {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })
	return out
}

// sealForEmit encrypts a payload for the current epoch. Without the key the
// device simply cannot write — membership is enforced by cryptography, not
// by UI (fail closed).
func (s *Space) sealForEmit(schema string, plaintext []byte) ([]byte, error) {
	key, ok := s.priv2.epochs[s.priv2.current]
	if !ok || s.priv2.current == 0 {
		return nil, errors.New("terminals: no current epoch key — not a member of this private space")
	}
	return crypto.SealPayload(key, s.ID, schema, plaintext)
}

// openForAbsorb tries to decrypt an incoming sealed payload. (nil, false)
// means "cannot read": counted, never guessed at.
func (s *Space) openForAbsorb(env *signal.Envelope) ([]byte, bool) {
	if s.priv2 == nil {
		return nil, false
	}
	n, err := crypto.SealedEpoch(env.Payload)
	if err != nil {
		return nil, false
	}
	key, ok := s.priv2.epochs[n]
	if !ok {
		return nil, false
	}
	pt, err := crypto.OpenPayload(key, s.ID, env.Schema, env.Payload)
	if err != nil {
		return nil, false
	}
	return pt, true
}

// ---- Capability invite (vision §6.3, plan M1.2) ----

// Invite wire key table v0 (body map; the outer wrapper is {1: body, 2: sig}).
const (
	invKeySpace    = 1
	invKeyDevice   = 2
	invKeyEpochs   = 3
	invKeyManifest = 4
)

// NewInvite builds an invite that shares the full history (all epochs).
func (s *Space) NewInvite(inviteeDevice id.DeviceID, inviteeXpub [32]byte) ([]byte, error) {
	return s.NewInviteWithHistory(inviteeDevice, inviteeXpub, true)
}

// NewInviteWithHistory (controller only) builds a signed invite for one
// device. includeHistory=false wraps only the current epoch: the newcomer
// reads from now on, and the past stays cryptographically closed — this is
// the "private history" memory policy, enforced by keys, not UI.
func (s *Space) NewInviteWithHistory(inviteeDevice id.DeviceID, inviteeXpub [32]byte, includeHistory bool) ([]byte, error) {
	if s.priv == nil {
		return nil, errors.New("terminals: only the controller replica can invite")
	}
	if s.priv2 == nil || s.priv2.current == 0 {
		return nil, errors.New("terminals: private space has no epoch yet")
	}
	firstEpoch := uint64(1)
	if !includeHistory {
		firstEpoch = s.priv2.current
	}
	target := map[id.DeviceID][32]byte{inviteeDevice: inviteeXpub}
	body := codec.AppendMap(nil, 4)
	body = codec.AppendUint(body, invKeySpace)
	body = codec.AppendBytes(body, s.ID[:])
	body = codec.AppendUint(body, invKeyDevice)
	body = codec.AppendBytes(body, inviteeDevice[:])
	body = codec.AppendUint(body, invKeyEpochs)
	body = codec.AppendArray(body, int(s.priv2.current-firstEpoch+1))
	for n := firstEpoch; n <= s.priv2.current; n++ {
		key, ok := s.priv2.epochs[n]
		if !ok {
			return nil, fmt.Errorf("terminals: controller is missing epoch %d", n)
		}
		wraps, err := crypto.WrapEpoch(s.ID, key, target)
		if err != nil {
			return nil, err
		}
		body = codec.AppendArray(body, 3)
		body = codec.AppendUint(body, n)
		body = codec.AppendBytes(body, wraps[0].Enc)
		body = codec.AppendBytes(body, wraps[0].CT)
	}
	body = codec.AppendUint(body, invKeyManifest)
	body = codec.AppendBytes(body, s.ManifestFrame)
	sig := ed25519.Sign(s.priv, body)
	out := append([]byte(nil), body...)
	// Note: signature over the body bytes; appended as a trailing entry of
	// a wrapper map for transport.
	wrapper := codec.AppendMap(nil, 2)
	wrapper = codec.AppendUint(wrapper, 1)
	wrapper = codec.AppendBytes(wrapper, out)
	wrapper = codec.AppendUint(wrapper, 2)
	wrapper = codec.AppendBytes(wrapper, sig)
	return wrapper, nil
}

// AcceptInvite verifies an invite and builds a joined in-memory private
// replica bound to the invitee device.
func AcceptInvite(invite []byte, dev *identity.Device) (*Space, error) {
	spaceID, manifestFrame, keys, err := DecodeInvite(invite, dev)
	if err != nil {
		return nil, err
	}
	s := Replica(spaceID)
	s.ManifestFrame = manifestFrame
	s.EnablePrivate(dev)
	s.RestoreEpochs(keys)
	return s, nil
}

// DecodeInvite verifies an invite's signature and addressing and returns
// the space id, manifest frame, and unwrapped epoch keys. Callers decide
// where the replica lives (memory or disk).
func DecodeInvite(invite []byte, dev *identity.Device) (id.TerminalID, []byte, []crypto.EpochKey, error) {
	d := codec.NewDecoder(invite)
	m, err := d.ReadMapHeader()
	if err != nil {
		return id.TerminalID{}, nil, nil, err
	}
	var body, sig []byte
	for {
		k, ok, er := m.Next()
		if er != nil {
			return id.TerminalID{}, nil, nil, er
		}
		if !ok {
			break
		}
		switch k {
		case 1:
			body, er = d.ReadBytes()
		case 2:
			sig, er = d.ReadBytes()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return id.TerminalID{}, nil, nil, er
		}
	}
	if err := d.Done(); err != nil {
		return id.TerminalID{}, nil, nil, err
	}
	if body == nil || len(sig) != ed25519.SignatureSize {
		return id.TerminalID{}, nil, nil, errors.New("terminals: malformed invite")
	}

	bd := codec.NewDecoder(body)
	bm, err := bd.ReadMapHeader()
	if err != nil {
		return id.TerminalID{}, nil, nil, err
	}
	var spaceID id.TerminalID
	var invitee id.DeviceID
	type epochEntry struct {
		n       uint64
		enc, ct []byte
	}
	var epochs []epochEntry
	var manifestFrame []byte
	for {
		k, ok, er := bm.Next()
		if er != nil {
			return id.TerminalID{}, nil, nil, er
		}
		if !ok {
			break
		}
		switch k {
		case invKeySpace:
			var b []byte
			b, er = bd.ReadBytes()
			if er == nil && len(b) == id.Size {
				copy(spaceID[:], b)
			}
		case invKeyDevice:
			var b []byte
			b, er = bd.ReadBytes()
			if er == nil && len(b) == id.Size {
				copy(invitee[:], b)
			}
		case invKeyEpochs:
			var cnt int
			cnt, er = bd.ReadArray()
			if er != nil {
				return id.TerminalID{}, nil, nil, er
			}
			for range cnt {
				if _, er = bd.ReadArray(); er != nil {
					return id.TerminalID{}, nil, nil, er
				}
				var e epochEntry
				if e.n, er = bd.ReadUint(); er != nil {
					return id.TerminalID{}, nil, nil, er
				}
				if e.enc, er = bd.ReadBytes(); er != nil {
					return id.TerminalID{}, nil, nil, er
				}
				if e.ct, er = bd.ReadBytes(); er != nil {
					return id.TerminalID{}, nil, nil, er
				}
				epochs = append(epochs, e)
			}
		case invKeyManifest:
			manifestFrame, er = bd.ReadBytes()
		default:
			er = bd.SkipItem()
		}
		if er != nil {
			return id.TerminalID{}, nil, nil, er
		}
	}
	if err := bd.Done(); err != nil {
		return id.TerminalID{}, nil, nil, err
	}
	// The invite is signed by the space's terminal key: the space id IS the
	// verification key (ADR-001).
	if !ed25519.Verify(ed25519.PublicKey(spaceID[:]), body, sig) {
		return id.TerminalID{}, nil, nil, errors.New("terminals: invite signature invalid")
	}
	if invitee != dev.ID {
		return id.TerminalID{}, nil, nil, errors.New("terminals: invite addressed to a different device")
	}
	var keys []crypto.EpochKey
	for _, e := range epochs {
		wraps := []crypto.Wrap{{Device: dev.ID, Enc: e.enc, CT: e.ct}}
		key, err := crypto.UnwrapEpoch(spaceID, e.n, wraps, dev.ID, dev.X25519Priv())
		if err != nil {
			return id.TerminalID{}, nil, nil, fmt.Errorf("terminals: cannot unwrap epoch %d: %w", e.n, err)
		}
		keys = append(keys, key)
	}
	return spaceID, append([]byte(nil), manifestFrame...), keys, nil
}
