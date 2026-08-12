// TR-0b — ADR-019 gate 5, the key-7 twin: provenance is not inherited, and
// it is held BY CONSTRUCTION — nothing in the share path reads
// e.Content.Text.External. A re-share of an imported email into another
// space is a quotation of words somebody saw in THIS space; carrying the
// email's foreign origin along would smuggle a correlation (and an implied
// endorsement) the destination never consented to.
package node

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func TestExternalOriginIsNotInheritedOnReshare(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	src, err := rt.CreateSpace("mailbox")
	if err != nil {
		t.Fatal(err)
	}
	dst, err := rt.CreateSpace("elsewhere")
	if err != nil {
		t.Fatal(err)
	}

	// An entry with foreign provenance sits in the source space. (Emitted
	// directly so the test does not need a full gateway membership; the
	// share path below cannot tell and must not care.)
	var eid id.EventID
	if err := rt.withSpace(src, func(st *spaceState) error {
		payload, err := (&schemas.TextMessage{
			Text: "приветствие из внешнего мира",
			External: &schemas.ExternalOrigin{
				ConnectorKind: "email",
				Address:       "alice@example.org",
				ExternalRef:   "<msg-42@example.org>",
			},
		}).Encode()
		if err != nil {
			return err
		}
		a, err := rt.Self.Emit(st.space, schemas.MessageText, payload,
			signal.AuthorshipHuman, 100)
		eid = a.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := rt.Share(src, eid, []id.TerminalID{dst}, ShareOptions{NameAuthor: true}); err != nil {
		t.Fatal(err)
	}

	quoted := 0
	_ = rt.withSpace(dst, func(st *spaceState) error {
		for _, e := range st.space.State.Entries() {
			if e.Content.Text == nil || e.Content.Text.Origin == nil {
				continue
			}
			quoted++
			if e.Content.Text.External != nil {
				t.Fatalf("a re-share carried the email's foreign origin: %+v",
					e.Content.Text.External)
			}
		}
		return nil
	})
	if quoted == 0 {
		t.Fatal("the quotation never landed — the invariant was not exercised")
	}
}
