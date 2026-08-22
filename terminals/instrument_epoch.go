package terminals

// The INSTRUMENT EPOCH — the space's second key lineage (QI-0, ADR-025).
//
// One sentence of doctrine, stated before any mechanism: this lineage
// isolates instruments from the human conversation; it does NOT isolate
// instruments from one another. Members and instruments share the
// instrument key, so any attached instrument can read any other's
// telemetry — the named, accepted price of one symmetric key in v1. The
// conversation epoch never reaches an instrument: its device is simply
// never in that wrap list, and cryptography does the refusing.
//
// Everything here is the conversation-epoch machinery run a second time
// with two deliberate differences:
//
//   - DOMAIN SEPARATION. Both lineages count 1, 2, 3… — so wraps and
//     seals bind to a derived plane id, never the raw space id. A
//     controller bug (or a replayed wrap) can therefore never make a
//     conversation key answer for an instrument frame or vice versa:
//     the AAD disagrees before any map is consulted.
//   - THE ENVELOPE NAMES THE LINEAGE. PayloadInstrumentSealed says which
//     map to open with; a decoder never guesses between same-numbered
//     epochs.
//
// Rotation happens on every event the owner's plan names: member added
// or removed, a device revoked, an instrument attached, detached or
// revoked. The node's rotation sites call both rotations together.

import (
	"crypto/sha256"
	"errors"

	"github.com/drrainlab/quiet_places/kernel/crypto"
	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// InstrumentPlaneID derives the instrument lineage's binding id from the
// space id — the domain separation both wraps and seals use. Exported
// because it is part of the wire contract a second implementation (the
// C core, QI-M) must reproduce: SHA256("qp.instrument-plane.v1" ‖ space).
func InstrumentPlaneID(space id.TerminalID) id.TerminalID {
	sum := sha256.Sum256(append([]byte("qp.instrument-plane.v1"), space[:]...))
	var out id.TerminalID
	copy(out[:], sum[:])
	return out
}

// instrState is the second, smaller privateState. Kept separate rather
// than folded into priv2's maps so a same-numbered epoch can never be
// looked up in the wrong lineage.
type instrState struct {
	epochs  map[uint64]crypto.EpochKey
	current uint64
	// instruments is the controller-side wrap ADDITION: instrument devices
	// joined with the conversation member list at rotation time.
	instruments map[id.DeviceID][32]byte
	// authorized is the receiver-side AUTHORIZATION SET: the devices the
	// CURRENT instrument epoch addresses. Detachment is a boundary of
	// authority, not merely of secrecy (owner's QI-M amendment 4): a
	// detached device may well still hold an older epoch key — members
	// keep old keys for delayed delivery — so "it cannot read the new
	// secret" is not the revocation. "Its identity is no longer addressed
	// by the current epoch" is. The set only ever moves FORWARD with the
	// epoch number (amendment 13): a late-arriving lower epoch never
	// rolls it back.
	authorized map[id.DeviceID]bool
	// authEpoch is the epoch the authorization set was taken from. Kept
	// APART from current on purpose: RestoreInstrumentEpochs raises
	// current from the keystore before the log is replayed, and if the
	// set were keyed to current, replay would never install it — a
	// restarted member refused every reading until the next rotation.
	// The node restart test found exactly that.
	authEpoch uint64
}

func (s *Space) instrInit() {
	if s.instr == nil {
		s.instr = &instrState{
			epochs:      map[uint64]crypto.EpochKey{},
			instruments: map[id.DeviceID][32]byte{},
		}
	}
}

// AddInstrument records an instrument device for future instrument-epoch
// rotations (controller side). The caller rotates afterwards — recording
// without rotating changes nothing, exactly as AddMember insists.
func (s *Space) AddInstrument(dev id.DeviceID, xpub [32]byte) {
	s.instrInit()
	s.instr.instruments[dev] = xpub
}

// RemoveInstrument drops an instrument from future rotations. The caller
// must rotate afterwards; a detached instrument keeps reading until the
// key turns — which is why detach ROTATES, always.
func (s *Space) RemoveInstrument(dev id.DeviceID) {
	if s.instr != nil {
		delete(s.instr.instruments, dev)
	}
}

// HasInstrument reports whether a device is carried by instrument
// rotations.
func (s *Space) HasInstrument(dev id.DeviceID) bool {
	if s.instr == nil {
		return false
	}
	_, ok := s.instr.instruments[dev]
	return ok
}

// InstrumentCount is how many instrument devices rotations carry.
func (s *Space) InstrumentCount() int {
	if s.instr == nil {
		return 0
	}
	return len(s.instr.instruments)
}

// CurrentInstrumentEpoch returns the highest instrument epoch seen.
func (s *Space) CurrentInstrumentEpoch() uint64 {
	if s.instr == nil {
		return 0
	}
	return s.instr.current
}

// KnowsInstrumentEpoch reports whether this replica can open the given
// instrument epoch.
func (s *Space) KnowsInstrumentEpoch(n uint64) bool {
	if s.instr == nil {
		return false
	}
	_, ok := s.instr.epochs[n]
	return ok
}

// RotateInstrumentEpoch (controller only): mints the next instrument
// epoch for members ∪ instruments and publishes the wraps. A space with
// no instruments has no plane and nothing to rotate — a clean no-op, so
// the node may call this beside every conversation rotation without
// checking first.
func (p *Participant) RotateInstrumentEpoch(s *Space) (eventlog.Applied, error) {
	// No plane, nothing to rotate — but ONLY when the plane never
	// existed. A plane whose LAST instrument was just detached must
	// still turn the key: the first version no-opped here and the
	// detached device kept reading every future observation, which the
	// detach test caught on its first run.
	if s.instr == nil || (len(s.instr.instruments) == 0 && s.instr.current == 0) {
		return eventlog.Applied{}, nil
	}
	if s.priv2 == nil || len(s.priv2.members) == 0 {
		return eventlog.Applied{}, errors.New("terminals: no members to rotate instruments for")
	}
	recipients := make(map[id.DeviceID][32]byte,
		len(s.priv2.members)+len(s.instr.instruments))
	for dev, x := range s.priv2.members {
		recipients[dev] = x
	}
	for dev, x := range s.instr.instruments {
		recipients[dev] = x
	}
	n := s.instr.current + 1
	key, err := crypto.NewEpochKey(n)
	if err != nil {
		return eventlog.Applied{}, err
	}
	wraps, err := crypto.WrapEpoch(InstrumentPlaneID(s.ID), key, recipients)
	if err != nil {
		return eventlog.Applied{}, err
	}
	payload, err := (&schemas.EpochPayload{N: n, Wraps: wraps}).Encode()
	if err != nil {
		return eventlog.Applied{}, err
	}
	s.instr.epochs[n] = key
	s.instr.current = n
	// The controller's own absorb will see ep.N == current and leave the
	// authorization set alone, so it is installed here, from the same
	// recipient list the wraps were minted for.
	auth := make(map[id.DeviceID]bool, len(recipients))
	for dev := range recipients {
		auth[dev] = true
	}
	s.instr.authorized, s.instr.authEpoch = auth, n
	return p.Emit(s, schemas.InstrumentEpoch, payload, p.DefaultAuthorship(), 0)
}

// absorbInstrumentEpoch mirrors absorbEpoch for the second lineage: track
// the number, learn the key if a wrap addresses our device. Not being
// addressed is normal life — a conversation-only member of somebody
// else's space, a removed instrument after the turn.
func (s *Space) absorbInstrumentEpoch(env *signal.Envelope) {
	ep, err := schemas.DecodeEpochPayload(env.Payload)
	if err != nil {
		return
	}
	s.instrInit()
	if ep.N > s.instr.current {
		s.instr.current = ep.N
	}
	if ep.N > s.instr.authEpoch {
		// The newest epoch's recipients ARE the authorization set — the
		// owner signed this list, and a guest learns who may speak on
		// the plane from exactly this frame. Strictly greater: an older
		// epoch arriving late (a second authority device, a replay)
		// keeps its key usable for delayed opens but never re-authorizes
		// a device the newer epoch dropped.
		auth := make(map[id.DeviceID]bool, len(ep.Wraps))
		for _, w := range ep.Wraps {
			auth[w.Device] = true
		}
		s.instr.authorized, s.instr.authEpoch = auth, ep.N
	}
	self := s.instrSelf()
	if self == nil {
		return
	}
	key, err := crypto.UnwrapEpoch(InstrumentPlaneID(s.ID), ep.N, ep.Wraps, self.ID, self.X25519Priv())
	if err != nil {
		return
	}
	s.instr.epochs[ep.N] = key
}

// InstrumentAuthorized reports whether a device is addressed by the
// current instrument epoch — the receiver-side authority check every
// instrument-sealed frame passes BEFORE any key is tried. A replica that
// has not yet seen an instrument epoch authorizes nobody: fail closed,
// and the reading is fleeting — the next tick arrives after the epoch.
func (s *Space) InstrumentAuthorized(dev id.DeviceID) bool {
	if s.instr == nil || s.instr.authorized == nil {
		return false
	}
	return s.instr.authorized[dev]
}

// instrSelf is the device that unwraps for THIS replica — the same device
// EnablePrivate bound. One device, two lineages: which wraps address it
// decides which planes it can read.
func (s *Space) instrSelf() *identity.Device {
	if s.priv2 == nil {
		return nil
	}
	return s.priv2.self
}

// sealForInstrument encrypts an instrument-plane payload for the current
// instrument epoch. Fail closed: without the key the device cannot speak
// on this plane, exactly as sealForEmit refuses for the conversation.
func (s *Space) sealForInstrument(schema string, plaintext []byte) ([]byte, error) {
	if s.instr == nil {
		return nil, errors.New("terminals: no instrument epoch yet — the plane has not been opened")
	}
	key, ok := s.instr.epochs[s.instr.current]
	if !ok || s.instr.current == 0 {
		return nil, errors.New("terminals: no current instrument epoch key")
	}
	return crypto.SealPayload(key, InstrumentPlaneID(s.ID), schema, plaintext)
}

// openInstrument tries to decrypt an instrument-sealed payload.
// (nil, false) means "cannot read": counted, never guessed at.
func (s *Space) openInstrument(env *signal.Envelope) ([]byte, bool) {
	if s.instr == nil {
		return nil, false
	}
	n, err := crypto.SealedEpoch(env.Payload)
	if err != nil {
		return nil, false
	}
	key, ok := s.instr.epochs[n]
	if !ok {
		return nil, false
	}
	pt, err := crypto.OpenPayload(key, InstrumentPlaneID(s.ID), env.Schema, env.Payload)
	if err != nil {
		return nil, false
	}
	return pt, true
}

// ExportInstrumentEpochs / RestoreInstrumentEpochs — the restart gate.
// Losing the instrument lineage on restart would silently blind a member
// to every reading until the next rotation.
func (s *Space) ExportInstrumentEpochs() []crypto.EpochKey {
	if s.instr == nil {
		return nil
	}
	out := make([]crypto.EpochKey, 0, len(s.instr.epochs))
	for n := uint64(1); n <= s.instr.current; n++ {
		if k, ok := s.instr.epochs[n]; ok {
			out = append(out, k)
		}
	}
	return out
}

func (s *Space) RestoreInstrumentEpochs(keys []crypto.EpochKey) {
	if len(keys) == 0 {
		return
	}
	s.instrInit()
	for _, k := range keys {
		s.instr.epochs[k.N] = k
		if k.N > s.instr.current {
			s.instr.current = k.N
		}
	}
}

// AbsorbInstrumentEpochFrame is the DEVICE'S way of learning an epoch —
// what a firmware does, modelled here so the Go side can play one (QI-M).
//
// An instrument is not a replica: it does not carry the owner's chain,
// the space manifest, or anybody's history, so the ordinary Absorb would
// park an owner frame with an unseen predecessor as pending forever. It
// checks what a device CAN check — the envelope's signature, that the
// frame is for this space, carries this lineage's schema and was issued
// by the principal the device was provisioned to — and absorbs the key
// distribution chain-free. The sender's device certificate is not
// verified here: a device holds no certificate store, and the bearer that
// will one day deliver rotations is where that trust gets decided.
func (s *Space) AbsorbInstrumentEpochFrame(frame []byte, principal id.PrincipalID) error {
	env, err := signal.Decode(frame)
	if err != nil {
		return err
	}
	if err := signal.VerifyFrame(frame, env); err != nil {
		return err
	}
	if env.Terminal != s.ID {
		return errors.New("terminals: epoch frame is for another space")
	}
	if env.Schema != schemas.InstrumentEpoch {
		return errors.New("terminals: not an instrument epoch frame")
	}
	if env.Principal != principal {
		return errors.New("terminals: epoch frame is not from this instrument's principal")
	}
	if env.LogicalClock > s.maxClock {
		s.maxClock = env.LogicalClock
	}
	s.absorbInstrumentEpoch(env)
	return nil
}
