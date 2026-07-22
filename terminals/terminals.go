// Package terminals is the shared runtime for headless terminals (M0.8):
// a Space replica plus Participants whose every action is checked against
// their own signed manifest. A participant physically cannot perform an
// operation its manifest does not declare (invariant §2.2), and cannot mark
// its events with an authorship its agency does not permit (plan §10).
package terminals

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/kernel/reducers"
	"github.com/drrainlab/quiet_places/kernel/registry"
	"github.com/drrainlab/quiet_places/kernel/trust"
	"github.com/drrainlab/quiet_places/protocol/capability"
	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// Errors: undeclared operations fail closed, loudly.
var (
	ErrUndeclaredOperation = errors.New("terminals: operation not declared in manifest")
	ErrAuthorshipForbidden = errors.New("terminals: authorship not permitted by declared agency")
)

// Space is one node's replica of a Space Terminal.
type Space struct {
	ID    id.TerminalID
	Log   *eventlog.Log
	State *reducers.State
	Trust *trust.Engine
	// Registry holds member terminal manifests published into the space
	// (M1.3): the source for device/agent cards.
	Registry *registry.Registry

	// Private marks an encrypted space (ADR-005). Undecryptable counts
	// events this replica received but could not read — shown, not hidden.
	Private       bool
	Undecryptable int

	// priv is held only by the creating node (the controller's replica).
	priv          ed25519.PrivateKey
	priv2         *privateState
	ManifestFrame []byte
	maxClock      uint64
}

// NewSpace creates a Space Terminal: keypair, signed manifest, empty log.
func NewSpace(title string, controller id.PrincipalID) (*Space, error) {
	pub, priv, err := ed25519.GenerateKey(identity.NewRand())
	if err != nil {
		return nil, err
	}
	var tid id.TerminalID
	copy(tid[:], pub)
	m := &manifest.Manifest{
		Terminal:       tid,
		Controller:     controller,
		Kind:           manifest.KindSpace,
		DeclaredLabels: []string{title},
		IOMode:         manifest.IODuplex,
		Capabilities: []string{capability.SignalPublish, capability.SignalReceive,
			capability.PresencePublish, capability.TerminalDiscover},
		AgencyMode: manifest.AgencyHuman,
		Revision:   1,
	}
	frame, err := m.Sign(priv)
	if err != nil {
		return nil, err
	}
	s := newReplica(tid)
	s.priv = priv
	s.ManifestFrame = frame
	return s, nil
}

// Replica joins an existing Space by id (e.g. from an invite).
func Replica(tid id.TerminalID) *Space { return newReplica(tid) }

func newReplica(tid id.TerminalID) *Space {
	s := &Space{
		ID:       tid,
		Log:      eventlog.New(tid, nil),
		State:    reducers.NewState(),
		Trust:    trust.NewEngine(),
		Registry: registry.New(),
	}
	return s
}

// absorb folds an applied event into materialized state and trust,
// decrypting sealed payloads when this replica holds the epoch key.
func (s *Space) absorb(a eventlog.Applied) {
	env := a.Env
	if env.LogicalClock > s.maxClock {
		s.maxClock = env.LogicalClock
	}
	if env.Schema == schemas.MembershipEpoch {
		s.absorbEpoch(env)
		return // key distribution is not user-visible state
	}
	if env.PayloadEncoding == signal.PayloadEncrypted {
		pt, ok := s.openForAbsorb(env)
		if !ok {
			s.Undecryptable++
			return
		}
		clone := *env
		clone.Payload = pt
		env = &clone
	}
	if env.Schema == schemas.ManifestUpdated {
		// A member published its interaction contract: install it. Errors
		// (stale revision, broken chain) are ignored here — the registry
		// keeps the last valid manifest and never downgrades.
		_, _ = s.Registry.Upsert(env.Payload)
		return
	}
	s.State.Apply(env, a.ID)
	if env.Schema == schemas.PresenceUpdate && env.SourceTerminal != nil {
		if p, err := schemas.DecodePresence(env.Payload); err == nil {
			s.Trust.UpdatePresence(claims.Presence{
				State:     p.State,
				EmittedAt: env.CreatedAt,
				ExpiresAt: p.ExpiresAt,
				Source:    *env.SourceTerminal,
			})
		}
	}
}

// Absorb ingests a frame that arrived from outside (sync, bundle).
func (s *Space) Absorb(frame []byte) (int, error) {
	applied, err := s.Log.Ingest(frame)
	if err != nil {
		return 0, err
	}
	for _, a := range applied {
		s.absorb(a)
	}
	return len(applied), nil
}

// AttachSyncApply wires a sync engine callback to this replica.
func (s *Space) AttachSyncApply(a eventlog.Applied) { s.absorb(a) }

// Participant is one terminal acting inside spaces, enforced by its own
// manifest.
type Participant struct {
	Principal *identity.Principal
	Device    *identity.Device

	TerminalID    id.TerminalID
	terminalPriv  ed25519.PrivateKey
	Manifest      *manifest.Manifest
	ManifestFrame []byte

	chains map[id.TerminalID]*chainState
	clk    uint64
}

type chainState struct {
	seq uint64
	tip id.EventID
}

// NewParticipant generates identity + terminal keys and signs the manifest
// template (Terminal/Controller fields are filled here).
func NewParticipant(template manifest.Manifest) (*Participant, error) {
	prin, err := identity.NewPrincipal(identity.NewRand())
	if err != nil {
		return nil, err
	}
	dev, err := identity.NewDevice(identity.NewRand())
	if err != nil {
		return nil, err
	}
	p, _, err := NewParticipantFrom(prin, dev, nil, template)
	return p, err
}

// allowedAuthorship maps declared agency to permissible authorship marks.
func (p *Participant) allowedAuthorship(a signal.Authorship) bool {
	switch p.Manifest.AgencyMode {
	case manifest.AgencyHuman:
		return a == signal.AuthorshipHuman || a == signal.AuthorshipHumanWithAI
	case manifest.AgencyDeterministic:
		return a == signal.AuthorshipDeterministicBot || a == signal.AuthorshipSensor
	case manifest.AgencyAIAgent:
		return a == signal.AuthorshipAIAgent
	default:
		return false
	}
}

// Emit publishes an event into a space, enforcing the participant's own
// manifest: publish capability, presence capability, authorship honesty.
func (p *Participant) Emit(s *Space, schema string, payload []byte,
	produced signal.Authorship, createdAt uint64) (eventlog.Applied, error) {

	caps := capability.NewSet(p.Manifest.Capabilities...)
	if !caps.Has(capability.SignalPublish) {
		return eventlog.Applied{}, fmt.Errorf("%w: signal.publish", ErrUndeclaredOperation)
	}
	if schema == schemas.PresenceUpdate && !caps.Has(capability.PresencePublish) {
		return eventlog.Applied{}, fmt.Errorf("%w: presence.publish", ErrUndeclaredOperation)
	}
	if !p.allowedAuthorship(produced) {
		return eventlog.Applied{}, fmt.Errorf("%w: agency %s cannot sign as %s",
			ErrAuthorshipForbidden, p.Manifest.AgencyMode, produced)
	}
	// Private spaces: seal the payload under the current epoch. No key —
	// no write; membership is cryptographic, not cosmetic.
	encoding := signal.PayloadCBOR
	wirePayload := payload
	priority := signal.PriorityMessage
	switch schema {
	case schemas.MembershipEpoch:
		priority = signal.PrioritySecurity
	case schemas.ManifestUpdated:
		priority = signal.PriorityManifest
	case schemas.PresenceUpdate:
		priority = signal.PriorityStatePatch
	case schemas.ObservationTemp:
		priority = signal.PriorityTelemetry
	}
	// Epoch distribution stays plaintext (its wraps are already encrypted
	// per device); everything else in a private space gets sealed.
	if s.Private && schema != schemas.MembershipEpoch {
		sealed, err := s.sealForEmit(schema, payload)
		if err != nil {
			return eventlog.Applied{}, err
		}
		wirePayload = sealed
		encoding = signal.PayloadEncrypted
	}
	c, ok := p.chains[s.ID]
	if !ok {
		c = &chainState{}
		p.chains[s.ID] = c
	}
	c.seq++
	p.clk++
	src := p.TerminalID
	env := &signal.Envelope{
		Terminal:        s.ID,
		Principal:       p.Principal.ID,
		Device:          p.Device.ID,
		Sequence:        c.seq,
		Schema:          schema,
		CreatedAt:       createdAt,
		LogicalClock:    p.clk,
		ProducedBy:      produced,
		SourceTerminal:  &src,
		PayloadEncoding: encoding,
		Payload:         wirePayload,
		Priority:        priority,
	}
	if c.seq > 1 {
		prev := c.tip
		env.Previous = &prev
	}
	frame, err := env.Sign(p.Device.SignKey())
	if err != nil {
		return eventlog.Applied{}, err
	}
	applied, err := s.Log.Ingest(frame)
	if err != nil {
		return eventlog.Applied{}, err
	}
	c.tip = applied[0].ID
	for _, a := range applied {
		s.absorb(a)
	}
	// Track the local lamport clock against everything in the space.
	if applied[0].Env.LogicalClock > p.clk {
		p.clk = applied[0].Env.LogicalClock
	}
	return applied[0], nil
}

// ObserveClock advances the participant's Lamport clock after reading the
// space (call after sync).
func (p *Participant) ObserveClock(clock uint64) {
	if clock > p.clk {
		p.clk = clock
	}
}

// CanReceive reports whether this participant's manifest lets it consume
// incoming signals at all (sink surface). A source-only sensor returns
// false — pointing sync at it is an undeclared operation.
func (p *Participant) CanReceive() bool {
	return capability.NewSet(p.Manifest.Capabilities...).Has(capability.SignalReceive)
}

// RequireReceive errors unless the participant may consume signals.
func (p *Participant) RequireReceive() error {
	if !p.CanReceive() {
		return fmt.Errorf("%w: signal.receive", ErrUndeclaredOperation)
	}
	return nil
}
