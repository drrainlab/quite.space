// Persistence support (M1.0/M1.B): disk-backed spaces, participants built
// from stored identity, chain resume after restart, and epoch export/import
// for the encrypted keystore. Everything here re-derives state from the log
// plus key material — there is no second source of truth.
package terminals

import (
	"crypto/ed25519"
	"errors"

	"github.com/drrainlab/quiet_places/kernel/crypto"
	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
)

// NewSpaceAt creates a Space whose log persists under dir.
func NewSpaceAt(title string, controller id.PrincipalID, dir string) (*Space, error) {
	s, err := NewSpace(title, controller)
	if err != nil {
		return nil, err
	}
	log, replayed, err := eventlog.Open(s.ID, dir, nil)
	if err != nil {
		return nil, err
	}
	if len(replayed) != 0 {
		return nil, errors.New("terminals: directory already holds another space's events")
	}
	s.Log = log
	return s, nil
}

// OpenReplicaAt opens (or creates) a disk-backed replica and folds the
// stored events into materialized state. For private spaces, restore epoch
// keys BEFORE calling this (or use AttachLog) — otherwise the replay cannot
// decrypt and honestly lands everything in Undecryptable.
func OpenReplicaAt(tid id.TerminalID, dir string) (*Space, error) {
	log, replayed, err := eventlog.Open(tid, dir, nil)
	if err != nil {
		return nil, err
	}
	s := Replica(tid)
	s.AttachLog(log, replayed)
	return s, nil
}

// AttachLog binds a disk-backed log to a prepared replica (keys and
// controller state already restored) and absorbs the replayed events.
func (s *Space) AttachLog(log *eventlog.Log, replayed []eventlog.Applied) {
	s.Log = log
	for _, a := range replayed {
		s.absorb(a)
	}
}

// TerminalSeed exports the controller's terminal key seed for the keystore.
// Only the creating replica holds it.
func (s *Space) TerminalSeed() ([]byte, error) {
	if s.priv == nil {
		return nil, errors.New("terminals: not the controller replica")
	}
	return s.priv.Seed(), nil
}

// RestoreController reattaches controller state after a restart: terminal
// key, manifest frame, and the member list for future rotations.
func (s *Space) RestoreController(terminalSeed, manifestFrame []byte, members map[id.DeviceID][32]byte) error {
	if len(terminalSeed) != ed25519.SeedSize {
		return errors.New("terminals: bad terminal seed")
	}
	priv := ed25519.NewKeyFromSeed(terminalSeed)
	pub := priv.Public().(ed25519.PublicKey)
	if string(pub) != string(s.ID[:]) {
		return errors.New("terminals: terminal seed does not match space id")
	}
	s.priv = priv
	s.ManifestFrame = append([]byte(nil), manifestFrame...)
	for dev, xpub := range members {
		s.AddMember(dev, xpub)
	}
	return nil
}

// Members exports the controller's member list for the keystore.
func (s *Space) Members() map[id.DeviceID][32]byte {
	if s.priv2 == nil {
		return nil
	}
	out := make(map[id.DeviceID][32]byte, len(s.priv2.members))
	for k, v := range s.priv2.members {
		out[k] = v
	}
	return out
}

// ExportEpochs returns every epoch key this replica knows, for the
// keystore. Losing these on restart would lock the user out of their own
// space (M1.0).
func (s *Space) ExportEpochs() []crypto.EpochKey {
	if s.priv2 == nil {
		return nil
	}
	out := make([]crypto.EpochKey, 0, len(s.priv2.epochs))
	for n := uint64(1); n <= s.priv2.current; n++ {
		if k, ok := s.priv2.epochs[n]; ok {
			out = append(out, k)
		}
	}
	return out
}

// RestoreEpochs reloads stored epoch keys into the replica.
func (s *Space) RestoreEpochs(keys []crypto.EpochKey) {
	if s.priv2 == nil {
		s.priv2 = &privateState{
			epochs:  map[uint64]crypto.EpochKey{},
			members: map[id.DeviceID][32]byte{},
		}
	}
	for _, k := range keys {
		s.priv2.epochs[k.N] = k
		if k.N > s.priv2.current {
			s.priv2.current = k.N
		}
	}
}

// NewParticipantFrom builds a participant on an existing identity. When
// terminalSeed is nil a fresh terminal key is generated; the caller
// persists the returned seed.
func NewParticipantFrom(prin *identity.Principal, dev *identity.Device,
	terminalSeed []byte, template manifest.Manifest) (*Participant, []byte, error) {

	var priv ed25519.PrivateKey
	if terminalSeed == nil {
		var err error
		_, priv, err = ed25519.GenerateKey(identity.NewRand())
		if err != nil {
			return nil, nil, err
		}
	} else {
		if len(terminalSeed) != ed25519.SeedSize {
			return nil, nil, errors.New("terminals: bad participant terminal seed")
		}
		priv = ed25519.NewKeyFromSeed(terminalSeed)
	}
	p := &Participant{
		Principal:    prin,
		Device:       dev,
		terminalPriv: priv,
		chains:       map[id.TerminalID]*chainState{},
	}
	copy(p.TerminalID[:], priv.Public().(ed25519.PublicKey))
	template.Terminal = p.TerminalID
	template.Controller = prin.ID
	if template.Revision == 0 {
		template.Revision = 1
	}
	frame, err := template.Sign(priv)
	if err != nil {
		return nil, nil, err
	}
	p.Manifest = &template
	p.ManifestFrame = frame
	return p, priv.Seed(), nil
}

// ResumeChain restores the participant's per-space sequence counter and tip
// from the log, so writing continues seamlessly after a restart.
func (p *Participant) ResumeChain(s *Space) {
	seq, tip, ok := s.Log.ChainTip(p.Device.ID)
	if !ok {
		return
	}
	p.chains[s.ID] = &chainState{seq: seq, tip: tip}
	p.ObserveClock(s.MaxClock())
}

// MaxClock returns the highest Lamport clock absorbed so far.
func (s *Space) MaxClock() uint64 { return s.maxClock }
