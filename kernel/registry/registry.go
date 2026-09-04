// Package registry is the Terminal Registry (M0.3): known terminals, their
// manifest revision chains, and the four label planes (plan §7). It is also
// the enforcement point for "capabilities before assumptions" (invariant
// §2.2): any attempt to use a terminal beyond its manifest fails here.
package registry

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/drrainlab/quiet_places/protocol/capability"
	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
)

// Terminal is the registry's view of one known terminal.
type Terminal struct {
	ID            id.TerminalID
	Manifest      *manifest.Manifest
	ManifestFrame []byte // received bytes, verbatim (ADR-003)
	ManifestHash  id.Hash
	// Device is the device that signed the envelope this manifest arrived
	// in — set by the space on apply, not by the registry (the manifest
	// payload itself does not name it). For an instrument it is the key
	// the instrument plane addresses, which is what "still attached" means.
	Device id.DeviceID

	// Observed labels are local claims about behavior (plan §7.3): never
	// global truth, always origin peer_observed.
	Observed map[string]claims.Claim
	// Local labels never leave this device without explicit export (§7.5).
	Local map[string]struct{}
}

// SysLabels computes protocol labels from the manifest (plan §7.1). They are
// derived by the client — a terminal cannot assign sys.* to itself.
func (t *Terminal) SysLabels() []string {
	m := t.Manifest
	labels := []string{
		"sys.kind." + m.Kind.String(),
		"sys.io." + m.IOMode.String(),
	}
	switch m.AgencyMode {
	case manifest.AgencyHuman:
		labels = append(labels, "sys.agency.human")
	case manifest.AgencyDeterministic:
		labels = append(labels, "sys.agency.deterministic")
	case manifest.AgencyAIAgent:
		labels = append(labels, "sys.agency.ai_agent")
	}
	if m.AIPresent {
		labels = append(labels, "sys.ai.present")
	}
	if m.RetentionSecs > 0 && m.RetentionSecs <= 86400 {
		labels = append(labels, "sys.storage.ephemeral")
	}
	if !m.EncryptedOutput {
		labels = append(labels, "sys.security.plaintext_output")
	}
	sort.Strings(labels)
	return labels
}

// Capabilities returns the manifest's atomic capability set.
func (t *Terminal) Capabilities() capability.Set {
	return capability.NewSet(t.Manifest.Capabilities...)
}

// Registry tracks known terminals. Pure state, rebuildable from the event
// log (ADR-006); persistence is the caller's concern.
type Registry struct {
	terminals map[id.TerminalID]*Terminal
}

func New() *Registry {
	return &Registry{terminals: map[id.TerminalID]*Terminal{}}
}

// Upsert verifies a manifest frame and installs it, enforcing the revision
// chain: strictly increasing revisions, previous hash linking to the frame
// we hold. A conflicting revision (same number, different bytes) is rejected
// and surfaced — never silently resolved (invariant §2.7).
func (r *Registry) Upsert(frame []byte) (*Terminal, error) {
	m, err := manifest.Decode(frame)
	if err != nil {
		return nil, err
	}
	if err := manifest.VerifyFrame(frame, m); err != nil {
		return nil, err
	}
	h := manifest.Hash(frame)
	existing, known := r.terminals[m.Terminal]
	if known {
		switch {
		case m.Revision == existing.Manifest.Revision && h == existing.ManifestHash:
			return existing, nil // idempotent re-ingest
		case m.Revision <= existing.Manifest.Revision:
			return nil, fmt.Errorf("registry: stale or conflicting revision %d (have %d)",
				m.Revision, existing.Manifest.Revision)
		case m.Previous == nil || *m.Previous != existing.ManifestHash:
			return nil, errors.New("registry: manifest revision chain broken")
		}
	}
	// First sight of a terminal accepts its current revision as the
	// baseline: a member who renamed before joining arrives above revision 1,
	// and history we never saw cannot be verified anyway. The signature is
	// checked; subsequent revisions must chain from this baseline.
	t := &Terminal{
		ID:            m.Terminal,
		Manifest:      m,
		ManifestFrame: frame,
		ManifestHash:  h,
		Observed:      map[string]claims.Claim{},
		Local:         map[string]struct{}{},
	}
	if known {
		t.Observed = existing.Observed
		t.Local = existing.Local
		t.Observe("manifest_changed", "true", 0)
	}
	r.terminals[m.Terminal] = t
	return t, nil
}

// Get returns a known terminal.
func (r *Registry) Get(tid id.TerminalID) (*Terminal, bool) {
	t, ok := r.terminals[tid]
	return t, ok
}

// All lists known terminals, sorted by id for determinism.
func (r *Registry) All() []*Terminal {
	out := make([]*Terminal, 0, len(r.terminals))
	for _, t := range r.terminals {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		return string(out[i].ID[:]) < string(out[j].ID[:])
	})
	return out
}

// Observe records an observed label as a peer_observed claim (§7.3).
func (t *Terminal) Observe(key, value string, atUnix uint64) {
	t.Observed["observed."+key] = claims.Claim{
		Key:      "observed." + key,
		Value:    value,
		Origin:   claims.OriginPeerObserved,
		IssuedAt: atUnix,
	}
}

// SetLocalLabel attaches a local-only label (§7.5).
func (t *Terminal) SetLocalLabel(label string) {
	if !strings.HasPrefix(label, "local.") {
		label = "local." + label
	}
	t.Local[label] = struct{}{}
}

// CapabilityDiff reports capability changes between the current manifest and
// a candidate frame (M0.3: revision review before accepting).
func (r *Registry) CapabilityDiff(tid id.TerminalID, candidate []byte) (added, removed []string, err error) {
	t, ok := r.terminals[tid]
	if !ok {
		return nil, nil, errors.New("registry: unknown terminal")
	}
	m, err := manifest.Decode(candidate)
	if err != nil {
		return nil, nil, err
	}
	added, removed = capability.NewSet(m.Capabilities...).Diff(t.Capabilities())
	return added, removed, nil
}

// Errors for authorization decisions. All fail closed (invariant §2.7).
var (
	ErrUnknownTerminal  = errors.New("registry: unknown terminal — nothing is authorized")
	ErrNotCapable       = errors.New("registry: terminal does not declare this capability")
	ErrSchemaNotOffered = errors.New("registry: terminal does not accept this schema")
)

// AuthorizeCommand decides whether a command with the given schema may be
// addressed to the terminal. This is the M0.3 acceptance point: a
// source-only sensor structurally cannot be a command receiver.
func (r *Registry) AuthorizeCommand(target id.TerminalID, commandSchema string) error {
	t, ok := r.terminals[target]
	if !ok {
		return ErrUnknownTerminal
	}
	caps := t.Capabilities()
	if !caps.Has(capability.CommandReceive) {
		return ErrNotCapable
	}
	for _, s := range t.Manifest.Commands {
		if s == commandSchema {
			return nil
		}
	}
	return ErrSchemaNotOffered
}

// AuthorizeSend decides whether ordinary signals may be addressed to the
// terminal at all (sink surface).
func (r *Registry) AuthorizeSend(target id.TerminalID, schema string) error {
	t, ok := r.terminals[target]
	if !ok {
		return ErrUnknownTerminal
	}
	if !t.Capabilities().Has(capability.SignalReceive) {
		return ErrNotCapable
	}
	if len(t.Manifest.Accepts) == 0 {
		return nil // accepts any schema it can reduce
	}
	for _, s := range t.Manifest.Accepts {
		if s == schema {
			return nil
		}
	}
	return ErrSchemaNotOffered
}
