// post.atmosphere.v1 — the environment a post is meant to be experienced in.
//
// A generative visual bed, an optional ambient audio bed, and a fallback that
// is never optional. It sits on the Document rather than in a side event so
// that editing atmosphere produces an ordinary publication revision: history,
// provenance and conflict handling all come from machinery that already works.
//
// TWO RULES SHAPE EVERY FIELD HERE.
//
//  1. RECIPE, NOT PERFORMANCE. What is signed is the deterministic FORM: a
//     scene id, a seed, bounded parameters. What the scene actually does moment
//     to moment — reacting to its own audio, to a pointer, to how long you have
//     been reading — is local, live, and never written down. The two cannot be
//     mixed: the instant a signed object depends on live audio or wall-clock
//     age it stops being reproducible, and the `seed` inside the signature
//     starts lying. So there is deliberately NO field here for elapsed time,
//     reaction counts or comment counts. Those exist at render time or not at
//     all.
//
//  2. ADR-013 INVARIANT 2 — NO EXECUTABLE PAYLOAD, EVER. Not HTML, CSS, JS,
//     script-bearing SVG, iframes, shaders, remote URLs as source of truth, or
//     unbounded animation. Only an allowlisted scene id with bounded numeric
//     parameters, content-addressed asset refs, and sanitised text. A client
//     never trusts a renderer IMPLEMENTATION that arrived from a space — only
//     ids it already ships. Every bound below exists to keep that true.
//
// The grammar deliberately mirrors block.live_signal.v1
// (protocol/schemas/blocks_types.go), which already carries
// engine/preset/seed/params through the same three-layer doctrine: producer
// UIs are strict, renderers are defensive, the network is tolerant.
package publication

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

// Bounds. All integers: deterministic CBOR carries no floats, so every
// continuous quantity is permille (0..1000).
const (
	MaxAtmosphereParams  = 16
	MaxAtmospherePalette = 4
	// MaxAtmosphereFallback is short on purpose. This is what a reader gets
	// when the scene cannot run; it describes the atmosphere, it is not a
	// second body for the post.
	MaxAtmosphereFallback = 200
	// MaxFadeMs bounds a fade so a "gentle" one cannot become a ten-minute
	// ramp that never audibly starts.
	MaxFadeMs = 10000
	// MaxPermille is the ceiling for every 0..1000 quantity.
	MaxPermille = 1000
)

// Audio playback modes.
const (
	AudioLoop = "loop"
	AudioOnce = "once"
)

var (
	// sceneRe is live_signal's preset grammar, verbatim: a lowercase name and
	// an explicit version. The version is part of the id because a scene that
	// changes what a seed means is a different scene, and silently rendering
	// an old recipe with new code would betray the determinism promise.
	sceneRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}@[1-9][0-9]{0,3}$`)
	// paramRe keeps parameter names machine-shaped so a renderer can switch
	// on them without normalising anything.
	paramRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	hexRe   = regexp.MustCompile(`^#[0-9a-f]{6}$`)
)

// Atmosphere is the whole recipe. A post without one is an ordinary post.
type Atmosphere struct {
	Visual  Visual
	Audio   *Audio
	React   *Reactive
	Fall    Fallback
	Derived *Derived
}

// Visual is the deterministic form: which scene, from which seed, with which
// bounded knobs.
type Visual struct {
	Scene   string // "name@version"
	Seed    uint64
	Params  []Param
	Palette []PaletteToken
}

// Param is one bounded knob. Value is permille, so "0.4 density" travels as
// 400 and no float ever touches the wire.
type Param struct {
	Name  string
	Value uint64
}

// PaletteToken matches composition.PaletteToken so a person moving between
// space appearance and post atmosphere meets one vocabulary, not two.
type PaletteToken struct {
	Name string
	Hex  string // #rrggbb, lowercase
}

// Audio is the ambient bed. Optional, and never autoplayed by the contract —
// whether it sounds at all is the reader's decision, made in the client.
type Audio struct {
	Asset     string // hex asset id: 32 or 64 lowercase hex characters
	Mode      string // loop | once
	Gain      uint64 // permille
	FadeInMs  uint64
	FadeOutMs uint64
}

// Reactive declares what the scene is ALLOWED to respond to. Two booleans,
// and both are narrow on purpose:
//
//   - Audio means the scene may read the analyser of ITS OWN bed and nothing
//     else. Not the microphone, not another space's listening session.
//   - Pointer means the scene may be perturbed by the cursor. Not tracked, not
//     recorded, not sent anywhere.
//
// Everything a scene reacts to is performance, so none of it is ever written
// back into the log.
type Reactive struct {
	Audio   bool
	Pointer bool
}

// Fallback is required, always. ADR-013 invariant 1: a renderer may degrade,
// meaning may not. A client that has never heard of this scene shows the text;
// it never shows a blank rectangle where an experience was promised.
type Fallback struct {
	Text   string // required, <= MaxAtmosphereFallback
	Poster string // optional hex asset id
}

// Derived is lineage. RecipeHash proves "this came from that recipe"; the two
// optional locators say WHERE that recipe lives, so the UI can name a parent
// instead of merely asserting descent. Anonymous inheritance keeps the hash
// alone.
type Derived struct {
	RecipeHash    []byte // 32 bytes
	PublicationID string // optional, hex
	RevisionHash  string // optional, hex
}

// Wire keys, append-only per ADR-009.
const (
	atmKeyVisual   = 1
	atmKeyAudio    = 2
	atmKeyReactive = 3
	atmKeyFallback = 4
	atmKeyDerived  = 5

	visKeyScene   = 1
	visKeySeed    = 2
	visKeyParams  = 3
	visKeyPalette = 4

	prmKeyName  = 1
	prmKeyValue = 2

	palKeyName = 1
	palKeyHex  = 2

	audKeyAsset   = 1
	audKeyMode    = 2
	audKeyGain    = 3
	audKeyFadeIn  = 4
	audKeyFadeOut = 5

	rctKeyAudio   = 1
	rctKeyPointer = 2

	fbKeyText   = 1
	fbKeyPoster = 2

	drvKeyRecipeHash = 1
	drvKeyPublicID   = 2
	drvKeyRevision   = 3
)

// AssetRefs lists every asset this atmosphere points at.
//
// BOTH of them, and the completeness matters more than it looks: an asset that
// is not reported here is not indexed, not fetchable, not servable, fails the
// authoring validator, and is excluded from the public projection by the
// custody gate. A poster omitted from this list would look fine to its author
// and be invisible to everybody else.
func (a *Atmosphere) AssetRefs() []string {
	if a == nil {
		return nil
	}
	var out []string
	if a.Audio != nil && a.Audio.Asset != "" {
		out = append(out, a.Audio.Asset)
	}
	if a.Fall.Poster != "" {
		out = append(out, a.Fall.Poster)
	}
	return out
}

// Validate checks everything provable from the bytes alone. Readability,
// contrast and whether a scene is too busy are the client's call at render
// time (ADR-013 invariant 5): the contract proposes an atmosphere, the client
// keeps the last word.
func (a *Atmosphere) Validate(assetOK func(hexID string) bool) error {
	if a == nil {
		return nil
	}
	if !sceneRe.MatchString(a.Visual.Scene) {
		return fmt.Errorf("publication: atmosphere scene %q is not name@version", a.Visual.Scene)
	}
	if len(a.Visual.Params) > MaxAtmosphereParams {
		return fmt.Errorf("publication: %d atmosphere params, at most %d",
			len(a.Visual.Params), MaxAtmosphereParams)
	}
	seen := make(map[string]struct{}, len(a.Visual.Params))
	for _, p := range a.Visual.Params {
		if !paramRe.MatchString(p.Name) {
			return fmt.Errorf("publication: atmosphere param name %q is not allowed", p.Name)
		}
		if _, dup := seen[p.Name]; dup {
			// A repeated name has no defined meaning: two renderers would be
			// free to disagree about which wins.
			return fmt.Errorf("publication: atmosphere param %q appears twice", p.Name)
		}
		seen[p.Name] = struct{}{}
		if p.Value > MaxPermille {
			return fmt.Errorf("publication: atmosphere param %q is %d, at most %d",
				p.Name, p.Value, MaxPermille)
		}
	}
	if len(a.Visual.Palette) > MaxAtmospherePalette {
		return fmt.Errorf("publication: %d palette tokens, at most %d",
			len(a.Visual.Palette), MaxAtmospherePalette)
	}
	for _, t := range a.Visual.Palette {
		if t.Name == "" || len(t.Name) > 64 {
			return errors.New("publication: palette token name is empty or too long")
		}
		if !hexRe.MatchString(t.Hex) {
			return fmt.Errorf("publication: palette colour %q is not #rrggbb lowercase", t.Hex)
		}
	}

	if a.Audio != nil {
		if err := checkAssetRef(a.Audio.Asset, assetOK, "atmosphere audio"); err != nil {
			return err
		}
		if a.Audio.Mode != AudioLoop && a.Audio.Mode != AudioOnce {
			return fmt.Errorf("publication: atmosphere audio mode %q is not loop or once",
				a.Audio.Mode)
		}
		if a.Audio.Gain > MaxPermille {
			return fmt.Errorf("publication: atmosphere gain %d, at most %d",
				a.Audio.Gain, MaxPermille)
		}
		if a.Audio.FadeInMs > MaxFadeMs || a.Audio.FadeOutMs > MaxFadeMs {
			return fmt.Errorf("publication: atmosphere fade longer than %dms", MaxFadeMs)
		}
	}

	// The fallback is the one thing that cannot be skipped.
	if a.Fall.Text == "" {
		return errors.New("publication: atmosphere needs fallback text — a client " +
			"that does not know this scene must still be able to say what was here")
	}
	if len([]rune(a.Fall.Text)) > MaxAtmosphereFallback {
		return fmt.Errorf("publication: atmosphere fallback text longer than %d characters",
			MaxAtmosphereFallback)
	}
	if a.Fall.Poster != "" {
		if err := checkAssetRef(a.Fall.Poster, assetOK, "atmosphere poster"); err != nil {
			return err
		}
	}

	if a.Derived != nil {
		if len(a.Derived.RecipeHash) != 32 {
			return fmt.Errorf("publication: derived_from recipe hash is %d bytes, want 32",
				len(a.Derived.RecipeHash))
		}
		if a.Derived.PublicationID != "" && len(a.Derived.PublicationID) != 32 {
			return errors.New("publication: derived_from publication id must be a " +
				"32-character hex document id")
		}
		if a.Derived.RevisionHash != "" && len(a.Derived.RevisionHash) != 64 {
			return errors.New("publication: derived_from revision hash must be a " +
				"64-character hex event id")
		}
	}
	return nil
}

// RecipeHash is the digest that lineage points at: the visual recipe alone.
//
// Deliberately NOT the whole atmosphere. Two people who start from the same
// scene and the same seed have inherited the same thing whether or not they
// also picked the same background track, and a hash that changed when somebody
// swapped the audio would break every chain for a reason nobody would call
// descent.
func (a *Atmosphere) RecipeHash() []byte {
	if a == nil {
		return nil
	}
	h := sha256.Sum256(append([]byte("qs.atmosphere.recipe.v1"), encodeVisual(nil, &a.Visual)...))
	return h[:]
}

func (a *Atmosphere) encode(buf []byte) []byte {
	n := 2 // visual + fallback are always present
	if a.Audio != nil {
		n++
	}
	if a.React != nil {
		n++
	}
	if a.Derived != nil {
		n++
	}
	buf = codec.AppendMap(buf, n)

	buf = codec.AppendUint(buf, atmKeyVisual)
	buf = encodeVisual(buf, &a.Visual)

	if a.Audio != nil {
		buf = codec.AppendUint(buf, atmKeyAudio)
		na := 2 // asset + mode
		if a.Audio.Gain > 0 {
			na++
		}
		if a.Audio.FadeInMs > 0 {
			na++
		}
		if a.Audio.FadeOutMs > 0 {
			na++
		}
		buf = codec.AppendMap(buf, na)
		buf = codec.AppendUint(buf, audKeyAsset)
		buf = codec.AppendText(buf, a.Audio.Asset)
		buf = codec.AppendUint(buf, audKeyMode)
		buf = codec.AppendText(buf, a.Audio.Mode)
		if a.Audio.Gain > 0 {
			buf = codec.AppendUint(buf, audKeyGain)
			buf = codec.AppendUint(buf, a.Audio.Gain)
		}
		if a.Audio.FadeInMs > 0 {
			buf = codec.AppendUint(buf, audKeyFadeIn)
			buf = codec.AppendUint(buf, a.Audio.FadeInMs)
		}
		if a.Audio.FadeOutMs > 0 {
			buf = codec.AppendUint(buf, audKeyFadeOut)
			buf = codec.AppendUint(buf, a.Audio.FadeOutMs)
		}
	}

	if a.React != nil {
		buf = codec.AppendUint(buf, atmKeyReactive)
		nr := 0
		if a.React.Audio {
			nr++
		}
		if a.React.Pointer {
			nr++
		}
		buf = codec.AppendMap(buf, nr)
		if a.React.Audio {
			buf = codec.AppendUint(buf, rctKeyAudio)
			buf = codec.AppendBool(buf, true)
		}
		if a.React.Pointer {
			buf = codec.AppendUint(buf, rctKeyPointer)
			buf = codec.AppendBool(buf, true)
		}
	}

	buf = codec.AppendUint(buf, atmKeyFallback)
	nf := 1
	if a.Fall.Poster != "" {
		nf++
	}
	buf = codec.AppendMap(buf, nf)
	buf = codec.AppendUint(buf, fbKeyText)
	buf = codec.AppendText(buf, a.Fall.Text)
	if a.Fall.Poster != "" {
		buf = codec.AppendUint(buf, fbKeyPoster)
		buf = codec.AppendText(buf, a.Fall.Poster)
	}

	if a.Derived != nil {
		buf = codec.AppendUint(buf, atmKeyDerived)
		nd := 1
		if a.Derived.PublicationID != "" {
			nd++
		}
		if a.Derived.RevisionHash != "" {
			nd++
		}
		buf = codec.AppendMap(buf, nd)
		buf = codec.AppendUint(buf, drvKeyRecipeHash)
		buf = codec.AppendBytes(buf, a.Derived.RecipeHash)
		if a.Derived.PublicationID != "" {
			buf = codec.AppendUint(buf, drvKeyPublicID)
			buf = codec.AppendText(buf, a.Derived.PublicationID)
		}
		if a.Derived.RevisionHash != "" {
			buf = codec.AppendUint(buf, drvKeyRevision)
			buf = codec.AppendText(buf, a.Derived.RevisionHash)
		}
	}
	return buf
}

func encodeVisual(buf []byte, v *Visual) []byte {
	n := 2 // scene + seed
	if len(v.Params) > 0 {
		n++
	}
	if len(v.Palette) > 0 {
		n++
	}
	buf = codec.AppendMap(buf, n)
	buf = codec.AppendUint(buf, visKeyScene)
	buf = codec.AppendText(buf, v.Scene)
	buf = codec.AppendUint(buf, visKeySeed)
	buf = codec.AppendUint(buf, v.Seed)
	if len(v.Params) > 0 {
		buf = codec.AppendUint(buf, visKeyParams)
		buf = codec.AppendArray(buf, len(v.Params))
		for _, p := range v.Params {
			buf = codec.AppendMap(buf, 2)
			buf = codec.AppendUint(buf, prmKeyName)
			buf = codec.AppendText(buf, p.Name)
			buf = codec.AppendUint(buf, prmKeyValue)
			buf = codec.AppendUint(buf, p.Value)
		}
	}
	if len(v.Palette) > 0 {
		buf = codec.AppendUint(buf, visKeyPalette)
		buf = codec.AppendArray(buf, len(v.Palette))
		for _, t := range v.Palette {
			buf = codec.AppendMap(buf, 2)
			buf = codec.AppendUint(buf, palKeyName)
			buf = codec.AppendText(buf, t.Name)
			buf = codec.AppendUint(buf, palKeyHex)
			buf = codec.AppendText(buf, t.Hex)
		}
	}
	return buf
}

func decodeAtmosphere(d *codec.Decoder) (*Atmosphere, error) {
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	a := &Atmosphere{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case atmKeyVisual:
			er = decodeVisual(d, &a.Visual)
		case atmKeyAudio:
			a.Audio, er = decodeAudio(d)
		case atmKeyReactive:
			a.React, er = decodeReactive(d)
		case atmKeyFallback:
			er = decodeFallback(d, &a.Fall)
		case atmKeyDerived:
			a.Derived, er = decodeDerived(d)
		default:
			er = d.SkipItem() // must-ignore (ADR-009)
		}
		if er != nil {
			return nil, er
		}
	}
	return a, nil
}

func decodeVisual(d *codec.Decoder, v *Visual) error {
	mr, err := d.ReadMapHeader()
	if err != nil {
		return err
	}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return er
		}
		if !ok {
			return nil
		}
		switch k {
		case visKeyScene:
			v.Scene, er = d.ReadText()
		case visKeySeed:
			v.Seed, er = d.ReadUint()
		case visKeyParams:
			cnt, e := d.ReadArray()
			if e != nil {
				return e
			}
			if cnt > MaxAtmosphereParams {
				return errors.New("publication: too many atmosphere params")
			}
			for range cnt {
				p, e := decodeParam(d)
				if e != nil {
					return e
				}
				v.Params = append(v.Params, p)
			}
		case visKeyPalette:
			cnt, e := d.ReadArray()
			if e != nil {
				return e
			}
			if cnt > MaxAtmospherePalette {
				return errors.New("publication: too many palette tokens")
			}
			for range cnt {
				t, e := decodePaletteToken(d)
				if e != nil {
					return e
				}
				v.Palette = append(v.Palette, t)
			}
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return er
		}
	}
}

func decodeParam(d *codec.Decoder) (Param, error) {
	mr, err := d.ReadMapHeader()
	if err != nil {
		return Param{}, err
	}
	var p Param
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return Param{}, er
		}
		if !ok {
			return p, nil
		}
		switch k {
		case prmKeyName:
			p.Name, er = d.ReadText()
		case prmKeyValue:
			p.Value, er = d.ReadUint()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return Param{}, er
		}
	}
}

func decodePaletteToken(d *codec.Decoder) (PaletteToken, error) {
	mr, err := d.ReadMapHeader()
	if err != nil {
		return PaletteToken{}, err
	}
	var t PaletteToken
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return PaletteToken{}, er
		}
		if !ok {
			return t, nil
		}
		switch k {
		case palKeyName:
			t.Name, er = d.ReadText()
		case palKeyHex:
			t.Hex, er = d.ReadText()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return PaletteToken{}, er
		}
	}
}

func decodeAudio(d *codec.Decoder) (*Audio, error) {
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	a := &Audio{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			return a, nil
		}
		switch k {
		case audKeyAsset:
			a.Asset, er = d.ReadText()
		case audKeyMode:
			a.Mode, er = d.ReadText()
		case audKeyGain:
			a.Gain, er = d.ReadUint()
		case audKeyFadeIn:
			a.FadeInMs, er = d.ReadUint()
		case audKeyFadeOut:
			a.FadeOutMs, er = d.ReadUint()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
}

func decodeReactive(d *codec.Decoder) (*Reactive, error) {
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	r := &Reactive{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			return r, nil
		}
		switch k {
		case rctKeyAudio:
			r.Audio, er = d.ReadBool()
		case rctKeyPointer:
			r.Pointer, er = d.ReadBool()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
}

func decodeFallback(d *codec.Decoder, f *Fallback) error {
	mr, err := d.ReadMapHeader()
	if err != nil {
		return err
	}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return er
		}
		if !ok {
			return nil
		}
		switch k {
		case fbKeyText:
			f.Text, er = d.ReadText()
		case fbKeyPoster:
			f.Poster, er = d.ReadText()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return er
		}
	}
}

func decodeDerived(d *codec.Decoder) (*Derived, error) {
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	dv := &Derived{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			return dv, nil
		}
		switch k {
		case drvKeyRecipeHash:
			dv.RecipeHash, er = d.ReadBytes()
		case drvKeyPublicID:
			dv.PublicationID, er = d.ReadText()
		case drvKeyRevision:
			dv.RevisionHash, er = d.ReadText()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
}
