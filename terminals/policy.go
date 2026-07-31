// Space access policy (PA-0): visibility, join and publish rules ride the
// space's SIGNED manifest as qp.* declared labels — the same pattern as
// Character, and the same root of trust: the manifest is signed by the
// terminal key whose public key IS the space id (ADR-001), so any stranger
// can verify the policy intrinsically.
//
// Authorization model, honestly stated: the protocol has no in-space
// device→principal certificate distribution yet, so a curated space
// authorizes WRITER DEVICE BINDINGS attested by the owner at creation
// (space key → policy labels → principal:device). A bare "I am principal X"
// claim is never accepted — the signer device must match an attested
// binding. Policy is immutable at creation in PA-0 (manifest revisions are
// a later wave), so the binding set is fixed too.
package terminals

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// Visibility is the space's read-access tier.
type Visibility string

const (
	// VisibilityPrivate is the absent-label default: today's epoch-encrypted
	// space. Every pre-PA-0 manifest parses to this.
	VisibilityPrivate Visibility = "private"
	// VisibilityUnlisted is public-by-link: anyone holding the space id can
	// read; the space is not catalog-eligible.
	VisibilityUnlisted Visibility = "unlisted"
	// VisibilityPublic is unlisted plus catalog-eligible (catalogs are a
	// later wave; the flag is the seam).
	VisibilityPublic Visibility = "public"
)

// Join and publish policy values (v1 set; unknown values fail Validate).
const (
	JoinNone = ""     // no self-serve join (private, broadcast)
	JoinOpen = "open" // one-click join → write (open community)

	PublishAll     = ""        // every resolved author may publish
	PublishCurated = "curated" // only attested writer bindings publish
)

// WriterBinding is one owner-attested authorized writer of a curated space:
// the principal (product identity) and the signer device the owner vouches
// for. The owner's own device rides here too (principal == controller).
type WriterBinding struct {
	Principal id.PrincipalID
	Device    id.DeviceID
}

// SpacePolicy is the parsed access policy. The zero value means "private,
// no labels" — byte-identical manifests for all pre-PA-0 spaces.
type SpacePolicy struct {
	Visibility Visibility // zero value "" == private (absent labels)
	Join       string     // JoinNone | JoinOpen
	Publish    string     // PublishAll | PublishCurated
	Writers    []WriterBinding
	// Frozen is the TRUE-freeze flag (PA-1): while set, ALL content emits
	// are refused — including the owner's — and ingress is not read; the
	// only permitted act is the manifest revision that unfreezes. Readers
	// render an honest FROZEN state instead of guessing at outages.
	Frozen bool
}

// IsPublic reports whether the space is readable without membership.
func (p SpacePolicy) IsPublic() bool {
	return p.Visibility == VisibilityUnlisted || p.Visibility == VisibilityPublic
}

// AllowsWriter reports whether the (principal, device) pair may publish
// under this policy. Non-curated policies allow every resolved author.
func (p SpacePolicy) AllowsWriter(prin id.PrincipalID, dev id.DeviceID) bool {
	if p.Publish != PublishCurated {
		return true
	}
	for _, w := range p.Writers {
		if w.Principal == prin && w.Device == dev {
			return true
		}
	}
	return false
}

// Effective returns the visibility with the absent-label default applied.
func (p SpacePolicy) Effective() Visibility {
	if p.Visibility == "" {
		return VisibilityPrivate
	}
	return p.Visibility
}

// canonicalize sorts and deduplicates writer bindings in place (stable
// label order is part of the signed encoding).
func (p *SpacePolicy) canonicalize() {
	sort.Slice(p.Writers, func(i, j int) bool {
		a, b := p.Writers[i], p.Writers[j]
		if c := strings.Compare(string(a.Principal[:]), string(b.Principal[:])); c != 0 {
			return c < 0
		}
		return string(a.Device[:]) < string(b.Device[:])
	})
	out := p.Writers[:0]
	for i, w := range p.Writers {
		if i > 0 && w == p.Writers[i-1] {
			continue
		}
		out = append(out, w)
	}
	p.Writers = out
}

// Validate enforces the v1 policy envelope and mode combinations.
func (p SpacePolicy) Validate() error {
	switch p.Effective() {
	case VisibilityPrivate:
		if p.Join != JoinNone || p.Publish != PublishAll || len(p.Writers) != 0 || p.Frozen {
			return errors.New("terminals: private spaces carry no join/publish policy")
		}
		return nil
	case VisibilityUnlisted, VisibilityPublic:
	default:
		return fmt.Errorf("terminals: unknown visibility %q", p.Visibility)
	}
	switch p.Publish {
	case PublishCurated: // broadcast
		if p.Join != JoinNone {
			return errors.New("terminals: curated (broadcast) spaces do not take joins")
		}
		if len(p.Writers) == 0 {
			return errors.New("terminals: curated spaces need at least one attested writer")
		}
	case PublishAll: // open community
		if p.Join != JoinOpen {
			return errors.New("terminals: public publish=all requires join=open")
		}
		if len(p.Writers) != 0 {
			return errors.New("terminals: writer bindings are only for publish=curated")
		}
	default:
		return fmt.Errorf("terminals: unknown publish policy %q", p.Publish)
	}
	return nil
}

// Labels encodes the policy as canonical manifest labels. The zero (private)
// policy encodes to nothing — private manifests stay byte-identical.
func (p SpacePolicy) Labels() []string {
	if p.Effective() == VisibilityPrivate {
		return nil
	}
	q := p
	q.canonicalize()
	out := []string{
		charPrefix + "visibility=" + string(q.Visibility),
	}
	if q.Join != JoinNone {
		out = append(out, charPrefix+"join="+q.Join)
	}
	if q.Publish != PublishAll {
		out = append(out, charPrefix+"publish="+q.Publish)
	}
	if q.Frozen {
		out = append(out, charPrefix+"frozen=1")
	}
	for _, w := range q.Writers {
		out = append(out, charPrefix+"writer="+
			hex.EncodeToString(w.Principal[:])+":"+hex.EncodeToString(w.Device[:]))
	}
	return out
}

// ParsePolicy decodes policy labels. Absent labels → the private zero value
// (back-compat with every existing space). Duplicate scalar labels or
// malformed writer bindings make the policy fail closed to private — a
// tampered or ambiguous policy must never widen access.
func ParsePolicy(labels []string) SpacePolicy {
	var p SpacePolicy
	seen := map[string]bool{}
	bad := false
	for _, l := range labels {
		if !strings.HasPrefix(l, charPrefix) {
			continue
		}
		kv := strings.SplitN(strings.TrimPrefix(l, charPrefix), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "visibility", "join", "publish", "frozen":
			if seen[kv[0]] {
				bad = true // conflicting duplicates: ambiguous, fail closed
				continue
			}
			seen[kv[0]] = true
			switch kv[0] {
			case "visibility":
				p.Visibility = Visibility(kv[1])
			case "join":
				p.Join = kv[1]
			case "publish":
				p.Publish = kv[1]
			case "frozen":
				p.Frozen = kv[1] == "1"
			}
		case "writer":
			parts := strings.SplitN(kv[1], ":", 2)
			if len(parts) != 2 {
				bad = true
				continue
			}
			pr, err1 := hex.DecodeString(parts[0])
			dv, err2 := hex.DecodeString(parts[1])
			if err1 != nil || err2 != nil || len(pr) != id.Size || len(dv) != id.Size {
				bad = true
				continue
			}
			var w WriterBinding
			copy(w.Principal[:], pr)
			copy(w.Device[:], dv)
			p.Writers = append(p.Writers, w)
		}
	}
	if bad {
		return SpacePolicy{} // fail closed: treat as private
	}
	p.canonicalize()
	return p
}

// Policy parses the space's access policy from its signed manifest frame.
// Bare replicas (no manifest yet) read as private — the bootstrap state.
func (s *Space) Policy() SpacePolicy {
	if len(s.ManifestFrame) == 0 {
		return SpacePolicy{}
	}
	m, err := manifest.Decode(s.ManifestFrame)
	if err != nil {
		return SpacePolicy{}
	}
	return ParsePolicy(m.DeclaredLabels)
}

// PolicyRejection is one bounded record of a frame refused by policy —
// the honest counterpart of Undecryptable ("shown, not hidden").
type PolicyRejection struct {
	Device    id.DeviceID
	Principal id.PrincipalID // the claim; may be unresolved product identity
	Schema    string
	Reason    string
	CreatedAt uint64
}

// PolicyStats counts and samples policy rejections. IgnoredRecent is a
// bounded ring — never payloads, never unbounded.
type PolicyStats struct {
	IgnoredTotal  uint64
	IgnoredRecent []PolicyRejection // ring, newest last, cap policyRingCap
}

const policyRingCap = 64

func (ps *PolicyStats) record(r PolicyRejection) {
	ps.IgnoredTotal++
	ps.IgnoredRecent = append(ps.IgnoredRecent, r)
	if len(ps.IgnoredRecent) > policyRingCap {
		ps.IgnoredRecent = ps.IgnoredRecent[len(ps.IgnoredRecent)-policyRingCap:]
	}
}

// ErrNotAuthorized is the policy refusal (I2): the frame is validly signed
// but its signer is not an attested writer of this curated space.
var ErrNotAuthorized = errors.New("terminals: signer is not an authorized writer of this space")

// ErrSpaceFrozen refuses every content emit while the policy is frozen —
// the owner's included ("final projection" means final). The only permitted
// act is the manifest revision that unfreezes.
var ErrSpaceFrozen = errors.New("terminals: this space is frozen — publication is paused")

// ValidateRevision gates a policy transition (PA-1 v1 envelope). Allowed:
// unlisted↔public, broadcast↔community (with matching join flip), curator
// binding edits, freeze on/off. Forbidden: any change across the private
// boundary — the cryptographic mode of a space is creation-immutable, and
// "return to truly private" is never promised (published content is
// irrevocable).
func ValidateRevision(old, next SpacePolicy) error {
	if err := next.Validate(); err != nil {
		return err
	}
	oldPub, nextPub := old.IsPublic(), next.IsPublic()
	if oldPub != nextPub {
		return errors.New("terminals: the private/public boundary is immutable — create a new space instead")
	}
	if !oldPub {
		return errors.New("terminals: private spaces take no policy revisions")
	}
	return nil
}

// ReviseManifest re-signs the space manifest with a new title, character
// and policy: the exact Participant.Rename pattern — Revision+1, Previous
// hash-links the current frame, signature by the SPACE terminal key.
// Controller replicas only; the policy transition must pass
// ValidateRevision against the currently signed policy.
func (s *Space) ReviseManifest(title string, c Character, pol SpacePolicy) error {
	if s.priv == nil {
		return errors.New("terminals: not the controller replica")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if err := ValidateRevision(s.Policy(), pol); err != nil {
		return err
	}
	cur, err := manifest.Decode(s.ManifestFrame)
	if err != nil {
		return err
	}
	next := *cur
	next.DeclaredLabels = append(c.Labels(title), pol.Labels()...)
	prev := manifest.Hash(s.ManifestFrame)
	next.Previous = &prev
	next.Revision = cur.Revision + 1
	next.Signature = nil
	frame, err := next.Sign(s.priv)
	if err != nil {
		return err
	}
	s.SetManifestFrame(frame)
	return nil
}

// ManifestRevision reads the revision number of a manifest frame.
func ManifestRevision(frame []byte) (uint64, error) {
	m, err := manifest.Decode(frame)
	if err != nil {
		return 0, err
	}
	return m.Revision, nil
}

// manifestSupersedes decides whether an incoming VERIFIED space manifest
// may replace the held one (anti-rollback, registry.Upsert rules adapted
// to projections, which carry only the LATEST revision):
//   - strictly greater Revision required (equal revision must be the
//     identical frame — a same-rev different-bytes fork is refused);
//   - when Revision == held+1, Previous must hash-link the held frame;
//   - larger gaps are tolerated (first-sight-baseline precedent — the
//     intermediate frames never travel).
//
// Returns (install?, error): (false, nil) means "already current, no-op".
func manifestSupersedes(held, incoming []byte) (bool, error) {
	if len(held) == 0 {
		return true, nil // bootstrap: nothing held yet
	}
	if string(held) == string(incoming) {
		return false, nil // identical frame: no-op
	}
	h, err := manifest.Decode(held)
	if err != nil {
		return true, nil // held frame unreadable: accept the verified one
	}
	in, err := manifest.Decode(incoming)
	if err != nil {
		return false, err
	}
	switch {
	case in.Revision < h.Revision:
		return false, errors.New("terminals: stale manifest revision — kept the newer one")
	case in.Revision == h.Revision:
		return false, errors.New("terminals: conflicting manifest at the same revision")
	case in.Revision == h.Revision+1:
		want := manifest.Hash(held)
		if in.Previous == nil || *in.Previous != want {
			return false, errors.New("terminals: manifest revision chain broken")
		}
	}
	return true, nil
}

// ErrReadOnlyReplica refuses local writes from reader replicas (I3).
var ErrReadOnlyReplica = errors.New("terminals: join this space to write")

// refreshPolicy re-derives enforcement from the (new) manifest frame: for
// curated spaces it installs the log admission gate (I2 — unauthorized
// frames never enter the canonical log); for frozen spaces it seals
// admission entirely. Call wherever ManifestFrame is set.
//
// Determinism rule (PA-1): reducers.State.Authorized (defense in depth)
// applies ONLY while Manifest.Revision == 1 AND the initial policy is
// curated. After ANY manifest revision it is permanently disabled for the
// space — a broadcast→community→broadcast chain must never retroactively
// hide content that was legally admitted during the community phase, and
// replay must stay identical on old and fresh replicas. All authorization
// then lives at the admission gate.
func (s *Space) refreshPolicy() {
	pol := s.Policy()
	if pol.Frozen {
		// TRUE freeze: nothing is admitted — the owner's frames included.
		s.State.Authorized = nil // freeze is always rev ≥ 2
		s.Log.SetAdmit(func(env *signal.Envelope) error {
			s.PolicyStats.record(PolicyRejection{
				Device: env.Device, Principal: env.Principal,
				Schema: env.Schema, Reason: "space frozen",
				CreatedAt: env.CreatedAt,
			})
			return ErrSpaceFrozen
		})
		return
	}
	if pol.Publish != PublishCurated {
		s.Log.SetAdmit(nil)
		s.State.Authorized = nil
		return
	}
	writers := make(map[WriterBinding]bool, len(pol.Writers))
	for _, w := range pol.Writers {
		writers[w] = true
	}
	var m *manifest.Manifest
	if dm, err := manifest.Decode(s.ManifestFrame); err == nil {
		m = dm
	}
	// One computation, shared with the transient preview (PS-3), so the
	// preview can never be a LAXER reader than a real replica.
	s.State.Authorized = authorizedFromPolicy(m, pol)
	s.Log.SetAdmit(func(env *signal.Envelope) error {
		if writers[WriterBinding{Principal: env.Principal, Device: env.Device}] {
			return nil
		}
		s.PolicyStats.record(PolicyRejection{
			Device: env.Device, Principal: env.Principal,
			Schema: env.Schema, Reason: "not an attested writer",
			CreatedAt: env.CreatedAt,
		})
		return ErrNotAuthorized
	})
}

// authorizedFromPolicy computes the defense-in-depth principal set for a
// verified manifest+policy pair: non-nil ONLY while Revision == 1 and the
// initial policy is curated (the PA-1 determinism rule stated above). Both
// the replica (refreshPolicy) and the transient preview build it here.
func authorizedFromPolicy(m *manifest.Manifest, pol SpacePolicy) map[id.PrincipalID]bool {
	if m == nil || pol.Frozen || pol.Publish != PublishCurated || m.Revision != 1 {
		return nil
	}
	principals := map[id.PrincipalID]bool{}
	for _, w := range pol.Writers {
		principals[w.Principal] = true
	}
	principals[m.Controller] = true
	return principals
}
