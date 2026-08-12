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

	// ReadOnly marks a public-space reader replica (PA-0): it may absorb
	// and materialize, but local emits are refused at the low-level gate
	// (I3) until the node flips it on join/curator activation.
	ReadOnly bool

	// PolicyStats counts frames refused by the space's publish policy —
	// the honest counterpart of Undecryptable (I2: refused frames never
	// enter the canonical log; this is the visible trace they existed).
	PolicyStats PolicyStats

	// OnBlock receives every DECRYPTED block.* event (asset indexing and
	// other post-reduction hooks live above the terminals layer).
	OnBlock func(env *signal.Envelope, eid id.EventID)

	// OnAbsorb fires for EVERY absorbed event — local emits and synced
	// frames alike (the single funnel). The node runtime uses it for the
	// delivery ladder (ADR-015 §5); it must not re-enter the space.
	OnAbsorb func(a eventlog.Applied)

	// ProjectionFrames counts events applied via public projections (the
	// reader replica's honest "how much I hold" — its canonical log is
	// empty by design).
	ProjectionFrames int

	// priv is held only by the creating node (the controller's replica).
	priv          ed25519.PrivateKey
	priv2         *privateState
	ManifestFrame []byte
	maxClock      uint64
	// projSeen dedups projection-applied events on reader replicas.
	projSeen map[id.EventID]bool
}

// NewSpace creates a Space Terminal with the default (campfire) character.
func NewSpace(title string, controller id.PrincipalID) (*Space, error) {
	return NewSpaceWithCharacter(title, controller, DefaultCharacter("campfire"))
}

// NewSpaceWithCharacter creates a Space Terminal whose character is part of
// its signed manifest: every replica reads the same declared feel.
func NewSpaceWithCharacter(title string, controller id.PrincipalID, c Character) (*Space, error) {
	return NewSpaceWithPolicy(title, controller, c, SpacePolicy{})
}

// NewSpaceWithPolicy additionally signs an access policy into the manifest
// (PA-0). The zero policy emits no labels — private manifests stay
// byte-identical to pre-PA-0 ones.
func NewSpaceWithPolicy(title string, controller id.PrincipalID, c Character, pol SpacePolicy) (*Space, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if err := pol.Validate(); err != nil {
		return nil, err
	}
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
		DeclaredLabels: append(c.Labels(title), pol.Labels()...),
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
	s.refreshPolicy()
	return s, nil
}

// Replica joins an existing Space by id (e.g. from an invite).
func Replica(tid id.TerminalID) *Space { return newReplica(tid) }

// Character parses the space's declared character from its manifest frame.
// Falls back to the default when the manifest is absent (bare replicas).
func (s *Space) Character() (string, Character) {
	if len(s.ManifestFrame) == 0 {
		return "", DefaultCharacter("campfire")
	}
	m, err := manifest.Decode(s.ManifestFrame)
	if err != nil {
		return "", DefaultCharacter("campfire")
	}
	return ParseCharacter(m.DeclaredLabels)
}

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
	// The controller is static space metadata from the manifest (never
	// event-derived); the reducer needs it only to authorize keep moderation.
	if s.State.Controller == nil && len(s.ManifestFrame) > 0 {
		if m, err := manifest.Decode(s.ManifestFrame); err == nil {
			c := m.Controller
			s.State.Controller = &c
		}
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
	if s.OnBlock != nil && schemas.IsBlockSchema(env.Schema) {
		s.OnBlock(env, a.ID)
	}
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
	if s.OnAbsorb != nil {
		s.OnAbsorb(a)
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
	case manifest.AgencyGateway:
		// The one honest mark for words the signer carried but did not
		// write (TR-0). A gateway can say NOTHING else: not human, not
		// bot — either would pass observed content off as its own.
		return a == signal.AuthorshipImported
	default:
		return false
	}
}

// Emit publishes an event into a space, enforcing the participant's own
// manifest: publish capability, presence capability, authorship honesty.
func (p *Participant) Emit(s *Space, schema string, payload []byte,
	produced signal.Authorship, createdAt uint64) (eventlog.Applied, error) {

	// The single low-level write gate (PA-0 I3 + PA-1 freeze): reader
	// replicas never emit; frozen spaces refuse EVERYONE — the owner
	// included; curated spaces refuse non-writers. All checked BEFORE the
	// chain sequence advances, so a refusal never desyncs the author
	// chain. The log admission gate enforces the same policy one layer
	// lower for frames arriving from outside.
	if s.ReadOnly {
		return eventlog.Applied{}, ErrReadOnlyReplica
	}
	if pol := s.Policy(); pol.IsPublic() {
		if pol.Frozen {
			return eventlog.Applied{}, ErrSpaceFrozen
		}
		if !pol.AllowsWriter(p.Principal.ID, p.Device.ID) {
			return eventlog.Applied{}, ErrNotAuthorized
		}
	}
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
	var headerExpiry, maxForwards uint64
	switch schema {
	case schemas.MembershipEpoch:
		priority = signal.PrioritySecurity
	case schemas.ManifestUpdated:
		priority = signal.PriorityManifest
	case schemas.PresenceUpdate:
		priority = signal.PriorityStatePatch
		// Presence is ephemeral by nature: mirror the payload TTL into the
		// custody-expiry header and declare NoCustody (ADR-015) — bridges
		// and relays never store-and-forward stale presence.
		if pp, err := schemas.DecodePresence(payload); err == nil {
			headerExpiry = pp.ExpiresAt
			maxForwards = 1
		}
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
	// LAMPORT'S RULE, DERIVED RATHER THAN REMEMBERED: the clock of a new
	// event is one past the highest this replica has absorbed, not one past
	// the last thing we happened to say.
	//
	// It used to be ObserveClock's job, and ObserveClock is called on
	// ResumeChain — a restart — and nowhere on the live sync path. So a
	// running node learned nothing from what it received: a phone that had
	// absorbed clocks 36, 37, 38 was still stamping its own messages 11 and
	// 12, every one of them "before" everything the other side had already
	// said. Nothing is lost by that, but ordering is decided by
	// (created_at, logical_clock, id) and the tie-break stops meaning
	// anything — two devices racing an edit resolve by a dead number.
	//
	// Taking the maximum HERE, at the one place a clock is spent, makes the
	// invariant impossible to break by forgetting a call site.
	if s.maxClock > p.clk {
		p.clk = s.maxClock
	}
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
		ExpiresAt:       headerExpiry,
		MaxForwards:     maxForwards,
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
