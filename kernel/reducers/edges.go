// Object→asset edges and media annotations (SP-2).
//
// An edge is ONE LWW REGISTER per (object, asset): the latest
// object.attached.v1 event for the pair IS the edge, whole. Registers are
// NEVER evicted — evicting an LWW register is the archive/restore ordering
// hole in a new costume (a late stale event for an evicted key would
// resurrect it as a fresh register and diverge replicas forever). The
// bound lives at the node as an authoring refusal, not here.
//
// Everything derived here is ROLE-BLIND. Role steers renderers; the
// kernel never branches on it — the same law Status obeys (ADR-029). The
// candidate register is "the preferred current asset for this object" —
// what we are listening to now — NOT "approved", NOT "final"; those words
// belong to future primitives and must not creep in here.
//
// Annotations are bounded immutable timelines per asset under the
// observation eviction law: survivors are exactly the newest
// maxObservationsPerTimeline notes by (CreatedAt, Clock, EventID) in any
// arrival order. Dedupe is by EventID ONLY — an AnnotationID is a handle,
// and first-sight-wins by handle would be arrival-order dependent once
// eviction starts.
package reducers

import (
	"sort"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/objects"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// assetEdge is one converged register. Every field comes from the WINNING
// event alone — no first-accepted freezes near the digest.
type assetEdge struct {
	role       string
	label      string
	ordinal    uint64
	detached   bool
	supersedes string
	author     id.PrincipalID
	eid        id.EventID
	clock      uint64
	createdAt  uint64
}

// candReg is the per-object candidate register. A clear OCCUPIES the
// register with an empty asset — otherwise an earlier set would win a race
// against a later clear.
type candReg struct {
	asset string // "" = cleared
	clock uint64
	eid   id.EventID
}

// AssetEdge is the projection of one edge.
type AssetEdge struct {
	Asset      string
	Role       string
	Label      string
	Ordinal    uint64
	Detached   bool
	Supersedes string
	Author     id.PrincipalID
	EventID    id.EventID
	Clock      uint64
	CreatedAt  uint64
	Candidate  bool // this asset is the object's candidate
}

// AnnotationNote is one media annotation on an asset's timeline.
type AnnotationNote struct {
	EventID     id.EventID
	Author      id.PrincipalID
	Text        string
	Asset       string
	PositionMs  uint64
	HasPosition bool
	ObjectID    *[16]byte
	CreatedAt   uint64
	Clock       uint64
}

func (s *State) applyObjectAttached(env *signal.Envelope, eid id.EventID) {
	p, err := objects.DecodeAttachPayload(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	rec := s.objRecFor(p.ObjectID)
	if rec.edges == nil {
		rec.edges = map[string]*assetEdge{}
	}
	cur := rec.edges[p.Asset]
	if cur == nil || later(env.LogicalClock, eid, cur.clock, cur.eid) {
		rec.edges[p.Asset] = &assetEdge{
			role: p.Role, label: p.Label, ordinal: p.Ordinal,
			detached: p.Detached, supersedes: p.Supersedes,
			author: env.Principal, eid: eid,
			clock: env.LogicalClock, createdAt: env.CreatedAt,
		}
	}
	// The candidate register moves ONLY when the event says so — an
	// absent key is a label edit, not a theft of the star.
	switch p.Candidate {
	case objects.CandidateSet:
		if rec.cand == nil || later(env.LogicalClock, eid, rec.cand.clock, rec.cand.eid) {
			rec.cand = &candReg{asset: p.Asset, clock: env.LogicalClock, eid: eid}
		}
	case objects.CandidateClear:
		if rec.cand == nil || later(env.LogicalClock, eid, rec.cand.clock, rec.cand.eid) {
			rec.cand = &candReg{asset: "", clock: env.LogicalClock, eid: eid}
		}
	}
}

func (s *State) applyAssetAnnotated(env *signal.Envelope, eid id.EventID) {
	a, err := schemas.DecodeAssetAnnotation(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	note := AnnotationNote{
		EventID: eid, Author: env.Principal, Text: a.Text, Asset: a.Asset,
		PositionMs: a.PositionMs, HasPosition: a.HasPosition(), ObjectID: a.ObjectID,
		CreatedAt: env.CreatedAt, Clock: env.LogicalClock,
	}
	if s.annots == nil {
		s.annots = map[string][]AnnotationNote{}
	}
	s.annots[a.Asset] = s.insertAnnotation(s.annots[a.Asset], note)
}

// insertAnnotation keeps a per-asset timeline sorted ascending by
// (CreatedAt, Clock, EventID) and bounded — the observation eviction law,
// verbatim: the surviving set is the greatest maxObservationsPerTimeline
// notes in the total order, so any arrival order converges. Evictions
// count into State.AnnotationEvicted — its own counter, not Unsupported.
func (s *State) insertAnnotation(list []AnnotationNote, n AnnotationNote) []AnnotationNote {
	for _, e := range list {
		if e.EventID == n.EventID {
			return list // replayed duplicate: idempotent
		}
	}
	pos := sort.Search(len(list), func(i int) bool { return annBefore(n, list[i]) })
	list = append(list, AnnotationNote{})
	copy(list[pos+1:], list[pos:])
	list[pos] = n
	if len(list) > maxObservationsPerTimeline {
		list = append(list[:0], list[1:]...) // evict the OLDEST
		s.AnnotationEvicted++
	}
	return list
}

func annBefore(a, b AnnotationNote) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt < b.CreatedAt
	}
	if a.Clock != b.Clock {
		return a.Clock < b.Clock
	}
	return string(a.EventID[:]) < string(b.EventID[:])
}

// AnnotationsForAsset lists one asset's notes, ascending.
func (s *State) AnnotationsForAsset(assetHex string) []AnnotationNote {
	return append([]AnnotationNote(nil), s.annots[assetHex]...)
}

// EdgesForObject projects an object's asset edges (detached included —
// the caller filters; detach history is part of the story). Order:
// (Ordinal, CreatedAt, Clock, EventID) — deterministic display order.
func (s *State) EdgesForObject(objectID [16]byte) []AssetEdge {
	rec, ok := s.objects[objectID]
	if !ok || len(rec.edges) == 0 {
		return nil
	}
	cand := ""
	if rec.cand != nil {
		cand = rec.cand.asset
	}
	out := make([]AssetEdge, 0, len(rec.edges))
	for asset, e := range rec.edges {
		out = append(out, AssetEdge{
			Asset: asset, Role: e.role, Label: e.label, Ordinal: e.ordinal,
			Detached: e.detached, Supersedes: e.supersedes,
			Author: e.author, EventID: e.eid, Clock: e.clock, CreatedAt: e.createdAt,
			Candidate: asset == cand && !e.detached,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Ordinal != b.Ordinal {
			return a.Ordinal < b.Ordinal
		}
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt < b.CreatedAt
		}
		if a.Clock != b.Clock {
			return a.Clock < b.Clock
		}
		return string(a.EventID[:]) < string(b.EventID[:])
	})
	return out
}

// digestEdges enumerates one object's edge registers sorted by asset hex,
// for the state digest only (detached included — a detached register is
// still converged state).
func (s *State) digestEdges(objectID [16]byte) []AssetEdge {
	rec, ok := s.objects[objectID]
	if !ok || len(rec.edges) == 0 {
		return nil
	}
	assets := make([]string, 0, len(rec.edges))
	for a := range rec.edges {
		assets = append(assets, a)
	}
	sort.Strings(assets)
	out := make([]AssetEdge, 0, len(assets))
	for _, a := range assets {
		e := rec.edges[a]
		out = append(out, AssetEdge{
			Asset: a, Role: e.role, Detached: e.detached,
			Supersedes: e.supersedes, EventID: e.eid,
		})
	}
	return out
}

// CandidateAsset returns the object's candidate register value ("" when
// none, or when the candidate edge is detached).
func (s *State) CandidateAsset(objectID [16]byte) string {
	rec, ok := s.objects[objectID]
	if !ok || rec.cand == nil || rec.cand.asset == "" {
		return ""
	}
	if e := rec.edges[rec.cand.asset]; e == nil || e.detached {
		return ""
	}
	return rec.cand.asset
}

// VersionChain is one lineage thread, newest first, starting at a head.
type VersionChain struct {
	Head  string
	Chain []AssetEdge // head first, then what it supersedes, transitively
}

// VersionChains derives the object's lineage threads from live edges:
// heads are live edges no live edge supersedes; each chain follows
// Supersedes through live edges with a seen-set, so duplicate supersedes
// become sibling chains and a cycle terminates — never a hang, rendered
// as flat threads. CurrentAsset picks what plays.
func (s *State) VersionChains(objectID [16]byte) []VersionChain {
	edges := s.EdgesForObject(objectID)
	live := map[string]AssetEdge{}
	superseded := map[string]bool{}
	for _, e := range edges {
		if e.Detached {
			continue
		}
		live[e.Asset] = e
		if e.Supersedes != "" {
			superseded[e.Supersedes] = true
		}
	}
	var heads []AssetEdge
	for _, e := range live {
		if !superseded[e.Asset] {
			heads = append(heads, e)
		}
	}
	// Newest head first — deterministic by winner (CreatedAt, Clock, EventID).
	sort.Slice(heads, func(i, j int) bool { return edgeNewer(heads[i], heads[j]) })
	out := make([]VersionChain, 0, len(heads))
	for _, h := range heads {
		chain := VersionChain{Head: h.Asset}
		seen := map[string]bool{}
		for cur, ok := h, true; ok && !seen[cur.Asset]; {
			seen[cur.Asset] = true
			chain.Chain = append(chain.Chain, cur)
			if cur.Supersedes == "" {
				break
			}
			cur, ok = live[cur.Supersedes], live[cur.Supersedes].Asset != ""
		}
		out = append(out, chain)
	}
	return out
}

func edgeNewer(a, b AssetEdge) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt > b.CreatedAt
	}
	if a.Clock != b.Clock {
		return a.Clock > b.Clock
	}
	return string(a.EventID[:]) > string(b.EventID[:])
}

// CurrentAsset is what the object "plays now": the candidate when set and
// live, else the newest head. Role-blind by law — the renderer decides
// what a mix is; the kernel only knows which relation is freshest.
func (s *State) CurrentAsset(objectID [16]byte) string {
	if c := s.CandidateAsset(objectID); c != "" {
		return c
	}
	chains := s.VersionChains(objectID)
	if len(chains) == 0 {
		return ""
	}
	return chains[0].Head
}

// ChildrenOf DERIVES the containment edge from Record.Parent — the same
// law as TasksForObject: a parent record never lists its children, so a
// child revision never touches the parent. Live children only, ordered
// (CreatedAt, Clock, ObjectID).
func (s *State) ChildrenOf(objectID [16]byte) []Object {
	var out []Object
	for _, o := range s.Objects() {
		if o.Record.Parent != nil && *o.Record.Parent == objectID {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt < b.CreatedAt
		}
		if a.Clock != b.Clock {
			return a.Clock < b.Clock
		}
		return string(a.ObjectID[:]) < string(b.ObjectID[:])
	})
	return out
}

// ObjectLiveAssetIDs is the retention/custody feed (ADR-030's law): every
// asset hex named by a LIVE, NON-DETACHED edge, plus every record's
// Cover. A live structural reference keeps an asset alive; a detached
// edge does not; an annotation NEVER does — commentary must not
// immortalize a deleted take.
func (s *State) ObjectLiveAssetIDs() map[string]struct{} {
	if len(s.objects) == 0 {
		return nil
	}
	out := map[string]struct{}{}
	for _, rec := range s.objects {
		for asset, e := range rec.edges {
			if !e.detached {
				out[asset] = struct{}{}
			}
		}
		if rec.recRaw == nil {
			continue
		}
		if r, err := objects.Decode(rec.recRaw); err == nil && r.Cover != "" {
			out[r.Cover] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
