package composition

import (
	"encoding/hex"
	"errors"
	"fmt"
)

// Contract-time validation (ADR-013 refinement 4): everything provable on the
// backend from the bytes alone — schema, signature, limits, allowlists, no
// executable payload, fallback presence. Readability/contrast is deferred to
// the client's ValidateRenderedSpace and is NOT checked here.

// Allowlisted renderer ids. A client never trusts a renderer implementation
// from a space — only these ids, which it already ships. An id outside the
// allowlist is the "no executable payload" guarantee: there is no free-form
// markup to smuggle, only a known id or a rejection.
var AllowedObjectRenderers = map[string]bool{
	"generic.text.v1": true, "generic.image.v1": true, "generic.audio.v1": true,
	"generic.video.v1": true, "generic.file.v1": true, "generic.link.v1": true,
	"message.relic.v1": true, "music.card.v1": true, "image.card.v1": true,
	"video.poster.v1": true, "file.card.v1": true, "link.card.v1": true,
	"note.card.v1": true,
}

var AllowedZoneRenderers = map[string]bool{
	"shelf.compact.v1": true, "ordered-list.v1": true,
	"wall.freeform-lite.v1": true, "card-grid.v1": true, "pinned-rail.v1": true,
}

var AllowedSemanticKinds = map[string]bool{
	"text": true, "image": true, "audio": true, "video": true, "file": true,
	"link": true, "note": true, "device": true, "bot": true,
}

var allowedMotion = map[string]bool{"quiet": true, "still": true, "lively": true}
var allowedDensity = map[string]bool{"calm": true, "dense": true}
var allowedVariants = map[string]bool{"original": true, "preview": true, "thumbnail": true, "lqip": true}

const (
	maxObjects   = 256
	maxZones     = 32
	maxTextField = 200
	maxIDField   = 64
	maxBlurPx    = 64
)

// sanitizeText rejects control characters and over-long strings — the only
// text that reaches a renderer must be inert, bounded content.
func sanitizeText(s string, max int, what string) error {
	if len(s) > max {
		return fmt.Errorf("composition: %s too long", what)
	}
	for _, r := range s {
		if r < 0x20 && r != '\t' && r != '\n' {
			return fmt.Errorf("composition: %s contains control characters", what)
		}
	}
	return nil
}

func validHexRef(s string, what string) error {
	if s == "" {
		return nil
	}
	b, err := hex.DecodeString(s)
	if err != nil || (len(b) != 16 && len(b) != 32) {
		return fmt.Errorf("composition: %s is not a valid asset/event id", what)
	}
	return nil
}

func validColor(s, what string) error {
	if s == "" {
		return nil
	}
	if len(s) != 7 || s[0] != '#' {
		return fmt.Errorf("composition: %s must be #rrggbb", what)
	}
	if _, err := hex.DecodeString(s[1:]); err != nil {
		return fmt.Errorf("composition: %s must be #rrggbb", what)
	}
	return nil
}

// ValidateAppearance checks the atmosphere layer.
func ValidateAppearance(a *Appearance) error {
	for _, p := range a.Palette {
		if err := sanitizeText(p.Name, maxIDField, "palette token name"); err != nil {
			return err
		}
		if err := validColor(p.Hex, "palette color"); err != nil {
			return err
		}
	}
	if b := a.Background; b != nil {
		if err := validHexRef(b.AssetID, "background asset"); err != nil {
			return err
		}
		if err := validColor(b.Tint, "background tint"); err != nil {
			return err
		}
		if b.Blur > maxBlurPx {
			return fmt.Errorf("composition: background blur %d exceeds %d px", b.Blur, maxBlurPx)
		}
		for _, v := range []uint64{b.Dim, b.Saturation, b.Grain, b.Vignette} {
			if v > maxMilli {
				return errors.New("composition: background treatment out of [0,1]")
			}
		}
	}
	if a.MotionPolicy != "" && !allowedMotion[a.MotionPolicy] {
		return fmt.Errorf("composition: motion policy %q not allowed", a.MotionPolicy)
	}
	if a.Density != "" && !allowedDensity[a.Density] {
		return fmt.Errorf("composition: density %q not allowed", a.Density)
	}
	if a.Noise > maxMilli {
		return errors.New("composition: noise out of [0,1]")
	}
	return nil
}

// ValidateComposition checks the arrangement layer.
func ValidateComposition(c *Composition) error {
	if c.CoordinateSystem != CoordinateSystem {
		return fmt.Errorf("composition: unsupported coordinate system %q", c.CoordinateSystem)
	}
	if len(c.Zones) > maxZones {
		return errors.New("composition: too many zones")
	}
	if len(c.Objects) > maxObjects {
		return errors.New("composition: too many objects")
	}
	zones := map[string]bool{}
	for _, z := range c.Zones {
		if err := sanitizeText(z.ID, maxIDField, "zone id"); err != nil || z.ID == "" {
			return errors.New("composition: bad zone id")
		}
		if zones[z.ID] {
			return fmt.Errorf("composition: duplicate zone %q", z.ID)
		}
		zones[z.ID] = true
		if !AllowedZoneRenderers[z.Renderer] {
			return fmt.Errorf("composition: zone renderer %q not allowlisted", z.Renderer)
		}
		if !AllowedZoneRenderers[z.FallbackRenderer] {
			return fmt.Errorf("composition: zone fallback %q not allowlisted", z.FallbackRenderer)
		}
	}
	ids := map[string]bool{}
	for i := range c.Objects {
		o := &c.Objects[i]
		if o.ID == "" || ids[o.ID] {
			return errors.New("composition: object id missing or duplicate")
		}
		ids[o.ID] = true
		if err := sanitizeText(o.ID, maxIDField, "object id"); err != nil {
			return err
		}
		if !AllowedSemanticKinds[o.SemanticKind] {
			return fmt.Errorf("composition: semantic kind %q not allowed", o.SemanticKind)
		}
		if o.ZoneID != "" && !zones[o.ZoneID] {
			return fmt.Errorf("composition: object %q references unknown zone %q", o.ID, o.ZoneID)
		}
		if !AllowedObjectRenderers[o.Renderer] {
			return fmt.Errorf("composition: renderer %q not allowlisted", o.Renderer)
		}
		// Meaning must survive an unknown renderer: a fallback renderer AND
		// readable fallback metadata are mandatory (ADR-013 invariant 1).
		if !AllowedObjectRenderers[o.FallbackRenderer] {
			return fmt.Errorf("composition: fallback renderer %q not allowlisted", o.FallbackRenderer)
		}
		if o.FallbackTitle == "" {
			return fmt.Errorf("composition: object %q has no fallback title", o.ID)
		}
		for _, f := range []string{o.FallbackTitle, o.FallbackAuthor, o.FallbackDetail} {
			if err := sanitizeText(f, maxTextField, "fallback text"); err != nil {
				return err
			}
		}
		if err := validHexRef(o.SourceEventID, "source event"); err != nil {
			return err
		}
		if err := validHexRef(o.SourceAssetID, "source asset"); err != nil {
			return err
		}
		if err := o.Transform.validate(); err != nil {
			return err
		}
	}
	if err := validHexRef(c.GraffitiPreview, "graffiti preview"); err != nil {
		return err
	}
	return nil
}

// ValidateBundle checks an immutable bundle body.
func ValidateBundle(b *Bundle) error {
	if !bundleKinds[b.Kind] {
		return fmt.Errorf("composition: bundle kind %q not allowed", b.Kind)
	}
	for _, e := range b.Entries {
		if err := validHexRef(e.AssetID, "bundle entry asset"); err != nil || e.AssetID == "" {
			return errors.New("composition: bundle entry needs a valid asset id")
		}
		if !allowedVariants[e.Variant] {
			return fmt.Errorf("composition: variant %q not allowed", e.Variant)
		}
		if e.EncryptionEpoch == 0 {
			return errors.New("composition: bundle entry needs an encryption epoch")
		}
	}
	return nil
}

// ValidateBundleIndex checks a bundle-index body.
func ValidateBundleIndex(bi *BundleIndex) error {
	for _, r := range bi.Bundles {
		if !bundleKinds[r.Kind] {
			return fmt.Errorf("composition: bundle kind %q not allowed", r.Kind)
		}
	}
	return nil
}

// ValidateContract is the backend gate: it verifies the signed snapshot tip
// and validates the payload for its declared kind. It is the provable half of
// ADR-013 refinement 4 — readability stays with the client.
func ValidateContract(frame []byte) error {
	s, err := DecodeSnapshot(frame)
	if err != nil {
		return err
	}
	if err := s.Verify(); err != nil {
		return err
	}
	switch s.DocumentKind {
	case KindAppearance:
		a, err := DecodeAppearance(s.Payload)
		if err != nil {
			return err
		}
		return ValidateAppearance(a)
	case KindComposition:
		c, err := DecodeComposition(s.Payload)
		if err != nil {
			return err
		}
		return ValidateComposition(c)
	case KindBundleIndex:
		bi, err := DecodeBundleIndex(s.Payload)
		if err != nil {
			return err
		}
		return ValidateBundleIndex(bi)
	default:
		return fmt.Errorf("composition: unknown document kind %q", s.DocumentKind)
	}
}
