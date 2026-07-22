package composition

// Sample* build a small, valid contract used by tests and the cross-client
// rendering proof (SC-0 step 9). They are the canonical "foreign space" a
// visitor renders without media.

// SampleAppearance is a calm violet studio atmosphere.
func SampleAppearance() *Appearance {
	return &Appearance{
		Palette: []PaletteToken{
			{Name: "canvas", Hex: "#0d0a10"},
			{Name: "accent", Hex: "#c48ae4"},
			{Name: "signal", Hex: "#8ebaa8"},
		},
		Background:   &Background{Tint: "#24172d", Blur: 14, Dim: 420, Vignette: 140},
		MotionPolicy: "quiet",
		Density:      "calm",
		Noise:        60,
	}
}

// SampleComposition is a shelf + wall with one music card and one quote relic.
func SampleComposition() *Composition {
	return &Composition{
		CoordinateSystem: CoordinateSystem,
		Zones: []Zone{
			{ID: "shelf", Kind: "shelf", Renderer: "shelf.compact.v1", FallbackRenderer: "ordered-list.v1"},
			{ID: "wall", Kind: "wall", Renderer: "wall.freeform-lite.v1", FallbackRenderer: "card-grid.v1"},
		},
		Objects: []Object{
			{
				ID: "object:music-1", SemanticKind: "audio", ZoneID: "shelf",
				Renderer: "music.card.v1", FallbackRenderer: "generic.audio.v1",
				FallbackTitle: "Night Drive", FallbackAuthor: "Alice", FallbackDetail: "3:42",
				Transform: Transform{X: 100, Y: 200, W: 280, H: 140, RotationDeci: 0, Z: 2},
			},
			{
				ID: "object:quote-1", SemanticKind: "text", ZoneID: "wall",
				Renderer: "message.relic.v1", FallbackRenderer: "generic.text.v1",
				FallbackTitle: "“this place remembers”", FallbackAuthor: "Bob",
				Transform: Transform{X: 620, Y: 180, W: 220, H: 160, RotationDeci: -40, Z: 4},
			},
		},
	}
}

// SampleBundleIndex references one core bundle.
func SampleBundleIndex() *BundleIndex {
	core := &Bundle{Kind: BundleCore, Priority: 20, EstimatedBytes: 32_000}
	return &BundleIndex{Bundles: []BundleRef{
		{BundleID: core.ID(), Kind: BundleCore, Priority: 20, EstimatedBytes: 32_000},
	}}
}
