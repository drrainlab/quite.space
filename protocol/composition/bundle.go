package composition

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"sort"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// Bundle kinds — the delivery role of a content-addressed asset set.
const (
	BundleCore        = "core"
	BundleViewport    = "viewport"
	BundleInteraction = "interaction"
	BundleBackground  = "background"
	BundleEditor      = "editor"
	BundleArchive     = "archive"
)

var bundleKinds = map[string]bool{
	BundleCore: true, BundleViewport: true, BundleInteraction: true,
	BundleBackground: true, BundleEditor: true, BundleArchive: true,
}

// Bundle is an IMMUTABLE, content-addressed manifest of assets (ADR-013). It
// is NOT self-signed: integrity comes from its id, authenticity from the
// signed bundle index that references it. New content ⇒ new id.
type Bundle struct {
	Kind           string
	Priority       uint64
	EstimatedBytes uint64
	Dependencies   []id.Hash // ids of bundles that must arrive first
	Entries        []BundleEntry
}

// BundleEntry names one asset variant to deliver. EncryptionEpoch is per entry
// (an archive/editor bundle may span epochs).
type BundleEntry struct {
	AssetID         string // hex content id (V2)
	Variant         string // original | preview | thumbnail | lqip
	Required        bool
	EncryptionEpoch uint64
}

const (
	buKeyKind     = 1
	buKeyPriority = 2
	buKeyEstBytes = 3
	buKeyDeps     = 4
	buKeyEntries  = 5
	beKeyAssetID  = 1
	beKeyVariant  = 2
	beKeyRequired = 3
	beKeyEpoch    = 4
)

// canonicalBody encodes the immutable body with entries and dependencies in a
// canonical order, so identical content always yields the same id. It excludes
// any id, signature, transport hint, or cache metadata (there are none here).
func (b *Bundle) canonicalBody() []byte {
	entries := append([]BundleEntry(nil), b.Entries...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].AssetID != entries[j].AssetID {
			return entries[i].AssetID < entries[j].AssetID
		}
		return entries[i].Variant < entries[j].Variant
	})
	deps := append([]id.Hash(nil), b.Dependencies...)
	sort.Slice(deps, func(i, j int) bool { return bytes.Compare(deps[i][:], deps[j][:]) < 0 })

	n := 3 // kind, priority, est_bytes
	if len(deps) > 0 {
		n++
	}
	if len(entries) > 0 {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, buKeyKind)
	buf = codec.AppendText(buf, b.Kind)
	buf = codec.AppendUint(buf, buKeyPriority)
	buf = codec.AppendUint(buf, b.Priority)
	buf = codec.AppendUint(buf, buKeyEstBytes)
	buf = codec.AppendUint(buf, b.EstimatedBytes)
	if len(deps) > 0 {
		buf = codec.AppendUint(buf, buKeyDeps)
		buf = codec.AppendArray(buf, len(deps))
		for _, d := range deps {
			buf = codec.AppendBytes(buf, d[:])
		}
	}
	if len(entries) > 0 {
		buf = codec.AppendUint(buf, buKeyEntries)
		buf = codec.AppendArray(buf, len(entries))
		for _, e := range entries {
			buf = codec.AppendMap(buf, 4)
			buf = codec.AppendUint(buf, beKeyAssetID)
			buf = codec.AppendText(buf, e.AssetID)
			buf = codec.AppendUint(buf, beKeyVariant)
			buf = codec.AppendText(buf, e.Variant)
			buf = codec.AppendUint(buf, beKeyRequired)
			buf = codec.AppendBool(buf, e.Required)
			buf = codec.AppendUint(buf, beKeyEpoch)
			buf = codec.AppendUint(buf, e.EncryptionEpoch)
		}
	}
	return buf
}

// Encode returns the canonical immutable body (also what is hashed for the id).
func (b *Bundle) Encode() []byte { return b.canonicalBody() }

// ID is the content address: SHA256("qs.bundle.v1" ‖ canonical body).
func (b *Bundle) ID() id.Hash {
	h := sha256.New()
	h.Write([]byte("qs.bundle.v1"))
	h.Write(b.canonicalBody())
	var out id.Hash
	h.Sum(out[:0])
	return out
}

// DecodeBundle parses an immutable bundle body.
func DecodeBundle(body []byte) (*Bundle, error) {
	d := codec.NewDecoder(body)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	b := &Bundle{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case buKeyKind:
			b.Kind, er = d.ReadText()
		case buKeyPriority:
			b.Priority, er = d.ReadUint()
		case buKeyEstBytes:
			b.EstimatedBytes, er = d.ReadUint()
		case buKeyDeps:
			cnt, e := d.ReadArray()
			if e != nil {
				return nil, e
			}
			for range cnt {
				var h id.Hash
				bs, e := d.ReadBytes()
				if e != nil {
					return nil, e
				}
				if len(bs) != 32 {
					return nil, errors.New("composition: bundle dep must be 32 bytes")
				}
				copy(h[:], bs)
				b.Dependencies = append(b.Dependencies, h)
			}
		case buKeyEntries:
			cnt, e := d.ReadArray()
			if e != nil {
				return nil, e
			}
			for range cnt {
				be, e := decodeBundleEntry(d)
				if e != nil {
					return nil, e
				}
				b.Entries = append(b.Entries, be)
			}
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
	return b, nil
}

func decodeBundleEntry(d *codec.Decoder) (BundleEntry, error) {
	mr, err := d.ReadMapHeader()
	if err != nil {
		return BundleEntry{}, err
	}
	var e BundleEntry
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return e, er
		}
		if !ok {
			break
		}
		switch k {
		case beKeyAssetID:
			e.AssetID, er = d.ReadText()
		case beKeyVariant:
			e.Variant, er = d.ReadText()
		case beKeyRequired:
			e.Required, er = d.ReadBool()
		case beKeyEpoch:
			e.EncryptionEpoch, er = d.ReadUint()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return e, er
		}
	}
	return e, nil
}

// ---- Bundle index (the signed authenticity layer) ----

// BundleIndex is the payload of a space.bundle.index snapshot: the current set
// of bundle ids for the space, bound to it by the snapshot's signature.
type BundleIndex struct {
	Bundles []BundleRef
}

// BundleRef names one bundle in the index (its content id + delivery hints).
type BundleRef struct {
	BundleID       id.Hash
	Kind           string
	Priority       uint64
	EstimatedBytes uint64
}

const (
	biKeyBundles = 1
	brKeyID      = 1
	brKeyKind    = 2
	brKeyPrio    = 3
	brKeyEst     = 4
)

// Encode serializes the bundle-index body.
func (bi *BundleIndex) Encode() []byte {
	buf := codec.AppendMap(nil, 1)
	buf = codec.AppendUint(buf, biKeyBundles)
	buf = codec.AppendArray(buf, len(bi.Bundles))
	for _, r := range bi.Bundles {
		buf = codec.AppendMap(buf, 4)
		buf = codec.AppendUint(buf, brKeyID)
		buf = codec.AppendBytes(buf, r.BundleID[:])
		buf = codec.AppendUint(buf, brKeyKind)
		buf = codec.AppendText(buf, r.Kind)
		buf = codec.AppendUint(buf, brKeyPrio)
		buf = codec.AppendUint(buf, r.Priority)
		buf = codec.AppendUint(buf, brKeyEst)
		buf = codec.AppendUint(buf, r.EstimatedBytes)
	}
	return buf
}

// DecodeBundleIndex parses a bundle-index body.
func DecodeBundleIndex(payload []byte) (*BundleIndex, error) {
	d := codec.NewDecoder(payload)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	bi := &BundleIndex{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case biKeyBundles:
			cnt, e := d.ReadArray()
			if e != nil {
				return nil, e
			}
			for range cnt {
				r, e := decodeBundleRef(d)
				if e != nil {
					return nil, e
				}
				bi.Bundles = append(bi.Bundles, r)
			}
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
	return bi, nil
}

func decodeBundleRef(d *codec.Decoder) (BundleRef, error) {
	mr, err := d.ReadMapHeader()
	if err != nil {
		return BundleRef{}, err
	}
	var r BundleRef
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return r, er
		}
		if !ok {
			break
		}
		switch k {
		case brKeyID:
			bs, e := d.ReadBytes()
			if e != nil {
				return r, e
			}
			if len(bs) != 32 {
				return r, errors.New("composition: bundle id must be 32 bytes")
			}
			copy(r.BundleID[:], bs)
		case brKeyKind:
			r.Kind, er = d.ReadText()
		case brKeyPrio:
			r.Priority, er = d.ReadUint()
		case brKeyEst:
			r.EstimatedBytes, er = d.ReadUint()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return r, er
		}
	}
	return r, nil
}
