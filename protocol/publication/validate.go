package publication

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Validate is the AUTHORING gate (ADR-014): unknown types are rejected here —
// the decoder keeps them opaque, but nothing unknown may be *authored*.
// assetOK reports whether a hex asset id is resolvable in the target space;
// pass nil to skip that check (pure-protocol tests).
func Validate(doc *Document, assetOK func(hexID string) bool) error {
	if doc.DocumentID == ([16]byte{}) {
		return errors.New("publication: document id required")
	}
	if !allowedKinds[doc.Kind] {
		return fmt.Errorf("publication: kind %q not allowed", doc.Kind)
	}
	if err := checkText(doc.Title, MaxTitle, "title"); err != nil || doc.Title == "" {
		return errors.New("publication: title required and bounded")
	}
	if err := checkText(doc.Summary, MaxSummary, "summary"); err != nil {
		return err
	}
	if !allowedVisibility[doc.Visibility] {
		return fmt.Errorf("publication: visibility_intent %q not allowed", doc.Visibility)
	}
	if !allowedDiscussion[doc.Discussion] {
		return fmt.Errorf("publication: discussion mode %q not allowed", doc.Discussion)
	}
	if !allowedLayout[doc.Layout] {
		return fmt.Errorf("publication: layout %q not allowed", doc.Layout)
	}
	// Asset EXISTENCE is checked in one unified pass below (visitAssets) —
	// the same walk that enumerates the asset graph, so checking and
	// enumerating cannot diverge. Here and in the tree walk only structure
	// is judged.
	if len(doc.Tags) > MaxTags {
		return errors.New("publication: too many tags")
	}
	for _, tg := range doc.Tags {
		if err := checkText(tg, 40, "tag"); err != nil || tg == "" {
			return errors.New("publication: bad tag")
		}
	}
	for _, a := range doc.DisplayAuthors {
		if err := checkText(a, 80, "author"); err != nil {
			return err
		}
	}

	// Atmosphere bounds are its own business; its ASSETS resolve through
	// the unified pass below, exactly like a cover image — an atmosphere
	// asset that was not uploaded into this space is refused rather than
	// becoming a post that renders for its author and is blank for
	// everybody else.
	if err := doc.Atmosphere.Validate(nil); err != nil {
		return err
	}
	total := 0
	for _, e := range doc.RawExtra {
		total += len(e.Raw)
	}
	if total > MaxRawExtraBytes {
		return fmt.Errorf("publication: %d bytes of unknown fields, at most %d",
			total, MaxRawExtraBytes)
	}

	v := &treeValidator{seen: map[string]int{}}
	for i := range doc.Blocks {
		if err := v.walk(&doc.Blocks[i], 1); err != nil {
			return err
		}
	}
	if v.total > MaxBlocks {
		return errors.New("publication: too many blocks")
	}
	if v.textBudget > MaxTotalText {
		return errors.New("publication: total text budget exceeded")
	}
	// Stage anchors need the tree, so they are resolved here rather than in
	// Atmosphere.Validate — which runs above and is deliberately usable
	// without a document at all.
	if err := validateStageAnchors(doc, v.seen); err != nil {
		return err
	}
	// The unified asset pass: the ONE walk (visitAssets) both checks and
	// enumerates, so a site added there is validated here for free — and a
	// site forgotten there fails the enumeration consumers too, loudly.
	var assetErr error
	doc.visitAssets(func(hexID, role string) {
		if assetErr == nil {
			assetErr = checkAssetRef(hexID, assetOK, role)
		}
	})
	if assetErr != nil {
		return assetErr
	}
	if len(doc.Encode()) > MaxSerializedSize {
		return errors.New("publication: serialized document too large")
	}
	return nil
}

// validateStageAnchors resolves an image sequence against the document it
// belongs to: every anchor names a block that exists, and the stages run in
// document order.
//
// Order is part of the contract rather than a renderer's problem. A sequence
// is read top to bottom, so stages out of document order have no meaning a
// renderer could act on — it would have to sort them and hope, or advance
// backwards. Deciding it here makes the renderer a single forward cursor.
//
// The decoder still accepts anything, exactly like an unknown block type: the
// refusal is the authoring gate's, never transport's (ADR-014).
func validateStageAnchors(doc *Document, order map[string]int) error {
	if doc.Atmosphere == nil {
		return nil
	}
	last := -1
	for _, s := range doc.Atmosphere.Visual.Stages {
		at, ok := order[s.Anchor]
		if !ok {
			return fmt.Errorf("publication: stage anchored to block %q, which "+
				"is not in this document", s.Anchor)
		}
		if at <= last {
			return fmt.Errorf("publication: stage anchored to block %q is out "+
				"of document order", s.Anchor)
		}
		last = at
	}
	return nil
}

type treeValidator struct {
	// seen maps a block id to its position in document order — position,
	// not merely presence, because stage anchors have to be checked against
	// the order the reader will meet them in.
	seen       map[string]int
	total      int
	textBudget int
}

func (v *treeValidator) walk(b *Block, depth int) error {
	if depth > MaxDepth {
		return errors.New("publication: block tree too deep")
	}
	v.total++
	if _, dup := v.seen[b.ID]; b.ID == "" || len(b.ID) > 64 || dup {
		return fmt.Errorf("publication: block id missing, oversized or duplicate (%q)", b.ID)
	}
	v.seen[b.ID] = v.total
	if !KnownBlockType(b.Type) {
		return fmt.Errorf("publication: unknown block type %q (authoring rejects; transport keeps opaque)", b.Type)
	}
	// Children only under composition types.
	if len(b.Children) > 0 && !compositionTypes[b.Type] {
		return fmt.Errorf("publication: %s block cannot have children", b.Type)
	}
	if len(b.Children) > MaxChildren {
		return errors.New("publication: too many children")
	}
	if err := v.props(b); err != nil {
		return err
	}
	for i := range b.Children {
		if err := v.walk(&b.Children[i], depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (v *treeValidator) props(b *Block) error {
	parsed, err := parseKnownProps(b)
	if err != nil {
		return err
	}
	switch p := parsed.(type) {
	case TextProps:
		for _, s := range []string{p.Text, p.Extra, p.More} {
			if err := checkText(s, MaxTextField, b.Type); err != nil {
				return err
			}
			v.textBudget += len(s)
		}
		switch b.Type {
		case "heading", "text", "quote", "code", "callout":
			if p.Text == "" {
				return fmt.Errorf("publication: %s block needs text", b.Type)
			}
		case "link":
			if err := checkURL(p.Text); err != nil {
				return err
			}
		}
	case AssetProps:
		v.textBudget += len(p.Text) + len(p.Caption)
		if err := checkText(p.Text, MaxTextField, b.Type); err != nil {
			return err
		}
		if err := checkText(p.Caption, MaxTextField, b.Type); err != nil {
			return err
		}
		switch b.Type {
		case "video-link":
			if err := checkURL(p.Asset); err != nil { // key 1 is the URL here
				return err
			}
			// The key-3 poster's existence rides the unified pass.
		case "app":
			if err := checkHex(p.Asset, "app instance"); err != nil { // key 1 = instance id
				return err
			}
		default: // image, audio, video, file
			if p.Asset == "" {
				return fmt.Errorf("publication: %s block needs an asset", b.Type)
			}
			// Existence rides the unified pass; presence is structural.
		}
	case ListProps:
		v.textBudget += len(p.Text)
		switch b.Type {
		case "gallery":
			if len(p.Items) == 0 || len(p.Items) > MaxGalleryAssets {
				return errors.New("publication: gallery needs 1..24 assets")
			}
			// Item existence rides the unified pass.
		case "credits":
			if len(p.Items) == 0 || len(p.Items) > MaxCredits*2 || len(p.Items)%2 != 0 {
				return errors.New("publication: credits must be role/name pairs")
			}
			for _, it := range p.Items {
				if err := checkText(it, 120, "credit"); err != nil {
					return err
				}
				v.textBudget += len(it)
			}
		}
	}
	return nil
}

func checkText(s string, max int, what string) error {
	if len(s) > max {
		return fmt.Errorf("publication: %s too long", what)
	}
	for _, r := range s {
		if r < 0x20 && r != '\t' && r != '\n' {
			return fmt.Errorf("publication: %s contains control characters", what)
		}
	}
	return nil
}

func checkHex(s, what string) error {
	if s == "" {
		return fmt.Errorf("publication: %s required", what)
	}
	b, err := hex.DecodeString(s)
	if err != nil || (len(b) != 16 && len(b) != 32) {
		return fmt.Errorf("publication: %s is not a valid id", what)
	}
	return nil
}

func checkAssetRef(s string, assetOK func(string) bool, what string) error {
	if s == "" {
		return nil
	}
	if err := checkHex(s, what); err != nil {
		return err
	}
	if assetOK != nil && !assetOK(s) {
		return fmt.Errorf("publication: %s asset is not published in this space", what)
	}
	return nil
}

func checkURL(s string) error {
	if strings.ContainsAny(s, "\n\r\t ") || len(s) > 2048 {
		return errors.New("publication: malformed URL")
	}
	// PA-1: "qs:<token>" is a Quiet Spaces share link — an opaque bearer
	// token, not a host-based URL. It lets a catalog space-card reference
	// another space in a plain link block.
	if strings.HasPrefix(s, "qs:") {
		if len(s) <= len("qs:") {
			return errors.New("publication: empty space link")
		}
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.New("publication: link must be an http(s) URL")
	}
	return nil
}
