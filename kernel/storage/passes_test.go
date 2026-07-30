package storage

import (
	"bytes"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// The saga journal must round-trip exactly: an outcome that changes shape
// across a restart is an outcome the owner can no longer honour.
func TestKeystoreCarriesTheJoinSaga(t *testing.T) {
	k := &Keystore{
		Passes: []PassRecord{{
			Space:    id.TerminalID{7},
			Frame:    []byte("signed-pass-frame"),
			Relay:    "relay.example:7411",
			Used:     2,
			Approval: "host",
			Handled: []HandledRequest{
				{Request: [32]byte{1}, Outcome: OutcomeGranted, At: 100},
				{Request: [32]byte{2}, Outcome: OutcomeDeclinedByHost, At: 200},
			},
			Entries: []EntryRecord{{
				Request: [32]byte{3}, Device: id.DeviceID{9},
				Name: "bob", AskedAt: 300, State: EntryPending,
			}},
		}},
		Joins: []JoinRecord{{
			Space: id.TerminalID{7}, PassFrame: []byte("frame"),
			Secret: [32]byte{4}, Request: [32]byte{5},
			Relay: "relay.example:7411", State: "waiting_for_host",
			StartedAt: 300, CollectUntil: 90000,
		}},
	}
	got, err := decodeKeystore(k.encode())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Passes) != 1 || len(got.Joins) != 1 {
		t.Fatalf("records lost: %d passes, %d joins", len(got.Passes), len(got.Joins))
	}
	p := got.Passes[0]
	if p.Used != 2 || p.Approval != "host" || p.Relay != "relay.example:7411" {
		t.Fatalf("pass record changed: %+v", p)
	}
	if !bytes.Equal(p.Frame, []byte("signed-pass-frame")) {
		t.Fatal("the signed frame did not survive")
	}
	if len(p.Handled) != 2 || p.Handled[1].Outcome != OutcomeDeclinedByHost {
		t.Fatalf("the idempotency journal changed: %+v", p.Handled)
	}
	if len(p.Entries) != 1 || p.Entries[0].Name != "bob" ||
		p.Entries[0].State != EntryPending {
		t.Fatalf("the door queue changed: %+v", p.Entries)
	}
	j := got.Joins[0]
	if j.State != "waiting_for_host" || j.CollectUntil != 90000 {
		t.Fatalf("guest saga changed: %+v", j)
	}
	if j.Secret != [32]byte{4} {
		t.Fatal("the guest lost the secret it needs to re-send")
	}
}

// A keystore written before this wave has no saga at all and must open
// exactly as before — an upgrade is not a reason to lose someone's spaces.
func TestAKeystoreWithoutASagaStillOpens(t *testing.T) {
	k := &Keystore{DisplayName: "alice"}
	got, err := decodeKeystore(k.encode())
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "alice" {
		t.Fatalf("display name lost: %q", got.DisplayName)
	}
	if len(got.Passes) != 0 || len(got.Joins) != 0 {
		t.Fatal("records appeared from nowhere")
	}
}
