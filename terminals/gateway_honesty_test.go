// TR-0b — the gateway's authorship honesty, red first.
//
// AuthorshipImported has been on the wire since M0 and reachable by nobody:
// no AgencyMode mapped to it, so a bridge would have had to sign imported
// email as deterministic_bot — a false statement about provenance in a
// codebase whose whole posture (ADR-008) is that such statements are true.
// The gateway terminal is the first honest speaker of "imported".
package terminals_test

import (
	"errors"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/gateway"
	"github.com/drrainlab/quiet_places/terminals/human"
)

func TestGatewayImportsHonestly(t *testing.T) {
	s := newSpace(t)
	gw, err := gateway.New("mail bridge")
	if err != nil {
		t.Fatal(err)
	}
	a, err := gateway.Import(gw, s, "hello from outside",
		&schemas.ExternalOrigin{
			ConnectorKind: "email",
			Address:       "alice@example.org",
			ExternalRef:   "<msg-1@example.org>",
		}, nil, 100)
	if err != nil {
		t.Fatalf("the gateway could not speak its one honest mark: %v", err)
	}
	if a.Env.ProducedBy != signal.AuthorshipImported {
		t.Fatalf("imported mail marked %v", a.Env.ProducedBy)
	}
	if a.Env.HumanApproved {
		t.Fatal("an import claimed human approval")
	}
}

// A gateway must not pass observed words off as its own writing — in either
// of the two directions somebody would be tempted to.
func TestGatewayCannotSignAsHumanOrBot(t *testing.T) {
	s := newSpace(t)
	gw, err := gateway.New("mail bridge")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := (&schemas.TextMessage{Text: "definitely a person"}).Encode()
	if _, err := gw.Emit(s, schemas.MessageText, payload, signal.AuthorshipHuman, 1); !errors.Is(err, terminals.ErrAuthorshipForbidden) {
		t.Fatalf("gateway signed as human: %v", err)
	}
	if _, err := gw.Emit(s, schemas.MessageText, payload, signal.AuthorshipDeterministicBot, 2); !errors.Is(err, terminals.ErrAuthorshipForbidden) {
		t.Fatalf("gateway signed as bot: %v", err)
	}
}

// And the mark is not available to anyone else: a human terminal claiming
// "imported" would be laundering its own words as somebody's email.
func TestHumanCannotSignImported(t *testing.T) {
	s := newSpace(t)
	alice, err := human.New("alice")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := (&schemas.TextMessage{Text: "totally an email"}).Encode()
	if _, err := alice.Emit(s, schemas.MessageText, payload, signal.AuthorshipImported, 1); !errors.Is(err, terminals.ErrAuthorshipForbidden) {
		t.Fatalf("a human signed imported: %v", err)
	}
}
