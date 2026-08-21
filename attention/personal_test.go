package attention

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// Personal ≠ Hard, pinned. Personal means somebody called YOU — a signed
// mention or a reply to your words; a soft signal (a question in the room)
// is real but impersonal. AuthorKind rides the context through untouched:
// it bends a chime's timbre client-side and must never affect the verdict.
func TestPersonalIsAboutBeingCalledNotAboutRank(t *testing.T) {
	e := NewEngine(Policy{Mode: ModeMinimal})
	var me, other id.PrincipalID
	me[0], other[0] = 1, 2
	viewer := Viewer{Principal: me, AuthoredByMe: func(id.EventID) bool { return false }}

	var ev1, ev2 id.EventID
	ev1[0], ev2[0] = 0xA1, 0xA2

	mention := Candidate{EventID: ev1, Author: other, Kind: "text",
		Text: "ping", Mentions: []id.PrincipalID{me}, ReceivedAt: 10}
	sig, ok := e.Judge(mention, Context{Viewer: viewer, AuthorKind: "sensor"}, 10)
	if !ok {
		t.Fatal("a signed mention did not become a signal")
	}
	if !sig.Personal {
		t.Fatal("a signed mention is the definition of personal")
	}
	if sig.AuthorKind != "sensor" {
		t.Fatalf("author kind lost: %q", sig.AuthorKind)
	}

	question := Candidate{EventID: ev2, Author: other, Kind: "text",
		Text: "does anyone know when the relay restarts?", ReceivedAt: 11}
	sig2, ok := e.Judge(question, Context{Viewer: viewer, AuthorKind: "human"}, 11)
	if !ok {
		t.Fatal("a direct question did not become a signal")
	}
	if sig2.Personal {
		t.Fatal("a question to the room is not personal — nobody called the viewer")
	}
}
