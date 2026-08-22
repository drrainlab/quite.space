// Member cards (M1.3/M1.4): each participant publishes its signed manifest
// into the space log, and every replica projects the member list from its
// Registry plus the Trust engine. The card never shows more than is proven:
// kind and capabilities are the manifest's own claims (origin
// self_declared), AI participation is a hard protocol fact of the manifest,
// presence is TTL-honest, and an undeclared model stays "not specified".
package terminals

import (
	"errors"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/trust"
	"github.com/drrainlab/quiet_places/protocol/capability"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// PublishManifest emits the participant's manifest frame into the space
// (schema terminal.manifest.updated.v1, manifest lane). Idempotent per
// revision: if the registry already holds this revision, nothing is sent.
func (p *Participant) PublishManifest(s *Space) (eventlog.Applied, bool, error) {
	if t, ok := s.Registry.Get(p.TerminalID); ok && t.Manifest.Revision >= p.Manifest.Revision {
		return eventlog.Applied{}, false, nil
	}
	a, err := p.Emit(s, schemas.ManifestUpdated, p.ManifestFrame, p.DefaultAuthorship(), 0)
	if err != nil {
		return eventlog.Applied{}, false, err
	}
	return a, true, nil
}

// PublishManifestFrameOnBehalf carries ANOTHER terminal's signed manifest
// into the space inside this participant's envelope (QI-M, ADR-026). Two
// levels of authorship, named so nobody "fixes" them apart later:
//
//	content author      = the instrument (its terminal key signed the manifest)
//	transport publisher = this participant (its device key signed the envelope)
//
// The registry verifies the manifest by manifest.Terminal and never asks
// who carried it — envelope.Device == manifest device is NOT a rule and
// must never become one: an instrument holds no conversation key, so its
// manifest can only reach a private space in a member's envelope.
// Idempotent per revision, like PublishManifest.
func (p *Participant) PublishManifestFrameOnBehalf(s *Space, frame []byte) (eventlog.Applied, bool, error) {
	m, err := manifest.Decode(frame)
	if err != nil {
		return eventlog.Applied{}, false, err
	}
	if err := manifest.VerifyFrame(frame, m); err != nil {
		return eventlog.Applied{}, false, err
	}
	if t, ok := s.Registry.Get(m.Terminal); ok && t.Manifest.Revision >= m.Revision {
		return eventlog.Applied{}, false, nil
	}
	a, err := p.Emit(s, schemas.ManifestUpdated, frame, p.DefaultAuthorship(), 0)
	if err != nil {
		return eventlog.Applied{}, false, err
	}
	return a, true, nil
}

// MemberCard is the projection one replica can honestly render about a
// member terminal (plan §M1.3/M1.4).
type MemberCard struct {
	Terminal id.TerminalID
	// Principal controls this terminal; Name is its self-declared display
	// label (the human-readable identity, a claim — never verified).
	Principal id.PrincipalID
	Name      string
	// Everything below the line is the member's own signed declaration —
	// a claim, not a verified fact (invariant §2.3).
	Kind           string
	Agency         string
	AIPresent      bool
	Autonomy       manifest.Autonomy
	ModelDeclared  bool // v0 manifests never declare a model
	IOMode         string
	Capabilities   []string
	DeclaredLabels []string
	// SysLabels are computed locally from the manifest (protocol origin).
	SysLabels []string
	// Presence is TTL-honest: Current=false + AgeSeconds for stale.
	Presence trust.ProjectedPresence
	// CanReceiveCommands is the local authorization answer, not a promise.
	CanReceiveCommands bool
}

// AutonomyLabel renders A0–A4 for UIs.
func AutonomyLabel(a manifest.Autonomy) string {
	switch a {
	case manifest.AutonomyNone:
		return "A0 none"
	case manifest.AutonomyAssistive:
		return "A1 assistive"
	case manifest.AutonomyDelegated:
		return "A2 delegated"
	case manifest.AutonomyAutonomous:
		return "A3 autonomous"
	case manifest.AutonomyPhysicalControl:
		return "A4 physical (not accepted in v0)"
	default:
		return "unknown"
	}
}

// MemberCards projects all known members at the given time.
func (s *Space) MemberCards(nowUnix uint64) []MemberCard {
	terminals := s.Registry.All()
	out := make([]MemberCard, 0, len(terminals))
	for _, t := range terminals {
		m := t.Manifest
		name := ""
		if len(m.DeclaredLabels) > 0 {
			name = m.DeclaredLabels[0]
		}
		card := MemberCard{
			Terminal:       t.ID,
			Principal:      m.Controller,
			Name:           name,
			Kind:           m.Kind.String(),
			Agency:         m.AgencyMode.String(),
			AIPresent:      m.AIPresent,
			Autonomy:       m.Autonomy,
			IOMode:         m.IOMode.String(),
			Capabilities:   append([]string(nil), m.Capabilities...),
			DeclaredLabels: append([]string(nil), m.DeclaredLabels...),
			SysLabels:      t.SysLabels(),
			Presence:       s.Trust.Presence(t.ID, nowUnix),
		}
		card.CanReceiveCommands = len(m.Commands) > 0 &&
			t.Capabilities().Has(capability.CommandReceive)
		out = append(out, card)
	}
	return out
}

// ErrNotAMember helps callers distinguish "no manifest yet" cases.
var ErrNotAMember = errors.New("terminals: terminal has not published a manifest here")
