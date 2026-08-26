// Object→Asset edges (SP-2): the wire form of "this track HAS this mix",
// "this session HAS these takes". An edge is a separate event — not a
// record field — because attaching is the concurrent operation of studio
// life: two people uploading takes at once must both land, and full-record
// revisions would 409 one of them for no human reason.
//
// The edge is where asset meaning lives, because assets themselves cannot
// carry it: a public asset id is a content hash, so "v2 of a mix" is a
// DIFFERENT asset with no inherent link back — lineage (Supersedes), role,
// label and the candidate mark are all relations, and relations belong to
// the edge.
//
// CANDIDATE, fixed semantics (ADR-030): candidate = the preferred current
// asset for this object — "what we are listening to now". It is NOT
// "approved" and NOT "final"; both of those are future primitives
// (approved: SP-3 Creative Presence; released: a future lifecycle) and
// renderers must not conflate the three words.
package objects

import (
	"errors"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// SchemaAttached is the object→asset edge event. One LWW register per
// (object, asset) in the reducer; a later event for the same pair replaces
// the whole edge state, so every emit carries the full intended state.
const SchemaAttached = "object.attached.v1"

// Edge bounds.
const (
	MaxEdgeLabel = 200
	MaxEdgeRole  = 32
	// MaxAssetEdgesPerObject is an AUTHORING bound, enforced at the node
	// before emit — never in the reducer. Evicting an LWW register is the
	// archive/restore ordering hole in a new costume: a late stale event
	// for an evicted key would resurrect it as a fresh register and
	// diverge replicas forever. So registers are never evicted, and the
	// bound is advisory across concurrent emitters (199+199 → 201 is
	// harmless — the state is unbounded-safe).
	MaxAssetEdgesPerObject = 200
)

// Candidate values (key 9). Absent/zero means DON'T TOUCH — otherwise
// every label edit would steal the star.
const (
	CandidateUntouched = 0
	CandidateSet       = 1
	CandidateClear     = 2
)

// AttachPayload is object.attached.v1: the FULL state of one edge.
type AttachPayload struct {
	Fallback string // key 1 — "label · object name"
	ObjectID [16]byte
	// Asset is the bare public asset id: 64 lowercase hex chars (V2
	// content id) or 32 (legacy V1 handle) — both widths, like the asset
	// API. Bare deliberately: the ref+key carrier is block.attached.v1,
	// which the asset index already ingests; this event only RELATES.
	Asset string
	// Role is a FREE slug (mix, take, stem, master, other — suggested by
	// UIs, never enforced): role steers the renderer, never the kernel.
	Role    string
	Label   string
	Ordinal uint64
	// Detached marks the edge removed. A state, not a tombstone: a later
	// re-attach is just a later write to the same register.
	Detached bool
	// Supersedes names the previous version's asset id (mix-12 → mix-11).
	// The version chain is derived at projection; a bad chain (duplicate,
	// cycle) degrades to siblings, never an error.
	Supersedes string
	// Candidate: see the constants above.
	Candidate uint64
}

const (
	aeKeyFallback   = 1
	aeKeyObject     = 2
	aeKeyAsset      = 3
	aeKeyRole       = 4
	aeKeyLabel      = 5
	aeKeyOrdinal    = 6
	aeKeyDetached   = 7
	aeKeySupersedes = 8
	aeKeyCandidate  = 9
)

// ValidAssetHex reports whether s is a well-formed bare public asset id:
// 32 or 64 lowercase hex characters.
func ValidAssetHex(s string) bool {
	if len(s) != 32 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Validate enforces authoring bounds; decoders call it too — one truth.
func (p *AttachPayload) Validate() error {
	if p.ObjectID == ([16]byte{}) {
		return errors.New("objects: edge names no object")
	}
	if !ValidAssetHex(p.Asset) {
		return errors.New("objects: edge asset id is not 32/64 lowercase hex")
	}
	if p.Role != "" && !slugOK(p.Role, MaxEdgeRole) {
		return errors.New("objects: edge role is not a slug")
	}
	if len(p.Label) > MaxEdgeLabel {
		return errors.New("objects: edge label too long")
	}
	if p.Supersedes != "" {
		if !ValidAssetHex(p.Supersedes) {
			return errors.New("objects: supersedes is not 32/64 lowercase hex")
		}
		if p.Supersedes == p.Asset {
			return errors.New("objects: an asset cannot supersede itself")
		}
	}
	if p.Candidate > CandidateClear {
		return errors.New("objects: unknown candidate value")
	}
	return nil
}

func (p *AttachPayload) Encode() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	n := 3 // fallback, object, asset
	if p.Role != "" {
		n++
	}
	if p.Label != "" {
		n++
	}
	if p.Ordinal != 0 {
		n++
	}
	if p.Detached {
		n++
	}
	if p.Supersedes != "" {
		n++
	}
	if p.Candidate != CandidateUntouched {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, aeKeyFallback)
	buf = codec.AppendText(buf, p.Fallback)
	buf = codec.AppendUint(buf, aeKeyObject)
	buf = codec.AppendBytes(buf, p.ObjectID[:])
	buf = codec.AppendUint(buf, aeKeyAsset)
	buf = codec.AppendText(buf, p.Asset)
	if p.Role != "" {
		buf = codec.AppendUint(buf, aeKeyRole)
		buf = codec.AppendText(buf, p.Role)
	}
	if p.Label != "" {
		buf = codec.AppendUint(buf, aeKeyLabel)
		buf = codec.AppendText(buf, p.Label)
	}
	if p.Ordinal != 0 {
		buf = codec.AppendUint(buf, aeKeyOrdinal)
		buf = codec.AppendUint(buf, p.Ordinal)
	}
	if p.Detached {
		buf = codec.AppendUint(buf, aeKeyDetached)
		buf = codec.AppendUint(buf, 1)
	}
	if p.Supersedes != "" {
		buf = codec.AppendUint(buf, aeKeySupersedes)
		buf = codec.AppendText(buf, p.Supersedes)
	}
	if p.Candidate != CandidateUntouched {
		buf = codec.AppendUint(buf, aeKeyCandidate)
		buf = codec.AppendUint(buf, p.Candidate)
	}
	return buf, nil
}

func DecodeAttachPayload(payload []byte) (*AttachPayload, error) {
	d := codec.NewDecoder(payload)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	p := &AttachPayload{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case aeKeyFallback:
			p.Fallback, er = d.ReadText()
		case aeKeyObject:
			er = read16(d, p.ObjectID[:])
		case aeKeyAsset:
			p.Asset, er = d.ReadText()
		case aeKeyRole:
			p.Role, er = d.ReadText()
		case aeKeyLabel:
			p.Label, er = d.ReadText()
		case aeKeyOrdinal:
			p.Ordinal, er = d.ReadUint()
		case aeKeyDetached:
			var v uint64
			v, er = d.ReadUint()
			p.Detached = v == 1
		case aeKeySupersedes:
			p.Supersedes, er = d.ReadText()
		case aeKeyCandidate:
			p.Candidate, er = d.ReadUint()
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
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

func init() {
	schemas.Register(SchemaAttached, func(p []byte) error {
		_, err := DecodeAttachPayload(p)
		return err
	})
}
