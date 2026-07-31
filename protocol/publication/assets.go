// The ONE walk over every place a Document names an asset (PA-Assets).
//
// Before this file there were two traversals that could diverge: Validate
// checked assets site by site during its tree walk, and anything wanting
// to ENUMERATE them (the projection builder, a preview's allowlist) would
// have had to re-derive the sites — and forget one, most likely the
// video-link poster hiding in AssetProps.Caption. Now there is exactly one
// traversal; Validate consumes it, so a new asset-bearing field added here
// is checked AND enumerated, or neither, and the compiler keeps the two
// halves honest.
package publication

// visitAssets calls visit for every asset id the document references,
// exactly once per site, with the site's role name (which doubles as the
// word in Validate's error). Empty ids are skipped — absence is legal
// everywhere except where the tree walk separately demands presence.
//
// Roles: cover · atmosphere_audio · atmosphere_poster · image · audio ·
// file · poster (video-link) · gallery item.
func (d *Document) visitAssets(visit func(hexID, role string)) {
	if d.Cover != "" {
		visit(d.Cover, "cover")
	}
	if a := d.Atmosphere; a != nil {
		if a.Audio != nil && a.Audio.Asset != "" {
			visit(a.Audio.Asset, "atmosphere_audio")
		}
		if a.Fall.Poster != "" {
			visit(a.Fall.Poster, "atmosphere_poster")
		}
	}
	var walk func(b *Block)
	walk = func(b *Block) {
		if parsed, err := parseKnownProps(b); err == nil {
			switch p := parsed.(type) {
			case AssetProps:
				switch b.Type {
				case "video-link":
					// Key 1 is the URL; the ASSET is the poster in key 3.
					if p.Caption != "" {
						visit(p.Caption, "poster")
					}
				case "app":
					// Key 1 is an instance id, not an asset.
				default: // image, audio, file
					if p.Asset != "" {
						visit(p.Asset, b.Type)
					}
				}
			case ListProps:
				if b.Type == "gallery" {
					for _, it := range p.Items {
						if it != "" {
							visit(it, "gallery item")
						}
					}
				}
			}
		}
		for i := range b.Children {
			walk(&b.Children[i])
		}
	}
	for i := range d.Blocks {
		walk(&d.Blocks[i])
	}
}

// LiveAssetIDs is the document's asset graph: every referenced id with the
// role it plays. The projection builder keeps carriers of these alive, and
// a transient reader's allowlist is exactly this set — never "any asset of
// the space". Where an id serves two roles the first (document order) wins;
// the id is what matters downstream.
func (d *Document) LiveAssetIDs() map[string]string {
	out := map[string]string{}
	d.visitAssets(func(hexID, role string) {
		if _, seen := out[hexID]; !seen {
			out[hexID] = role
		}
	})
	return out
}
