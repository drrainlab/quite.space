// Package projection is the PA-0 public-projection wire contract: a signed,
// bounded, independently-installable read model of a public space.
//
// FormatVersion 1 encodes the checkpoint state as a DEPENDENCY-CLOSED
// ORDERED FRAME SELECTION rather than a struct-serialized state image. The
// frames are already the protocol's canonical, individually-signed wire
// form, and the reducers materialize identical state from the same event
// set regardless of order (kernel/reducers contract) — so a fresh reader
// installs the selection into a projection store WITHOUT chain-contiguity
// requirements and lands on exactly the state the publisher projected
// (I6/I9 by construction). CutPoints record where each author chain's full
// history resumes, for future delta/incremental use. A later FormatVersion
// may introduce a struct IR when sub-frame pruning becomes necessary.
//
// The envelope is signed by the SPACE TERMINAL KEY (whose public key IS the
// space id, ADR-001): one signature binds the sequence number, truncation
// metadata, the manifest, and the complete ordered contents (I7) — a blind
// relay or mailbox squatter cannot forge the sequence, hide truncation, or
// remix frames.
package projection

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// FormatVersion gates the checkpoint wire contract (NOT any internal
// struct layout).
//
// v2 (PH-2) adds IngressHints. Note that this envelope is NOT
// must-ignore-safe like the rest of the protocol: Verify reconstructs the
// signed body by RE-ENCODING the decoded struct, so a v1 reader handed a v2
// envelope would drop the unknown key, re-encode a shorter body, and report
// "invalid space signature" — a security-shaped error for what is only a
// version skew. The version check below turns that into an honest sentence
// instead. Adding a signed field therefore always costs a version bump.
const FormatVersion = 2

// magic distinguishes projection envelopes from bundles and CBOR frames.
const magic = "QPP1"

// CutPoint records where one author chain's complete history resumes: the
// projection carries this chain's frames from AfterSeq+1 onward (or none);
// everything at or before AfterSeq was pruned by the bounds.
type CutPoint struct {
	Device   id.DeviceID
	AfterSeq uint64
	Tip      id.EventID // event id at AfterSeq — the resume anchor
}

// Envelope is the decoded public projection.
type Envelope struct {
	SpaceID         id.TerminalID
	Seq             uint64 // monotonic per publisher; content-change driven
	Format          uint16
	PublishedAt     uint64
	Truncated       bool
	OldestTime      uint64 // CreatedAt of the oldest retained feed entry
	PublisherDevice id.DeviceID
	ManifestFrame   []byte
	CutPoints       []CutPoint
	Frames          [][]byte // dependency-closed, deterministic order
	AssetManifests  [][]byte // asset manifest blobs (media bytes NEVER ride)
	// IngressHints (v2) are the mailbox addresses contributors and readers
	// push to — 8 shards across the current and next bucket. Publishing them
	// is what lets the owner keep the DRAIN capability private (PH-1): the
	// address is public, the key to empty it is not. They are inside the
	// signed body and the content digest on purpose — an uncommitted hint is
	// a squatter's invitation to redirect a space's contributions.
	IngressHints [][]byte
	Signature    []byte
}

// Envelope key table v1 (append-only, ADR-009).
const (
	keySpace     = 1
	keySeq       = 2
	keyFormat    = 3
	keyPublished = 4
	keyTruncated = 5
	keyOldest    = 6
	keyPubDevice = 7
	keyManifest  = 8
	keyCuts      = 9
	keyFrames    = 10
	keyAssets    = 11
	keySignature = 12
	keyIngress   = 13 // v2
)

func (e *Envelope) encodeBody(withSig bool) []byte {
	n := 10
	if len(e.AssetManifests) > 0 {
		n++
	}
	if len(e.IngressHints) > 0 {
		n++
	}
	if withSig {
		n++
	}
	buf := []byte(magic)
	buf = codec.AppendMap(buf, n)
	buf = codec.AppendUint(buf, keySpace)
	buf = codec.AppendBytes(buf, e.SpaceID[:])
	buf = codec.AppendUint(buf, keySeq)
	buf = codec.AppendUint(buf, e.Seq)
	buf = codec.AppendUint(buf, keyFormat)
	buf = codec.AppendUint(buf, uint64(e.Format))
	buf = codec.AppendUint(buf, keyPublished)
	buf = codec.AppendUint(buf, e.PublishedAt)
	buf = codec.AppendUint(buf, keyTruncated)
	buf = codec.AppendBool(buf, e.Truncated)
	buf = codec.AppendUint(buf, keyOldest)
	buf = codec.AppendUint(buf, e.OldestTime)
	buf = codec.AppendUint(buf, keyPubDevice)
	buf = codec.AppendBytes(buf, e.PublisherDevice[:])
	buf = codec.AppendUint(buf, keyManifest)
	buf = codec.AppendBytes(buf, e.ManifestFrame)
	buf = codec.AppendUint(buf, keyCuts)
	buf = codec.AppendArray(buf, len(e.CutPoints))
	for _, c := range e.CutPoints {
		buf = codec.AppendArray(buf, 3)
		buf = codec.AppendBytes(buf, c.Device[:])
		buf = codec.AppendUint(buf, c.AfterSeq)
		buf = codec.AppendBytes(buf, c.Tip[:])
	}
	buf = codec.AppendUint(buf, keyFrames)
	buf = codec.AppendArray(buf, len(e.Frames))
	for _, f := range e.Frames {
		buf = codec.AppendBytes(buf, f)
	}
	if len(e.AssetManifests) > 0 {
		buf = codec.AppendUint(buf, keyAssets)
		buf = codec.AppendArray(buf, len(e.AssetManifests))
		for _, a := range e.AssetManifests {
			buf = codec.AppendBytes(buf, a)
		}
	}
	if withSig {
		buf = codec.AppendUint(buf, keySignature)
		buf = codec.AppendBytes(buf, e.Signature)
	}
	// Key 13 comes after key 12: canonical CBOR demands strictly ascending
	// map keys, and the decoder enforces it.
	if len(e.IngressHints) > 0 {
		buf = codec.AppendUint(buf, keyIngress)
		buf = codec.AppendArray(buf, len(e.IngressHints))
		for _, h := range e.IngressHints {
			buf = codec.AppendBytes(buf, h)
		}
	}
	return buf
}

// Sign signs the envelope with the space terminal key and returns the wire
// bytes. The key must match the space id (ADR-001).
func (e *Envelope) Sign(spacePriv ed25519.PrivateKey) ([]byte, error) {
	pub := spacePriv.Public().(ed25519.PublicKey)
	if !bytes.Equal(pub, e.SpaceID[:]) {
		return nil, errors.New("projection: signing key does not match space id")
	}
	e.Format = FormatVersion
	e.Signature = ed25519.Sign(spacePriv, e.encodeBody(false))
	return e.encodeBody(true), nil
}

// ContentDigest identifies the projection CONTENT (manifest + ordered frame
// ids + asset manifest ids + truncation metadata) independent of Seq and
// PublishedAt: the publisher bumps Seq exactly when this digest changes,
// and republishes the same Seq on heartbeats.
func (e *Envelope) ContentDigest() [32]byte {
	h := sha256.New()
	h.Write([]byte("qp-projection-content-v1:"))
	h.Write(e.ManifestFrame)
	if e.Truncated {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
	for _, f := range e.Frames {
		eid := id.EventIDOf(f)
		h.Write(eid[:])
	}
	for _, a := range e.AssetManifests {
		d := sha256.Sum256(a)
		h.Write(d[:])
	}
	// Bound by the digest, so a rotated ingress address bumps Seq — twice a
	// day with no new content, which is correct and cheap. A hint that could
	// change without changing the digest would be a hint readers could not
	// trust.
	for _, hint := range e.IngressHints {
		h.Write(hint)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Decode parses and STRUCTURALLY validates an envelope (no signature check
// — call Verify with the wire bytes next; nothing may be trusted before
// both succeed).
func Decode(data []byte) (*Envelope, error) {
	if len(data) < len(magic) || string(data[:len(magic)]) != magic {
		return nil, errors.New("projection: not a projection envelope")
	}
	d := codec.NewDecoder(data[len(magic):])
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	e := &Envelope{}
	for {
		k, ok, er := m.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case keySpace:
			b, e2 := d.ReadBytes()
			if e2 != nil {
				return nil, e2
			}
			if len(b) != id.Size {
				return nil, errors.New("projection: bad space id")
			}
			copy(e.SpaceID[:], b)
		case keySeq:
			e.Seq, er = d.ReadUint()
		case keyFormat:
			var v uint64
			v, er = d.ReadUint()
			e.Format = uint16(v)
		case keyPublished:
			e.PublishedAt, er = d.ReadUint()
		case keyTruncated:
			e.Truncated, er = d.ReadBool()
		case keyOldest:
			e.OldestTime, er = d.ReadUint()
		case keyPubDevice:
			b, e2 := d.ReadBytes()
			if e2 != nil {
				return nil, e2
			}
			if len(b) != id.Size {
				return nil, errors.New("projection: bad publisher device")
			}
			copy(e.PublisherDevice[:], b)
		case keyManifest:
			var b []byte
			b, er = d.ReadBytes()
			e.ManifestFrame = append([]byte(nil), b...)
		case keyCuts:
			cnt, e2 := d.ReadArray()
			if e2 != nil {
				return nil, e2
			}
			for range cnt {
				if _, er = d.ReadArray(); er != nil {
					return nil, er
				}
				var c CutPoint
				db, e3 := d.ReadBytes()
				if e3 != nil {
					return nil, e3
				}
				copy(c.Device[:], db)
				if c.AfterSeq, er = d.ReadUint(); er != nil {
					return nil, er
				}
				tb, e4 := d.ReadBytes()
				if e4 != nil {
					return nil, e4
				}
				copy(c.Tip[:], tb)
				e.CutPoints = append(e.CutPoints, c)
			}
		case keyFrames:
			cnt, e2 := d.ReadArray()
			if e2 != nil {
				return nil, e2
			}
			for range cnt {
				f, e3 := d.ReadBytes()
				if e3 != nil {
					return nil, e3
				}
				e.Frames = append(e.Frames, append([]byte(nil), f...))
			}
		case keyAssets:
			cnt, e2 := d.ReadArray()
			if e2 != nil {
				return nil, e2
			}
			for range cnt {
				a, e3 := d.ReadBytes()
				if e3 != nil {
					return nil, e3
				}
				e.AssetManifests = append(e.AssetManifests, append([]byte(nil), a...))
			}
		case keyIngress:
			cnt, e2 := d.ReadArray()
			if e2 != nil {
				return nil, e2
			}
			for range cnt {
				hint, e3 := d.ReadBytes()
				if e3 != nil {
					return nil, e3
				}
				e.IngressHints = append(e.IngressHints, append([]byte(nil), hint...))
			}
		case keySignature:
			var b []byte
			b, er = d.ReadBytes()
			e.Signature = append([]byte(nil), b...)
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
	if e.Format != FormatVersion {
		return nil, errors.New("projection: unsupported format version")
	}
	if len(e.Signature) == 0 {
		return nil, errors.New("projection: unsigned envelope")
	}
	return e, nil
}

// Verify checks the space-key signature over the envelope body. The public
// key IS the space id — verification needs no external trust material (I7).
func Verify(e *Envelope) error {
	sig := e.Signature
	e.Signature = nil
	body := e.encodeBody(false)
	e.Signature = sig
	if !ed25519.Verify(ed25519.PublicKey(e.SpaceID[:]), body, sig) {
		return errors.New("projection: invalid space signature")
	}
	return nil
}
