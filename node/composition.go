// Space Composition Contract projection (SC-0). The owner node materializes a
// space's appearance/composition/bundle-index as signed snapshots (ADR-013)
// and serves them. Building the projection here is a producer-side detail
// (full replay of local state); a visiting client verifies the signed tip and
// never replays. For SC-0 the composition is the zone shell — object placement
// (Shelf/Wall population) arrives in SC-2.
package node

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/drrainlab/quiet_places/protocol/composition"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
)

// archetypePalette mirrors the UI archetype accents so a projected appearance
// matches how the space already reads. Canvas stays the Quiet Terminal Glass
// base; accent/signal come from the archetype.
var archetypePalette = map[string][3]string{
	//                     canvas     accent     signal
	"campfire":   {"#14100b", "#e8a862", "#8ebaa8"},
	"forest":     {"#0c1210", "#64d8a4", "#8ebaa8"},
	"studio":     {"#100c14", "#c88ce0", "#8ebaa8"},
	"workshop":   {"#0c1014", "#7ab4e0", "#8ebaa8"},
	"orbit":      {"#0a0d12", "#9cc2e8", "#8ebaa8"},
	"home":       {"#12100d", "#e0b49c", "#8ebaa8"},
	"radio_room": {"#0a0f0a", "#6ce07a", "#8ebaa8"},
}

func motionPolicy(characterMotion string) string {
	switch characterMotion {
	case "still":
		return "still"
	case "lively":
		return "lively"
	default:
		return "quiet"
	}
}

// projectAppearance derives the atmosphere layer from the space character.
func projectAppearance(c terminals.Character) *composition.Appearance {
	pal := archetypePalette[c.Archetype]
	if pal[0] == "" {
		pal = [3]string{"#0d0a10", "#c48ae4", "#8ebaa8"}
	}
	return &composition.Appearance{
		Palette: []composition.PaletteToken{
			{Name: "canvas", Hex: pal[0]},
			{Name: "accent", Hex: pal[1]},
			{Name: "signal", Hex: pal[2]},
		},
		Background:   &composition.Background{Tint: pal[0], Dim: 380, Vignette: 120},
		MotionPolicy: motionPolicy(c.Motion),
		Density:      "calm",
		Noise:        60,
	}
}

// projectComposition is the zone shell for SC-0 (no placed objects yet).
func projectComposition() *composition.Composition {
	return &composition.Composition{
		CoordinateSystem: composition.CoordinateSystem,
		Zones: []composition.Zone{
			{ID: "shelf", Kind: "shelf", Renderer: "shelf.compact.v1", FallbackRenderer: "ordered-list.v1"},
			{ID: "wall", Kind: "wall", Renderer: "wall.freeform-lite.v1", FallbackRenderer: "card-grid.v1"},
		},
	}
}

// spaceTerminalKey reconstructs an owned space's terminal signing key.
func (r *Runtime) spaceTerminalKey(tid id.TerminalID) (ed25519.PrivateKey, error) {
	seed, ok := r.ks.TerminalSeeds[tid]
	if !ok || len(seed) != ed25519.SeedSize {
		return nil, errors.New("node: space is not owned here (no terminal seed to sign a snapshot)")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// snapshotFrame projects, signs, and returns the canonical frame for one kind.
func (r *Runtime) snapshotFrame(tid id.TerminalID, kind string, payload []byte) ([]byte, error) {
	priv, err := r.spaceTerminalKey(tid)
	if err != nil {
		return nil, err
	}
	st, ok := r.spaces[tid]
	if !ok {
		return nil, errors.New("node: unknown space")
	}
	snap, err := composition.NewSnapshot(tid, kind, 1, st.space.MaxClock(), nil, payload)
	if err != nil {
		return nil, err
	}
	return snap.Sign(priv)
}

// AppearanceFrame / CompositionFrame / BundleIndexFrame return signed frames.
func (r *Runtime) AppearanceFrame(tid id.TerminalID) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return nil, errors.New("node: unknown space")
	}
	_, ch := st.space.Character()
	return r.snapshotFrame(tid, composition.KindAppearance, projectAppearance(ch).Encode())
}

func (r *Runtime) CompositionFrame(tid id.TerminalID) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotFrame(tid, composition.KindComposition, projectComposition().Encode())
}

func (r *Runtime) BundleIndexFrame(tid id.TerminalID) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotFrame(tid, composition.KindBundleIndex, (&composition.BundleIndex{}).Encode())
}

// ---- API: the signed contract + a JSON projection for the local web client.
// The web UI is a local client of its own node (ADR-011) and renders the JSON;
// the frame_b64 is the portable, signature-verifiable document for any other
// holder (a foreign Go node verifies it without replay).

func snapshotJSON(frame []byte) (map[string]any, error) {
	s, err := composition.DecodeSnapshot(frame)
	if err != nil {
		return nil, err
	}
	if err := composition.ValidateContract(frame); err != nil {
		return nil, err
	}
	h := composition.Hash(frame)
	out := map[string]any{
		"kind":              s.DocumentKind,
		"revision":          s.Revision,
		"projected_through": s.ProjectedThrough,
		"frame_b64":         base64.StdEncoding.EncodeToString(frame),
		"frame_hash":        hex.EncodeToString(h[:]),
	}
	switch s.DocumentKind {
	case composition.KindAppearance:
		a, _ := composition.DecodeAppearance(s.Payload)
		out["appearance"] = appearanceJSON(a)
	case composition.KindComposition:
		c, _ := composition.DecodeComposition(s.Payload)
		out["composition"] = compositionJSON(c)
	case composition.KindBundleIndex:
		bi, _ := composition.DecodeBundleIndex(s.Payload)
		out["bundle_index"] = bundleIndexJSON(bi)
	}
	return out, nil
}

func appearanceJSON(a *composition.Appearance) map[string]any {
	pal := make([]map[string]string, 0, len(a.Palette))
	for _, p := range a.Palette {
		pal = append(pal, map[string]string{"name": p.Name, "hex": p.Hex})
	}
	m := map[string]any{"palette": pal, "motion": a.MotionPolicy, "density": a.Density, "noise": a.Noise}
	if a.Background != nil {
		m["background"] = map[string]any{
			"asset_id": a.Background.AssetID, "tint": a.Background.Tint,
			"blur": a.Background.Blur, "dim": a.Background.Dim,
			"saturation": a.Background.Saturation, "grain": a.Background.Grain,
			"vignette": a.Background.Vignette,
		}
	}
	return m
}

func compositionJSON(c *composition.Composition) map[string]any {
	zones := make([]map[string]any, 0, len(c.Zones))
	for _, z := range c.Zones {
		zones = append(zones, map[string]any{"id": z.ID, "kind": z.Kind,
			"renderer": z.Renderer, "fallback_renderer": z.FallbackRenderer})
	}
	objs := make([]map[string]any, 0, len(c.Objects))
	for _, o := range c.Objects {
		objs = append(objs, map[string]any{
			"id": o.ID, "semantic_kind": o.SemanticKind, "zone_id": o.ZoneID,
			"renderer": o.Renderer, "fallback_renderer": o.FallbackRenderer,
			"fallback_title": o.FallbackTitle, "fallback_author": o.FallbackAuthor,
			"fallback_detail": o.FallbackDetail, "source_asset_id": o.SourceAssetID,
			"transform": map[string]any{"x": o.Transform.X, "y": o.Transform.Y,
				"w": o.Transform.W, "h": o.Transform.H,
				"rotation_deci": o.Transform.RotationDeci, "z": o.Transform.Z},
		})
	}
	return map[string]any{"coordinate_system": c.CoordinateSystem, "zones": zones, "objects": objs}
}

func bundleIndexJSON(bi *composition.BundleIndex) map[string]any {
	bs := make([]map[string]any, 0, len(bi.Bundles))
	for _, r := range bi.Bundles {
		bs = append(bs, map[string]any{"bundle_id": hex.EncodeToString(r.BundleID[:]),
			"kind": r.Kind, "priority": r.Priority, "estimated_bytes": r.EstimatedBytes})
	}
	return map[string]any{"bundles": bs}
}

func (a *APIServer) serveSnapshot(w http.ResponseWriter, r *http.Request, frameFn func(id.TerminalID) ([]byte, error)) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	frame, err := frameFn(tid)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	j, err := snapshotJSON(frame)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, j)
}

func (a *APIServer) handleAppearance(w http.ResponseWriter, r *http.Request) {
	a.serveSnapshot(w, r, a.rt.AppearanceFrame)
}
func (a *APIServer) handleComposition(w http.ResponseWriter, r *http.Request) {
	a.serveSnapshot(w, r, a.rt.CompositionFrame)
}
func (a *APIServer) handleBundles(w http.ResponseWriter, r *http.Request) {
	a.serveSnapshot(w, r, a.rt.BundleIndexFrame)
}
