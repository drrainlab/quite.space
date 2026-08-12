// Gateway Terminal (TR-0, engineering plan §5.7): protocol bridging with
// visible losses. A gateway is a full participant — it holds keys, signs
// events, obeys the same manifest honesty as everyone else — and it is a
// boundary: what it publishes was OBSERVED in an external system, not
// written by it. The one authorship mark that says so is
// signal.AuthorshipImported, and this package is the first terminal allowed
// to use it.
//
// The doctrine every connector inherits (plan rev 4, R4): a gateway asserts
// provenance; external trust is not transferred. DKIM may have passed at
// the mail server — inside the space that is still only "the gateway says
// so". External identity never becomes Quiet identity.
package gateway

import (
	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/capability"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
)

// Template is the gateway's manifest: duplex (it imports and it carries
// replies out), capabilities exactly {publish, receive}, and AgencyGateway
// so allowedAuthorship maps it to AuthorshipImported and nothing else. No
// presence: a mailbox bridge is not "around" in any sense a person means.
func Template(label string) manifest.Manifest {
	return manifest.Manifest{
		Kind:           manifest.KindGateway,
		DeclaredLabels: []string{label},
		IOMode:         manifest.IODuplex,
		Capabilities:   []string{capability.SignalPublish, capability.SignalReceive},
		Publishes:      []string{schemas.MessageText},
		AgencyMode:     manifest.AgencyGateway,
	}
}

// New creates a standalone gateway participant (tests, fixtures). A real
// deployment builds it from stored seeds with the operator's principal as
// controller, the way the node builds its agent.
func New(label string) (*terminals.Participant, error) {
	return terminals.NewParticipant(Template(label))
}

// Import publishes one observed external message into a space, marked
// imported and carrying its foreign provenance as key 7. replyTo, when the
// external thread resolved to an already-imported event, makes it an
// ordinary reply edge — no new addressing exists for the external world.
func Import(p *terminals.Participant, s *terminals.Space, text string,
	origin *schemas.ExternalOrigin, replyTo *id.EventID,
	at uint64) (eventlog.Applied, error) {

	payload, err := (&schemas.TextMessage{
		Text:     text,
		ReplyTo:  replyTo,
		External: origin,
	}).Encode()
	if err != nil {
		return eventlog.Applied{}, err
	}
	return p.Emit(s, schemas.MessageText, payload, signal.AuthorshipImported, at)
}
