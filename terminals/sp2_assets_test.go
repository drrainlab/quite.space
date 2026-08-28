// SP-2 retention law (ADR-030): a live structural reference — an object's
// non-detached asset edge — keeps the asset's carrier in the projection
// past MaxAge, exactly the way a publication's cover does. A detached edge
// stops pinning; an annotation never pins.
package terminals_test

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/objects"
	"github.com/drrainlab/quiet_places/protocol/projection"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
)

func emitObjectWithEdge(t *testing.T, owner *terminals.Participant,
	s *terminals.Space, assetHex string, detached bool, at uint64) {
	t.Helper()
	rec := &objects.Record{Kind: "track", Name: "Winter Song"}
	if _, err := rand.Read(rec.ObjectID[:]); err != nil {
		t.Fatal(err)
	}
	enc, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	payload := (&objects.RevisionPayload{Fallback: rec.Name, Record: enc}).Encode()
	if _, err := owner.Emit(s, objects.SchemaCreated, payload,
		signal.AuthorshipHuman, at); err != nil {
		t.Fatal(err)
	}
	edge := &objects.AttachPayload{
		Fallback: "mix", ObjectID: rec.ObjectID, Asset: assetHex,
		Role: "mix", Detached: detached,
	}
	ep, err := edge.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Emit(s, objects.SchemaAttached, ep,
		signal.AuthorshipHuman, at); err != nil {
		t.Fatal(err)
	}
}

func TestObjectEdgePinsCarrierPastMaxAge(t *testing.T) {
	s, owner := buildPublicSpace(t, 3)
	now := uint64(time.Now().Unix())
	old := now - 40*24*3600 // well past the 30-day MaxAge

	// A live edge pins its old carrier.
	pinned := emitCarrier(t, owner, s, old)
	emitObjectWithEdge(t, owner, s, pinned.PublicIDHex(), false, now)
	// A detached-only edge does not pin.
	loose := emitCarrier(t, owner, s, old)
	emitObjectWithEdge(t, owner, s, loose.PublicIDHex(), true, now)
	// An annotation alone does not pin either.
	noted := emitCarrier(t, owner, s, old)
	ann := &schemas.AssetAnnotation{Text: "вокал суховат", Asset: noted.PublicIDHex()}
	if _, err := rand.Read(ann.AnnotationID[:]); err != nil {
		t.Fatal(err)
	}
	ann.SetPosition(102_000)
	ap, err := ann.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Emit(s, schemas.AssetAnnotated, ap,
		signal.AuthorshipHuman, now); err != nil {
		t.Fatal(err)
	}

	wire, _, err := s.BuildPublicProjection(1, owner.Device.ID, now,
		terminals.DefaultProjectionLimits())
	if err != nil {
		t.Fatal(err)
	}
	env, err := projection.Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !carriesRef(env, pinned) {
		t.Fatal("a live edge's carrier was age-pruned — the mix is unfetchable by construction")
	}
	if carriesRef(env, loose) {
		t.Fatal("a detached edge still pinned its carrier")
	}
	if carriesRef(env, noted) {
		t.Fatal("an annotation pinned a carrier — commentary must never do that")
	}
}
