package composition

import (
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// CoordinateSystem is the only supported placement space in v1: x,y,w,h are
// milliunits in [0,1000]; rotation is deci-degrees in [-150,150]; z is a small
// bounded integer.
const CoordinateSystem = "normalized-2d.v1"

const (
	maxMilli    = 1000 // 1.0 in milliunits
	maxRotation = 150  // 15.0 deg in deci-degrees
	maxZ        = 64
	rotOffset   = 150 // encode signed rotation as an unsigned offset
)

// ---- Appearance ----

// Appearance is the atmosphere layer: palette, background treatment, motion
// policy. It carries no object placement.
type Appearance struct {
	Palette      []PaletteToken
	Background   *Background
	MotionPolicy string // quiet | still | lively (bounded vocabulary)
	Density      string // calm | dense
	Noise        uint64 // 0..1000 milliunits
}

type PaletteToken struct {
	Name string // canvas | surface_tint | accent | signal | ...
	Hex  string // #rrggbb
}

// Background is an optional image + bounded treatment. AssetID is the hex V2
// content id of a background asset (empty = tint-only).
type Background struct {
	AssetID    string // hex asset id, optional
	Tint       string // #rrggbb, optional
	Blur       uint64 // px, 0..64
	Dim        uint64 // milliunits 0..1000
	Saturation uint64 // milliunits 0..1000
	Grain      uint64 // milliunits 0..1000
	Vignette   uint64 // milliunits 0..1000
}

const (
	apKeyPalette = 1
	apKeyBg      = 2
	apKeyMotion  = 3
	apKeyDensity = 4
	apKeyNoise   = 5
)

const (
	bgKeyAssetID    = 1
	bgKeyTint       = 2
	bgKeyBlur       = 3
	bgKeyDim        = 4
	bgKeySaturation = 5
	bgKeyGrain      = 6
	bgKeyVignette   = 7
)

const (
	ptKeyName = 1
	ptKeyHex  = 2
)

// Encode serializes the appearance body to canonical CBOR.
func (a *Appearance) Encode() []byte {
	n := 0
	if len(a.Palette) > 0 {
		n++
	}
	if a.Background != nil {
		n++
	}
	if a.MotionPolicy != "" {
		n++
	}
	if a.Density != "" {
		n++
	}
	if a.Noise != 0 {
		n++
	}
	buf := codec.AppendMap(nil, n)
	if len(a.Palette) > 0 {
		buf = codec.AppendUint(buf, apKeyPalette)
		buf = codec.AppendArray(buf, len(a.Palette))
		for _, p := range a.Palette {
			buf = codec.AppendMap(buf, 2)
			buf = codec.AppendUint(buf, ptKeyName)
			buf = codec.AppendText(buf, p.Name)
			buf = codec.AppendUint(buf, ptKeyHex)
			buf = codec.AppendText(buf, p.Hex)
		}
	}
	if a.Background != nil {
		buf = codec.AppendUint(buf, apKeyBg)
		buf = a.Background.encode(buf)
	}
	if a.MotionPolicy != "" {
		buf = codec.AppendUint(buf, apKeyMotion)
		buf = codec.AppendText(buf, a.MotionPolicy)
	}
	if a.Density != "" {
		buf = codec.AppendUint(buf, apKeyDensity)
		buf = codec.AppendText(buf, a.Density)
	}
	if a.Noise != 0 {
		buf = codec.AppendUint(buf, apKeyNoise)
		buf = codec.AppendUint(buf, a.Noise)
	}
	return buf
}

func (b *Background) encode(buf []byte) []byte {
	n := 0
	for _, set := range []bool{b.AssetID != "", b.Tint != "", b.Blur != 0, b.Dim != 0, b.Saturation != 0, b.Grain != 0, b.Vignette != 0} {
		if set {
			n++
		}
	}
	buf = codec.AppendMap(buf, n)
	if b.AssetID != "" {
		buf = codec.AppendUint(buf, bgKeyAssetID)
		buf = codec.AppendText(buf, b.AssetID)
	}
	if b.Tint != "" {
		buf = codec.AppendUint(buf, bgKeyTint)
		buf = codec.AppendText(buf, b.Tint)
	}
	if b.Blur != 0 {
		buf = codec.AppendUint(buf, bgKeyBlur)
		buf = codec.AppendUint(buf, b.Blur)
	}
	if b.Dim != 0 {
		buf = codec.AppendUint(buf, bgKeyDim)
		buf = codec.AppendUint(buf, b.Dim)
	}
	if b.Saturation != 0 {
		buf = codec.AppendUint(buf, bgKeySaturation)
		buf = codec.AppendUint(buf, b.Saturation)
	}
	if b.Grain != 0 {
		buf = codec.AppendUint(buf, bgKeyGrain)
		buf = codec.AppendUint(buf, b.Grain)
	}
	if b.Vignette != 0 {
		buf = codec.AppendUint(buf, bgKeyVignette)
		buf = codec.AppendUint(buf, b.Vignette)
	}
	return buf
}

// DecodeAppearance parses an appearance body.
func DecodeAppearance(payload []byte) (*Appearance, error) {
	d := codec.NewDecoder(payload)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	a := &Appearance{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case apKeyPalette:
			cnt, e := d.ReadArray()
			if e != nil {
				return nil, e
			}
			for range cnt {
				pt, e := decodePaletteToken(d)
				if e != nil {
					return nil, e
				}
				a.Palette = append(a.Palette, pt)
			}
		case apKeyBg:
			bg, e := decodeBackground(d)
			if e != nil {
				return nil, e
			}
			a.Background = bg
		case apKeyMotion:
			a.MotionPolicy, er = d.ReadText()
		case apKeyDensity:
			a.Density, er = d.ReadText()
		case apKeyNoise:
			a.Noise, er = d.ReadUint()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	return a, nil
}

func decodePaletteToken(d *codec.Decoder) (PaletteToken, error) {
	mr, err := d.ReadMapHeader()
	if err != nil {
		return PaletteToken{}, err
	}
	var pt PaletteToken
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return pt, er
		}
		if !ok {
			break
		}
		switch k {
		case ptKeyName:
			pt.Name, er = d.ReadText()
		case ptKeyHex:
			pt.Hex, er = d.ReadText()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return pt, er
		}
	}
	return pt, nil
}

func decodeBackground(d *codec.Decoder) (*Background, error) {
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	b := &Background{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case bgKeyAssetID:
			b.AssetID, er = d.ReadText()
		case bgKeyTint:
			b.Tint, er = d.ReadText()
		case bgKeyBlur:
			b.Blur, er = d.ReadUint()
		case bgKeyDim:
			b.Dim, er = d.ReadUint()
		case bgKeySaturation:
			b.Saturation, er = d.ReadUint()
		case bgKeyGrain:
			b.Grain, er = d.ReadUint()
		case bgKeyVignette:
			b.Vignette, er = d.ReadUint()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	return b, nil
}

// ---- Composition ----

// Composition is the arrangement layer: zones and placed objects. Each object
// declares a renderer, a fallback renderer, and readable fallback metadata so
// meaning survives an unknown renderer.
type Composition struct {
	CoordinateSystem string
	Zones            []Zone
	Objects          []Object
	GraffitiPreview  string   // hex asset id, optional
	BundleIndexRef   *id.Hash // optional
}

type Zone struct {
	ID               string
	Kind             string // shelf | wall | pinned | ...
	Renderer         string
	FallbackRenderer string
}

type Object struct {
	ID               string
	SemanticKind     string // text | image | audio | video | file | link | note | device | bot
	SourceEventID    string // hex, optional
	SourceAssetID    string // hex asset id, optional
	ZoneID           string
	Renderer         string
	FallbackRenderer string
	FallbackTitle    string
	FallbackAuthor   string
	FallbackDetail   string
	Transform        Transform
}

// Transform is bounded integer placement (see CoordinateSystem).
type Transform struct {
	X, Y, W, H   uint64 // milliunits 0..1000
	RotationDeci int64  // deci-degrees -150..150
	Z            uint64 // 0..maxZ
}

const (
	coKeyCoord    = 1
	coKeyZones    = 2
	coKeyObjects  = 3
	coKeyGraffiti = 4
	coKeyBundle   = 5
)

const (
	znKeyID       = 1
	znKeyKind     = 2
	znKeyRenderer = 3
	znKeyFallback = 4
)

const (
	obKeyID        = 1
	obKeySemantic  = 2
	obKeyEventID   = 3
	obKeyAssetID   = 4
	obKeyZone      = 5
	obKeyRenderer  = 6
	obKeyFallback  = 7
	obKeyFbTitle   = 8
	obKeyFbAuthor  = 9
	obKeyFbDetail  = 10
	obKeyTransform = 11
)

const (
	trKeyX   = 1
	trKeyY   = 2
	trKeyW   = 3
	trKeyH   = 4
	trKeyRot = 5 // stored as RotationDeci + rotOffset
	trKeyZ   = 6
)

// Encode serializes the composition body to canonical CBOR.
func (c *Composition) Encode() []byte {
	n := 1 // coordinate system always present
	if len(c.Zones) > 0 {
		n++
	}
	if len(c.Objects) > 0 {
		n++
	}
	if c.GraffitiPreview != "" {
		n++
	}
	if c.BundleIndexRef != nil {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, coKeyCoord)
	cs := c.CoordinateSystem
	if cs == "" {
		cs = CoordinateSystem
	}
	buf = codec.AppendText(buf, cs)
	if len(c.Zones) > 0 {
		buf = codec.AppendUint(buf, coKeyZones)
		buf = codec.AppendArray(buf, len(c.Zones))
		for _, z := range c.Zones {
			buf = z.encode(buf)
		}
	}
	if len(c.Objects) > 0 {
		buf = codec.AppendUint(buf, coKeyObjects)
		buf = codec.AppendArray(buf, len(c.Objects))
		for _, o := range c.Objects {
			buf = o.encode(buf)
		}
	}
	if c.GraffitiPreview != "" {
		buf = codec.AppendUint(buf, coKeyGraffiti)
		buf = codec.AppendText(buf, c.GraffitiPreview)
	}
	if c.BundleIndexRef != nil {
		buf = codec.AppendUint(buf, coKeyBundle)
		buf = codec.AppendBytes(buf, c.BundleIndexRef[:])
	}
	return buf
}

func (z *Zone) encode(buf []byte) []byte {
	buf = codec.AppendMap(buf, 4)
	buf = codec.AppendUint(buf, znKeyID)
	buf = codec.AppendText(buf, z.ID)
	buf = codec.AppendUint(buf, znKeyKind)
	buf = codec.AppendText(buf, z.Kind)
	buf = codec.AppendUint(buf, znKeyRenderer)
	buf = codec.AppendText(buf, z.Renderer)
	buf = codec.AppendUint(buf, znKeyFallback)
	buf = codec.AppendText(buf, z.FallbackRenderer)
	return buf
}

func (o *Object) encode(buf []byte) []byte {
	fields := []struct {
		key uint64
		str string
	}{
		{obKeyID, o.ID}, {obKeySemantic, o.SemanticKind},
		{obKeyEventID, o.SourceEventID}, {obKeyAssetID, o.SourceAssetID},
		{obKeyZone, o.ZoneID}, {obKeyRenderer, o.Renderer},
		{obKeyFallback, o.FallbackRenderer}, {obKeyFbTitle, o.FallbackTitle},
		{obKeyFbAuthor, o.FallbackAuthor}, {obKeyFbDetail, o.FallbackDetail},
	}
	n := 1 // transform always present
	for _, f := range fields {
		if f.str != "" {
			n++
		}
	}
	buf = codec.AppendMap(buf, n)
	for _, f := range fields {
		if f.str != "" {
			buf = codec.AppendUint(buf, f.key)
			buf = codec.AppendText(buf, f.str)
		}
	}
	buf = codec.AppendUint(buf, obKeyTransform)
	buf = o.Transform.encode(buf)
	return buf
}

func (t *Transform) encode(buf []byte) []byte {
	buf = codec.AppendMap(buf, 6)
	buf = codec.AppendUint(buf, trKeyX)
	buf = codec.AppendUint(buf, t.X)
	buf = codec.AppendUint(buf, trKeyY)
	buf = codec.AppendUint(buf, t.Y)
	buf = codec.AppendUint(buf, trKeyW)
	buf = codec.AppendUint(buf, t.W)
	buf = codec.AppendUint(buf, trKeyH)
	buf = codec.AppendUint(buf, t.H)
	buf = codec.AppendUint(buf, trKeyRot)
	buf = codec.AppendUint(buf, uint64(t.RotationDeci+rotOffset))
	buf = codec.AppendUint(buf, trKeyZ)
	buf = codec.AppendUint(buf, t.Z)
	return buf
}

// DecodeComposition parses a composition body.
func DecodeComposition(payload []byte) (*Composition, error) {
	d := codec.NewDecoder(payload)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	c := &Composition{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case coKeyCoord:
			c.CoordinateSystem, er = d.ReadText()
		case coKeyZones:
			cnt, e := d.ReadArray()
			if e != nil {
				return nil, e
			}
			for range cnt {
				z, e := decodeZone(d)
				if e != nil {
					return nil, e
				}
				c.Zones = append(c.Zones, z)
			}
		case coKeyObjects:
			cnt, e := d.ReadArray()
			if e != nil {
				return nil, e
			}
			for range cnt {
				o, e := decodeObject(d)
				if e != nil {
					return nil, e
				}
				c.Objects = append(c.Objects, o)
			}
		case coKeyGraffiti:
			c.GraffitiPreview, er = d.ReadText()
		case coKeyBundle:
			var h id.Hash
			b, e := d.ReadBytes()
			if e != nil {
				return nil, e
			}
			if len(b) != 32 {
				return nil, errors.New("composition: bundle ref must be 32 bytes")
			}
			copy(h[:], b)
			c.BundleIndexRef = &h
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	return c, nil
}

func decodeZone(d *codec.Decoder) (Zone, error) {
	mr, err := d.ReadMapHeader()
	if err != nil {
		return Zone{}, err
	}
	var z Zone
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return z, er
		}
		if !ok {
			break
		}
		switch k {
		case znKeyID:
			z.ID, er = d.ReadText()
		case znKeyKind:
			z.Kind, er = d.ReadText()
		case znKeyRenderer:
			z.Renderer, er = d.ReadText()
		case znKeyFallback:
			z.FallbackRenderer, er = d.ReadText()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return z, er
		}
	}
	return z, nil
}

func decodeObject(d *codec.Decoder) (Object, error) {
	mr, err := d.ReadMapHeader()
	if err != nil {
		return Object{}, err
	}
	var o Object
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return o, er
		}
		if !ok {
			break
		}
		switch k {
		case obKeyID:
			o.ID, er = d.ReadText()
		case obKeySemantic:
			o.SemanticKind, er = d.ReadText()
		case obKeyEventID:
			o.SourceEventID, er = d.ReadText()
		case obKeyAssetID:
			o.SourceAssetID, er = d.ReadText()
		case obKeyZone:
			o.ZoneID, er = d.ReadText()
		case obKeyRenderer:
			o.Renderer, er = d.ReadText()
		case obKeyFallback:
			o.FallbackRenderer, er = d.ReadText()
		case obKeyFbTitle:
			o.FallbackTitle, er = d.ReadText()
		case obKeyFbAuthor:
			o.FallbackAuthor, er = d.ReadText()
		case obKeyFbDetail:
			o.FallbackDetail, er = d.ReadText()
		case obKeyTransform:
			o.Transform, er = decodeTransform(d)
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return o, er
		}
	}
	return o, nil
}

func decodeTransform(d *codec.Decoder) (Transform, error) {
	mr, err := d.ReadMapHeader()
	if err != nil {
		return Transform{}, err
	}
	var t Transform
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return t, er
		}
		if !ok {
			break
		}
		var u uint64
		switch k {
		case trKeyX:
			t.X, er = d.ReadUint()
		case trKeyY:
			t.Y, er = d.ReadUint()
		case trKeyW:
			t.W, er = d.ReadUint()
		case trKeyH:
			t.H, er = d.ReadUint()
		case trKeyRot:
			u, er = d.ReadUint()
			t.RotationDeci = int64(u) - rotOffset
		case trKeyZ:
			t.Z, er = d.ReadUint()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return t, er
		}
	}
	return t, nil
}

// validateTransform enforces the normalized bounds (contract-time).
func (t *Transform) validate() error {
	if t.X > maxMilli || t.Y > maxMilli || t.W > maxMilli || t.H > maxMilli {
		return errors.New("composition: transform coordinate out of [0,1]")
	}
	if t.X+t.W > maxMilli || t.Y+t.H > maxMilli {
		return errors.New("composition: object extends past the unit square")
	}
	if t.RotationDeci < -maxRotation || t.RotationDeci > maxRotation {
		return fmt.Errorf("composition: rotation %d deci-deg exceeds ±15°", t.RotationDeci)
	}
	if t.Z > maxZ {
		return fmt.Errorf("composition: z %d exceeds %d", t.Z, maxZ)
	}
	return nil
}
