// Package composition defines the Space Composition Contract (ADR-013): the
// signed, portable declarative documents a space hands a visiting client so it
// can render meaning + arrangement with its own trusted renderers — never
// executable UI.
//
// A Snapshot is the common signed envelope for the three document kinds
// (appearance, composition, bundle_index). It follows the Terminal Manifest
// pattern: canonical CBOR (ADR-003), an append-only integer wire-key table, a
// Revision/PreviousHash chain, and a terminal-key signature where the space's
// terminal id IS the verifying public key. Coordinates and treatments are
// encoded as bounded integers because deterministic CBOR carries no floats.
package composition

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// Document kinds (the value bound into a chain; immutable within one chain).
const (
	KindAppearance  = "appearance"
	KindComposition = "composition"
	KindBundleIndex = "bundle_index"
)

// Schema ids registered with protocol/schemas (see register.go). The registry
// grammar is <domain>.<type>.v<N>; these are the wire ids for the documents the
// plan calls space.appearance.snapshot / space.composition.snapshot /
// space.bundle.index.snapshot.
const (
	SchemaAppearance  = "appearance.snapshot.v1"
	SchemaComposition = "composition.snapshot.v1"
	SchemaBundleIndex = "bundle.index.v1"
)

// Snapshot is the signed envelope shared by every document kind.
type Snapshot struct {
	SpaceID          id.TerminalID // == the verifying public key
	DocumentKind     string
	Revision         uint64
	PreviousHash     *id.Hash // nil at revision 1
	ProjectedThrough uint64   // event_clock the projection is materialized through
	ProjectionHash   id.Hash  // domain-separated hash of Payload (recomputable)
	Payload          []byte   // canonical CBOR of the kind-specific body
	Signature        []byte
}

// Snapshot wire key table (append-only).
const (
	snKeySpaceID      = 1
	snKeyDocumentKind = 2
	snKeyRevision     = 3
	snKeyPreviousHash = 4
	snKeyProjClock    = 5
	snKeyProjHash     = 6
	snKeyPayload      = 7
	snKeySignature    = 8
)

// ProjectionHashOf is the domain-separated digest a visitor recomputes from
// the payload to bind it to its document kind.
func ProjectionHashOf(kind string, payload []byte) id.Hash {
	h := sha256.New()
	h.Write([]byte("qs.snapshot.v1"))
	h.Write([]byte(kind))
	h.Write(payload)
	var out id.Hash
	h.Sum(out[:0])
	return out
}

// NewSnapshot builds an unsigned snapshot for a payload, stamping the
// projection hash. Revision 1 must pass previous == nil.
func NewSnapshot(space id.TerminalID, kind string, revision, projectedThrough uint64, previous *id.Hash, payload []byte) (*Snapshot, error) {
	if revision == 0 {
		return nil, errors.New("composition: revision starts at 1")
	}
	if (revision == 1) != (previous == nil) {
		return nil, errors.New("composition: previous hash iff revision > 1")
	}
	return &Snapshot{
		SpaceID: space, DocumentKind: kind, Revision: revision,
		PreviousHash: previous, ProjectedThrough: projectedThrough,
		ProjectionHash: ProjectionHashOf(kind, payload), Payload: payload,
	}, nil
}

func (s *Snapshot) fieldCount(withSig bool) int {
	n := 6 // space_id, kind, revision, proj_clock, proj_hash, payload
	if s.PreviousHash != nil {
		n++
	}
	if withSig {
		n++
	}
	return n
}

// encodeBody emits the canonical frame, optionally including the signature.
func (s *Snapshot) encodeBody(withSig bool) ([]byte, error) {
	if s.DocumentKind == "" || len(s.DocumentKind) > 32 {
		return nil, errors.New("composition: bad document kind")
	}
	buf := codec.AppendMap(nil, s.fieldCount(withSig))
	buf = codec.AppendUint(buf, snKeySpaceID)
	buf = codec.AppendBytes(buf, s.SpaceID[:])
	buf = codec.AppendUint(buf, snKeyDocumentKind)
	buf = codec.AppendText(buf, s.DocumentKind)
	buf = codec.AppendUint(buf, snKeyRevision)
	buf = codec.AppendUint(buf, s.Revision)
	if s.PreviousHash != nil {
		buf = codec.AppendUint(buf, snKeyPreviousHash)
		buf = codec.AppendBytes(buf, s.PreviousHash[:])
	}
	buf = codec.AppendUint(buf, snKeyProjClock)
	buf = codec.AppendUint(buf, s.ProjectedThrough)
	buf = codec.AppendUint(buf, snKeyProjHash)
	buf = codec.AppendBytes(buf, s.ProjectionHash[:])
	buf = codec.AppendUint(buf, snKeyPayload)
	buf = codec.AppendBytes(buf, s.Payload)
	if withSig {
		buf = codec.AppendUint(buf, snKeySignature)
		buf = codec.AppendBytes(buf, s.Signature)
	}
	return buf, nil
}

// SigningBytes is the canonical encoding the terminal key signs.
func (s *Snapshot) SigningBytes() ([]byte, error) { return s.encodeBody(false) }

// Sign signs with the space terminal private key (pub must equal SpaceID) and
// returns the canonical signed frame.
func (s *Snapshot) Sign(terminalPriv ed25519.PrivateKey) ([]byte, error) {
	pub := terminalPriv.Public().(ed25519.PublicKey)
	if !bytes.Equal(pub, s.SpaceID[:]) {
		return nil, errors.New("composition: signing key does not match space id")
	}
	body, err := s.SigningBytes()
	if err != nil {
		return nil, err
	}
	s.Signature = ed25519.Sign(terminalPriv, body)
	return s.encodeBody(true)
}

// Hash links revisions: the hash of a signed frame.
func Hash(frame []byte) id.Hash { return id.HashOf(frame) }

// DecodeSnapshot parses a frame, skipping unknown keys (ADR-009).
func DecodeSnapshot(frame []byte) (*Snapshot, error) {
	d := codec.NewDecoder(frame)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	s := &Snapshot{}
	read := func(dst []byte, n int) error {
		b, err := d.ReadBytes()
		if err != nil {
			return err
		}
		if len(b) != n {
			return fmt.Errorf("composition: field must be %d bytes", n)
		}
		copy(dst, b)
		return nil
	}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case snKeySpaceID:
			er = read(s.SpaceID[:], 32)
		case snKeyDocumentKind:
			s.DocumentKind, er = d.ReadText()
		case snKeyRevision:
			s.Revision, er = d.ReadUint()
		case snKeyPreviousHash:
			var h id.Hash
			er = read(h[:], 32)
			s.PreviousHash = &h
		case snKeyProjClock:
			s.ProjectedThrough, er = d.ReadUint()
		case snKeyProjHash:
			er = read(s.ProjectionHash[:], 32)
		case snKeyPayload:
			s.Payload, er = d.ReadBytes()
		case snKeySignature:
			s.Signature, er = d.ReadBytes()
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
	return s, nil
}

// Verify checks the terminal-key signature and that the projection hash binds
// the payload to the declared kind. This is the visitor's snapshot-tip check;
// it needs no prior snapshot and no event replay.
func (s *Snapshot) Verify() error {
	if s.Revision == 0 {
		return errors.New("composition: revision starts at 1")
	}
	if (s.Revision == 1) != (s.PreviousHash == nil) {
		return errors.New("composition: previous hash iff revision > 1")
	}
	if s.ProjectionHash != ProjectionHashOf(s.DocumentKind, s.Payload) {
		return errors.New("composition: projection hash does not match payload")
	}
	body, err := s.encodeBody(false)
	if err != nil {
		return err
	}
	if len(s.Signature) != ed25519.SignatureSize ||
		!ed25519.Verify(ed25519.PublicKey(s.SpaceID[:]), body, s.Signature) {
		return errors.New("composition: invalid signature")
	}
	return nil
}

// VerifyChainStep verifies an update against a locally-held prior snapshot:
// same space + kind, revision = prev+1, previous hash links, clock does not
// go backward (ADR-013 refinement 1). prevFrame is the signed bytes of the
// trusted local snapshot.
func (s *Snapshot) VerifyChainStep(prev *Snapshot, prevFrame []byte) error {
	if err := s.Verify(); err != nil {
		return err
	}
	if s.SpaceID != prev.SpaceID {
		return errors.New("composition: chain space id changed")
	}
	if s.DocumentKind != prev.DocumentKind {
		return errors.New("composition: chain document kind changed")
	}
	if s.Revision != prev.Revision+1 {
		return fmt.Errorf("composition: revision must be %d, got %d", prev.Revision+1, s.Revision)
	}
	if s.PreviousHash == nil || *s.PreviousHash != Hash(prevFrame) {
		return errors.New("composition: previous hash does not link to local snapshot")
	}
	if s.ProjectedThrough < prev.ProjectedThrough {
		return errors.New("composition: projected_through_clock went backward")
	}
	return nil
}
