// PA-Assets: the projection keeps what a reader needs to FETCH the media
// of current publications. The carrier frame is the only thing in the
// world holding an asset's manifest id and key — age-pruning it left a
// post whose cover no peer could ever serve.
package terminals_test

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/projection"
	"github.com/drrainlab/quiet_places/protocol/publication"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
)

// emitCarrier posts a block.attached.v1 with a full AssetRef at a chosen
// time, and returns the ref.
func emitCarrier(t *testing.T, owner *terminals.Participant, s *terminals.Space,
	at uint64) *schemas.AssetRef {
	t.Helper()
	ref := &schemas.AssetRef{
		Role: "original", MediaType: "image/png", Size: 4096,
		ChunkSize: 4096,
	}
	if _, err := rand.Read(ref.AssetID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(ref.Key[:]); err != nil {
		t.Fatal(err)
	}
	var chunk id.Hash
	if _, err := rand.Read(chunk[:]); err != nil {
		t.Fatal(err)
	}
	ref.InlineChunks = []id.Hash{chunk}
	if _, err := rand.Read(ref.PlaintextDigest[:]); err != nil {
		t.Fatal(err)
	}
	payload, err := (&schemas.AttachedBlock{Original: ref}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Emit(s, schemas.BlockAttached, payload,
		signal.AuthorshipHuman, at); err != nil {
		t.Fatal(err)
	}
	return ref
}

// publishDocWithCover publishes a publication whose cover is the ref.
func publishDocWithCover(t *testing.T, owner *terminals.Participant,
	s *terminals.Space, ref *schemas.AssetRef, at uint64) {
	t.Helper()
	doc := &publication.Document{
		Kind: "article", Title: "иллюстрированное", Visibility: "space",
		Cover: ref.PublicIDHex(),
		Blocks: []publication.Block{{ID: "b1", Type: "text",
			RawProps: publication.EncodeTextProps(publication.TextProps{Text: "текст"})}},
	}
	if _, err := rand.Read(doc.DocumentID[:]); err != nil {
		t.Fatal(err)
	}
	payload := (&publication.RevisionPayload{
		Fallback: doc.Title, Document: doc.Encode(),
	}).Encode()
	if _, err := owner.Emit(s, publication.SchemaPublished, payload,
		signal.AuthorshipHuman, at); err != nil {
		t.Fatal(err)
	}
}

// The headline, red before the fix: a carrier older than MaxAge whose
// asset a CURRENT publication references survives the age pruning — and a
// fresh reader's projection still carries a fetchable ref.
func TestOldLiveCarrierSurvivesAgePruning(t *testing.T) {
	s, owner := buildPublicSpace(t, 3)
	now := uint64(time.Now().Unix())
	old := now - 40*24*3600 // well past the 30-day MaxAge
	ref := emitCarrier(t, owner, s, old)
	publishDocWithCover(t, owner, s, ref, now)

	wire, _, err := s.BuildPublicProjection(1, owner.Device.ID, now,
		terminals.DefaultProjectionLimits())
	if err != nil {
		t.Fatal(err)
	}
	env, err := projection.Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !carriesRef(env, ref) {
		t.Fatal("the live carrier was age-pruned — the cover is unfetchable by construction")
	}

	// And a carrier NOBODY references still ages out — the exemption is
	// for live media, not for every block frame forever.
	orphan := emitCarrier(t, owner, s, old)
	wire2, _, err := s.BuildPublicProjection(2, owner.Device.ID, now,
		terminals.DefaultProjectionLimits())
	if err != nil {
		t.Fatal(err)
	}
	env2, err := projection.Decode(wire2)
	if err != nil {
		t.Fatal(err)
	}
	if carriesRef(env2, orphan) {
		t.Fatal("an orphan carrier was retained — the exemption leaked past live media")
	}
	if !carriesRef(env2, ref) {
		t.Fatal("the live carrier vanished on rebuild")
	}
}

// Degrade, never brick: under byte pressure the builder drops oldest live
// carriers rather than refusing to publish.
func TestBudgetDropsOldestLiveCarriersBeforeRefusing(t *testing.T) {
	s, owner := buildPublicSpace(t, 2)
	now := uint64(time.Now().Unix())
	// Many live carriers, each referenced by a publication.
	var refs []*schemas.AssetRef
	for i := 0; i < 20; i++ {
		ref := emitCarrier(t, owner, s, now-uint64(3600*(20-i)))
		publishDocWithCover(t, owner, s, ref, now-uint64(1800*(20-i)))
		refs = append(refs, ref)
	}
	lim := terminals.DefaultProjectionLimits()
	// A budget that cannot possibly hold everything: structural
	// publication frames plus a few carriers only.
	lim.MaxBytes = 10 << 10
	wire, _, err := s.BuildPublicProjection(1, owner.Device.ID, now, lim)
	if err != nil {
		t.Fatalf("a squeezed budget refused to publish instead of degrading: %v", err)
	}
	env, err := projection.Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !env.Truncated {
		t.Fatal("nothing was pruned under a squeezed budget")
	}
	// The NEWEST live carrier outlives the oldest one under pressure.
	newest, oldest := refs[len(refs)-1], refs[0]
	if carriesRef(env, oldest) && !carriesRef(env, newest) {
		t.Fatal("budget pressure ate the newest live carrier before the oldest")
	}
}

// The one walk cannot diverge from the validator: what LiveAssetIDs
// enumerates is exactly what Validate checks, video-link poster included.
func TestLiveAssetIDsMatchesWhatValidateChecks(t *testing.T) {
	mk := func(n byte) string {
		var b [16]byte
		b[0] = n
		return hex.EncodeToString(b[:])
	}
	doc := &publication.Document{
		Kind: "article", Title: "t", Visibility: "space",
		Cover: mk(1),
		Atmosphere: &publication.Atmosphere{
			Visual: publication.Visual{Scene: "drift@1"},
			Audio:  &publication.Audio{Asset: mk(2), Mode: "loop"},
			Fall:   publication.Fallback{Text: "calm", Poster: mk(3)},
		},
		Blocks: []publication.Block{
			{ID: "b1", Type: "image",
				RawProps: publication.EncodeAssetProps(publication.AssetProps{Asset: mk(4), Text: "alt"})},
			{ID: "b2", Type: "video-link",
				RawProps: publication.EncodeAssetProps(publication.AssetProps{
					Asset: "https://example.org/v", Caption: mk(5)})},
			{ID: "b3", Type: "gallery",
				RawProps: publication.EncodeListProps(publication.ListProps{Items: []string{mk(6), mk(7)}})},
		},
	}
	doc.DocumentID = [16]byte{9}

	enumerated := doc.LiveAssetIDs()
	checked := map[string]bool{}
	_ = publication.Validate(doc, func(hexID string) bool {
		checked[hexID] = true
		return true
	})
	if len(enumerated) != 7 {
		t.Fatalf("the walk found %d assets, want 7: %v", len(enumerated), enumerated)
	}
	for aid := range enumerated {
		if !checked[aid] {
			t.Fatalf("enumerated but never validated: %s (%s)", aid[:8], enumerated[aid])
		}
	}
	for aid := range checked {
		if _, ok := enumerated[aid]; !ok {
			t.Fatalf("validated but never enumerated: %s", aid[:8])
		}
	}
	// The poster hides in Caption — the site everybody forgets.
	if enumerated[mk(5)] != "poster" {
		t.Fatalf("the video-link poster was missed: %v", enumerated[mk(5)])
	}
}

func carriesRef(env *projection.Envelope, want *schemas.AssetRef) bool {
	for _, frame := range env.Frames {
		fe, err := signal.Decode(frame)
		if err != nil || !schemas.IsBlockSchema(fe.Schema) {
			continue
		}
		for _, ref := range schemas.ExtractAssetRefs(fe.Schema, fe.Payload) {
			if ref != nil && ref.AssetID == want.AssetID {
				return true
			}
		}
	}
	return false
}
