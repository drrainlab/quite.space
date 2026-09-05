package schemas

// SKY (SK-1) — a shared drawing, reimagined for an append-only world.
//
// There is no canvas object that everybody edits. A sky is a message
// (block.sky.v1 — an ordinary feed entry with a fallback, so a client
// that has never heard of skies still shows "a shared sky"), and every
// stroke is its own tiny signed event (sky.stroke.v1) pointing at it.
// Strokes commute: the set is the picture, order only matters for
// playback, and the log — not a CRDT library — is the merge. An erase is
// a stroke event naming the strokes it removes; the reducer honours it
// only for the erasing person's own strokes (authorship is sovereignty).
//
// Coordinates are quantised to a square grid so a stroke is a handful
// of bytes: two per point, at most MaxStrokePoints of them.

import (
	"errors"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

const (
	BlockSky  = "block.sky.v1"
	SkyStroke = "sky.stroke.v1"

	// SkyGrid is the fixed quantisation: 0..127 on both axes. One byte per
	// coordinate, and fine enough for a sketch on a phone.
	SkyGrid = 128
	// MaxStrokePoints bounds one stroke; a longer gesture is split by the
	// client. MaxEraseTargets bounds one erase.
	MaxStrokePoints = 256
	MaxEraseTargets = 32
	// SkyBrightMax: three levels of glow, never a colour picker — the
	// colour is the person (derived from their principal on every client).
	SkyBrightMax = 3
)

// SkyBlock is the sky message itself.
type SkyBlock struct {
	Title string
}

func (b *SkyBlock) Fallback() string {
	if b.Title != "" {
		return clip("✦ "+b.Title, MaxFallbackLen)
	}
	return "✦ a shared sky"
}

func (b *SkyBlock) Encode() ([]byte, error) {
	if len(b.Title) > MaxTitleLen {
		return nil, errors.New("schemas: sky title too long")
	}
	n := 1
	if b.Title != "" {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, b.Fallback())
	if b.Title != "" {
		buf = codec.AppendUint(buf, 2)
		buf = codec.AppendText(buf, b.Title)
	}
	return finishBlock(buf)
}

func DecodeSkyBlock(p []byte) (*SkyBlock, error) {
	b := &SkyBlock{}
	err := walkBlock(p, func(k uint64, d *codec.Decoder) (er error) {
		switch k {
		case 2:
			b.Title, er = d.ReadText()
		default:
			er = d.SkipItem()
		}
		return er
	})
	if err != nil {
		return nil, err
	}
	if len(b.Title) > MaxTitleLen {
		return nil, errors.New("schemas: sky title too long")
	}
	return b, nil
}

// SkyStrokeEvent is one gesture — or one erase — on a sky.
type SkyStrokeEvent struct {
	Sky    id.EventID
	Points []byte // x0,y0,x1,y1,… each 0..SkyGrid-1; empty for an erase
	Bright uint8  // 1..SkyBrightMax; 0 reads as 2
	Erase  []id.EventID
}

func (s *SkyStrokeEvent) IsErase() bool { return len(s.Erase) > 0 }

func (s *SkyStrokeEvent) Encode() ([]byte, error) {
	if len(s.Points)%2 != 0 || len(s.Points)/2 > MaxStrokePoints {
		return nil, errors.New("schemas: stroke points malformed or too many")
	}
	if len(s.Erase) > MaxEraseTargets {
		return nil, errors.New("schemas: too many erase targets")
	}
	if len(s.Points) == 0 && len(s.Erase) == 0 {
		return nil, errors.New("schemas: a stroke draws or erases")
	}
	for _, v := range s.Points {
		if v >= SkyGrid {
			return nil, errors.New("schemas: stroke point off the grid")
		}
	}
	if s.Bright > SkyBrightMax {
		return nil, errors.New("schemas: stroke brightness out of range")
	}
	n := 1
	if len(s.Points) > 0 {
		n++
	}
	if s.Bright > 0 {
		n++
	}
	if len(s.Erase) > 0 {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendBytes(buf, s.Sky[:])
	if len(s.Points) > 0 {
		buf = codec.AppendUint(buf, 2)
		buf = codec.AppendBytes(buf, s.Points)
	}
	if s.Bright > 0 {
		buf = codec.AppendUint(buf, 3)
		buf = codec.AppendUint(buf, uint64(s.Bright))
	}
	if len(s.Erase) > 0 {
		buf = codec.AppendUint(buf, 4)
		buf = codec.AppendArray(buf, len(s.Erase))
		for _, e := range s.Erase {
			buf = codec.AppendBytes(buf, e[:])
		}
	}
	return buf, nil
}

func DecodeSkyStroke(p []byte) (*SkyStrokeEvent, error) {
	bad := errors.New("schemas: malformed sky stroke")
	s := &SkyStrokeEvent{}
	d := codec.NewDecoder(p)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, bad
	}
	seenSky := false
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, bad
		}
		if !ok {
			break
		}
		switch k {
		case 1:
			raw, er := d.ReadBytes()
			if er != nil || len(raw) != len(s.Sky) {
				return nil, bad
			}
			copy(s.Sky[:], raw)
			seenSky = true
		case 2:
			raw, er := d.ReadBytes()
			if er != nil {
				return nil, bad
			}
			s.Points = append([]byte(nil), raw...)
		case 3:
			v, er := d.ReadUint()
			if er != nil || v > SkyBrightMax {
				return nil, bad
			}
			s.Bright = uint8(v)
		case 4:
			n, er := d.ReadArray()
			if er != nil || n > MaxEraseTargets {
				return nil, bad
			}
			for i := 0; i < n; i++ {
				raw, er := d.ReadBytes()
				if er != nil || len(raw) != len(id.EventID{}) {
					return nil, bad
				}
				var e id.EventID
				copy(e[:], raw)
				s.Erase = append(s.Erase, e)
			}
		default:
			if er := d.SkipItem(); er != nil {
				return nil, bad
			}
		}
	}
	if !seenSky || len(s.Points)%2 != 0 || len(s.Points)/2 > MaxStrokePoints {
		return nil, bad
	}
	if len(s.Points) == 0 && len(s.Erase) == 0 {
		return nil, bad
	}
	for _, v := range s.Points {
		if v >= SkyGrid {
			return nil, bad
		}
	}
	return s, nil
}

func init() {
	Register(BlockSky, func(p []byte) error { _, err := DecodeSkyBlock(p); return err })
	Register(SkyStroke, func(p []byte) error { _, err := DecodeSkyStroke(p); return err })
}
