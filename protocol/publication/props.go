package publication

import (
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

// Typed props per block type. Each type owns an integer wire-key table inside
// its raw props item; unknown types simply never get parsed here.

// TextProps covers heading/text/quote/callout/code/link-ish text carriers.
type TextProps struct {
	Text  string // key 1 — the primary text (heading text, paragraph, quote…)
	Extra string // key 2 — level/attribution/tone/lang/url per type semantics
	More  string // key 3 — title/description where a type needs a third field
}

// AssetProps covers image/audio/file/video-link.
type AssetProps struct {
	Asset   string // key 1 — hex asset id (or URL for video-link key 1=url)
	Text    string // key 2 — alt/filename/title
	Caption string // key 3 — caption/poster(hex for video-link)
}

// ListProps covers gallery (assets) and credits (role|name pairs flattened).
type ListProps struct {
	Items []string // key 1 — bounded list of text items
	Text  string   // key 2 — caption/section title
}

const (
	prKey1 = 1
	prKey2 = 2
	prKey3 = 3
)

func encodeThree(a, b, c string) []byte {
	n := 0
	if a != "" {
		n++
	}
	if b != "" {
		n++
	}
	if c != "" {
		n++
	}
	buf := codec.AppendMap(nil, n)
	if a != "" {
		buf = codec.AppendUint(buf, prKey1)
		buf = codec.AppendText(buf, a)
	}
	if b != "" {
		buf = codec.AppendUint(buf, prKey2)
		buf = codec.AppendText(buf, b)
	}
	if c != "" {
		buf = codec.AppendUint(buf, prKey3)
		buf = codec.AppendText(buf, c)
	}
	return buf
}

func decodeThree(raw []byte) (a, b, c string, err error) {
	if len(raw) == 0 {
		return "", "", "", nil
	}
	d := codec.NewDecoder(raw)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return "", "", "", err
	}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return "", "", "", er
		}
		if !ok {
			break
		}
		switch k {
		case prKey1:
			a, er = d.ReadText()
		case prKey2:
			b, er = d.ReadText()
		case prKey3:
			c, er = d.ReadText()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return "", "", "", er
		}
	}
	return a, b, c, d.Done()
}

// EncodeTextProps / ParseTextProps.
func EncodeTextProps(p TextProps) []byte { return encodeThree(p.Text, p.Extra, p.More) }
func ParseTextProps(raw []byte) (TextProps, error) {
	a, b, c, err := decodeThree(raw)
	return TextProps{Text: a, Extra: b, More: c}, err
}

// EncodeAssetProps / ParseAssetProps.
func EncodeAssetProps(p AssetProps) []byte { return encodeThree(p.Asset, p.Text, p.Caption) }
func ParseAssetProps(raw []byte) (AssetProps, error) {
	a, b, c, err := decodeThree(raw)
	return AssetProps{Asset: a, Text: b, Caption: c}, err
}

// EncodeListProps / ParseListProps.
func EncodeListProps(p ListProps) []byte {
	n := 0
	if len(p.Items) > 0 {
		n++
	}
	if p.Text != "" {
		n++
	}
	buf := codec.AppendMap(nil, n)
	if len(p.Items) > 0 {
		buf = codec.AppendUint(buf, prKey1)
		buf = codec.AppendArray(buf, len(p.Items))
		for _, it := range p.Items {
			buf = codec.AppendText(buf, it)
		}
	}
	if p.Text != "" {
		buf = codec.AppendUint(buf, prKey2)
		buf = codec.AppendText(buf, p.Text)
	}
	return buf
}

func ParseListProps(raw []byte) (ListProps, error) {
	var p ListProps
	if len(raw) == 0 {
		return p, nil
	}
	d := codec.NewDecoder(raw)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return p, err
	}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return p, er
		}
		if !ok {
			break
		}
		switch k {
		case prKey1:
			cnt, e := d.ReadArray()
			if e != nil {
				return p, e
			}
			if cnt > MaxGalleryAssets+MaxCredits {
				return p, errors.New("publication: props list too long")
			}
			for range cnt {
				s, e := d.ReadText()
				if e != nil {
					return p, e
				}
				p.Items = append(p.Items, s)
			}
		case prKey2:
			p.Text, er = d.ReadText()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return p, er
		}
	}
	return p, d.Done()
}

// propsShape names which codec a known type uses.
func propsShape(blockType string) string {
	switch blockType {
	case "heading", "text", "quote", "callout", "code", "link":
		return "text"
	case "image", "audio", "file", "video-link", "app":
		return "asset"
	case "gallery", "credits":
		return "list"
	case "separator", "stack", "columns", "hero", "section":
		return "none"
	default:
		return "opaque"
	}
}

// parseKnownProps parses a known block's props for validation.
func parseKnownProps(b *Block) (any, error) {
	switch propsShape(b.Type) {
	case "text":
		return ParseTextProps(b.RawProps)
	case "asset":
		return ParseAssetProps(b.RawProps)
	case "list":
		return ParseListProps(b.RawProps)
	case "none":
		if len(b.RawProps) > 0 {
			// Section may carry a title via text props; others must be bare.
			if b.Type == "section" || b.Type == "hero" {
				return ParseTextProps(b.RawProps)
			}
			return nil, fmt.Errorf("publication: %s block carries props", b.Type)
		}
		return nil, nil
	default:
		return nil, nil // opaque — not validated here
	}
}
