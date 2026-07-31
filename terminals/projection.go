// Public projection build + install (PA-0.4A/B). The publisher (controller
// replica) selects a bounded, dependency-coherent set of authorized frames
// and signs it with the space key; a fresh reader installs the envelope
// into its projection store WITHOUT chain-contiguity requirements.
//
// Correctness note (I9): the reducers materialize identical state from the
// same event SET regardless of order (kernel/reducers contract), so
// "install(selection)" equals "replay(full history) restricted to the
// selection" by construction. Pruning drops whole old feed entries (and
// ages out their satellites naturally); frames referencing a pruned target
// stay unprojected on the reader exactly as out-of-order orphans do —
// never mis-projected.
package terminals

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/reducers"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/projection"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// PublicProjectionLimits bound the projection (the relay mailbox is a
// bounded read model, not an archive).
type PublicProjectionLimits struct {
	MaxFrames int
	MaxBytes  int
	MaxAge    time.Duration // limits ordinary feed entries + delta selection only
	// Exclude hides individual frames from the projection WITHOUT touching
	// the canonical log (PA-0.4D asset custody: a media publication stays
	// unprojected until the publisher holds and verified its blob). The
	// author chain in the log stays contiguous; the projection store is
	// gap-tolerant, so later frames still materialize for readers.
	Exclude func(env *signal.Envelope, eid id.EventID) bool
	// IngressHints are the mailbox addresses this projection advertises for
	// contributions and media wants (PH-2). Supplied by the caller, which
	// owns the relay's bucket clock; terminals only signs what it is given.
	IngressHints [][]byte
}

// MaxProjectionBytes is the real ceiling, and it is not a taste question.
// The envelope travels as ONE CBOR byte string inside a relay message, and
// codec.MaxItemLen bounds any single string at 1 MiB on the READ side. A
// larger projection encodes fine, then the relay's DecodeMsg rejects the
// packet and drops it silently — the publisher sees a timeout and no space
// ever says why it stopped publishing. 768 KiB matches the item budget the
// media path already uses and leaves room for message framing.
const MaxProjectionBytes = 768 << 10

// IngressRoot derives the secret behind a space's ingress mailbox
// addresses. It comes from the space's own private key, so exactly one
// party can compute it — the controller — and nothing extra has to be
// stored or rotated. The addresses it produces are published in the signed
// projection; the root itself never leaves this process.
func (s *Space) IngressRoot() ([32]byte, bool) {
	if len(s.priv) == 0 {
		return [32]byte{}, false
	}
	h := hmac.New(sha256.New, s.priv)
	h.Write([]byte("qp-ingress-root-v1"))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, true
}

// ErrProjectionTooLarge means the space cannot be published as one bounded
// snapshot: even after aging out every prunable frame, the structural
// remainder does not fit. Callers should surface it verbatim — it is a real
// state of the space, not a transient failure.
var ErrProjectionTooLarge = errors.New(
	"terminals: public projection exceeds the relay item limit")

// DefaultProjectionLimits fit comfortably under the relay item cap.
func DefaultProjectionLimits() PublicProjectionLimits {
	return PublicProjectionLimits{
		MaxFrames: 2000, MaxBytes: MaxProjectionBytes, MaxAge: 30 * 24 * time.Hour,
	}
}

// prunable reports whether a schema is an "ordinary feed entry" the bounds
// may age out. Structural state (manifests, membership, publications, apps,
// appearance, palettes, keeps) always stays: objects referenced by current
// public state survive regardless of age.
func prunable(schema string) bool {
	switch schema {
	case schemas.MessageText, schemas.MessageRevised, schemas.MessageTombstoned,
		schemas.PresenceUpdate, schemas.ObservationTemp:
		return true
	}
	// Block media entries age out with their messages; resonance ages with
	// its targets. Everything else (publication.*, app.*, keep.*,
	// appearance.*, palette.*, membership.*, manifests) is structural.
	if schemas.IsBlockSchema(schema) {
		return true
	}
	switch {
	case len(schema) > 10 && schema[:10] == "resonance.":
		return true
	}
	return false
}

// BuildPublicProjection selects, bounds, and SIGNS the public projection.
// Controller replicas only (the signature is the space key's, ADR-001).
// Returns the wire envelope and its content digest (the publisher bumps
// Seq exactly when the digest changes; heartbeats reuse the Seq).
func (s *Space) BuildPublicProjection(seq uint64, publisher id.DeviceID,
	nowUnix uint64, lim PublicProjectionLimits) ([]byte, [32]byte, error) {

	if s.priv == nil {
		return nil, [32]byte{}, errors.New("terminals: not the controller replica")
	}
	if !s.Policy().IsPublic() {
		return nil, [32]byte{}, errors.New("terminals: space is not public")
	}
	if lim.MaxFrames <= 0 || lim.MaxBytes <= 0 {
		d := DefaultProjectionLimits()
		d.Exclude = lim.Exclude
		lim = d
	}

	type rec struct {
		frame []byte
		env   *signal.Envelope
		eid   id.EventID
	}
	var all []rec
	total := 0
	if err := s.Log.Replay(func(a eventlog.Applied) error {
		all = append(all, rec{a.Frame, a.Env, a.ID})
		total += len(a.Frame)
		return nil
	}); err != nil {
		return nil, [32]byte{}, err
	}

	drop := map[id.EventID]bool{}
	truncated := false
	cutoff := uint64(0)
	if lim.MaxAge > 0 {
		if age := uint64(lim.MaxAge / time.Second); nowUnix > age {
			cutoff = nowUnix - age
		}
	}
	// Live-carrier retention (PA-Assets). A block frame carrying the
	// AssetRef of media a CURRENT publication references is what makes
	// that media fetchable at all — the ref holds the manifest id and the
	// key, and nothing else does. Age pruning used to drop these with
	// their thirty-day cohort, leaving a post whose cover no peer could
	// ever serve: the requester knew a bare hex id and nothing more.
	//
	// So live carriers are EXEMPT from tooOld — but they stay candidates
	// under the byte/frame budget, pruned last and oldest-first. A hard
	// exemption would convert the failure mode: ErrProjectionTooLarge is
	// terminal (the space stops publishing), and a media-heavy space must
	// degrade one old cover at a time, never brick.
	live := map[string]bool{}
	for _, pub := range s.State.Publications() {
		if pub.Document == nil {
			continue
		}
		for aid := range pub.Document.LiveAssetIDs() {
			live[aid] = true
		}
	}
	liveCarrier := map[id.EventID]bool{}
	if len(live) > 0 {
		for _, r := range all {
			if !schemas.IsBlockSchema(r.env.Schema) {
				continue
			}
			for _, ref := range schemas.ExtractAssetRefs(r.env.Schema, r.env.Payload) {
				if ref == nil {
					continue
				}
				if live[ref.PublicIDHex()] || live[hex.EncodeToString(ref.AssetID[:])] {
					liveCarrier[r.eid] = true
					break
				}
			}
		}
	}
	// Candidates: prunable frames, non-live first, then live carriers —
	// each half oldest-first, deterministic (CreatedAt, LogicalClock,
	// EventID). Budget pressure therefore consumes the ordinary cohort
	// before it touches anything a current publication still needs.
	var cand []rec
	for _, r := range all {
		if prunable(r.env.Schema) {
			cand = append(cand, r)
		}
	}
	sort.Slice(cand, func(i, j int) bool {
		a, b := cand[i], cand[j]
		if la, lb := liveCarrier[a.eid], liveCarrier[b.eid]; la != lb {
			return !la // non-live first
		}
		if a.env.CreatedAt != b.env.CreatedAt {
			return a.env.CreatedAt < b.env.CreatedAt
		}
		if a.env.LogicalClock != b.env.LogicalClock {
			return a.env.LogicalClock < b.env.LogicalClock
		}
		return string(a.eid[:]) < string(b.eid[:])
	})
	frames, bytesTotal := len(all), total
	for _, c := range cand {
		over := frames > lim.MaxFrames || bytesTotal > lim.MaxBytes
		tooOld := !liveCarrier[c.eid] && cutoff > 0 &&
			c.env.CreatedAt > 0 && c.env.CreatedAt < cutoff
		if !over && !tooOld {
			// Within the non-live prefix everything later is newer (not
			// tooOld); live carriers drop only under budget pressure,
			// which is monotone. Nothing further qualifies.
			break
		}
		drop[c.eid] = true
		frames--
		bytesTotal -= len(c.frame)
		truncated = true
	}

	// Retained set in log order; cut points record, per author chain, the
	// highest pruned sequence (where the full chain resumes).
	env := &projection.Envelope{
		SpaceID:         s.ID,
		Seq:             seq,
		PublishedAt:     nowUnix,
		PublisherDevice: publisher,
		ManifestFrame:   s.ManifestFrame,
		IngressHints:    lim.IngressHints,
	}
	cuts := map[id.DeviceID]projection.CutPoint{}
	oldest := uint64(0)
	for _, r := range all {
		if lim.Exclude != nil && lim.Exclude(r.env, r.eid) {
			continue // custody-held: hidden, but NOT a chain cut (temporary)
		}
		if drop[r.eid] {
			if c, ok := cuts[r.env.Device]; !ok || r.env.Sequence > c.AfterSeq {
				cuts[r.env.Device] = projection.CutPoint{
					Device: r.env.Device, AfterSeq: r.env.Sequence, Tip: r.eid,
				}
			}
			continue
		}
		env.Frames = append(env.Frames, r.frame)
		if prunable(r.env.Schema) && r.env.CreatedAt > 0 &&
			(oldest == 0 || r.env.CreatedAt < oldest) {
			oldest = r.env.CreatedAt
		}
	}
	env.Truncated = truncated
	env.OldestTime = oldest
	devs := make([]id.DeviceID, 0, len(cuts))
	for d := range cuts {
		devs = append(devs, d)
	}
	sort.Slice(devs, func(i, j int) bool { return string(devs[i][:]) < string(devs[j][:]) })
	for _, d := range devs {
		env.CutPoints = append(env.CutPoints, cuts[d])
	}

	wire, err := env.Sign(s.priv)
	if err != nil {
		return nil, [32]byte{}, err
	}
	// The last word on size, because MaxBytes cannot be it: aging only drops
	// PRUNABLE frames, and a space can hold more unprunable content
	// (publications, apps, keeps, membership) than one envelope may carry.
	// Say so here rather than handing the relay a packet it will silently
	// drop — a publisher deserves a sentence, not a timeout.
	if len(wire) > MaxProjectionBytes {
		return nil, [32]byte{}, fmt.Errorf("%w: %d bytes over a %d limit",
			ErrProjectionTooLarge, len(wire), MaxProjectionBytes)
	}
	return wire, env.ContentDigest(), nil
}

// InstallPublicProjection verifies a projection envelope end-to-end and
// applies it into this replica's projection store: the space signature
// (I7), the manifest (intrinsically, against the space id), then every
// frame's own device signature. Installation needs NO chain contiguity —
// this is the derived read model, never the canonical log (I6). Idempotent
// per event. Returns how many events were newly applied.
// verifyProjectionManifest is the shared verification half of accepting a
// projection: envelope signature against the space key, manifest decode,
// identity match and manifest signature. The replica install and the
// transient preview (PS-3) both come through here, so "verified" cannot
// mean two different things.
func verifyProjectionManifest(spaceID id.TerminalID, env *projection.Envelope) (*manifest.Manifest, error) {
	if env.SpaceID != spaceID {
		return nil, errors.New("terminals: projection for another space")
	}
	if err := projection.Verify(env); err != nil {
		return nil, err
	}
	m, err := manifest.Decode(env.ManifestFrame)
	if err != nil {
		return nil, err
	}
	if m.Terminal != spaceID || m.Kind != manifest.KindSpace {
		return nil, errors.New("terminals: projection manifest mismatch")
	}
	if err := manifest.VerifyFrame(env.ManifestFrame, m); err != nil {
		return nil, err
	}
	return m, nil
}

// verifiedProjectionFrame decodes and verifies one projected frame. A
// failure means skip — one bad frame never poisons a projection — and both
// consumers skip identically because there is one function to skip in.
func verifiedProjectionFrame(spaceID id.TerminalID, frame []byte) (*signal.Envelope, id.EventID, bool) {
	fe, err := signal.Decode(frame)
	if err != nil {
		return nil, id.EventID{}, false
	}
	if fe.Terminal != spaceID {
		return nil, id.EventID{}, false
	}
	if signal.VerifyFrame(frame, fe) != nil {
		return nil, id.EventID{}, false
	}
	return fe, id.EventIDOf(frame), true
}

// MaterializePublicProjection replays a verified projection into a
// THROWAWAY reducer state: no Space, no log, no disk, no keystore. This is
// the transient preview's whole substance (PS-3) — the non-persistence is
// by construction, because there is nothing here that could persist.
//
// The state is deliberately not a laxer reader than a real replica: the
// same verification, the same dedup, and the same Authorized defense in
// depth, built from the same verified policy.
func MaterializePublicProjection(spaceID id.TerminalID, env *projection.Envelope) (*reducers.State, *manifest.Manifest, error) {
	m, err := verifyProjectionManifest(spaceID, env)
	if err != nil {
		return nil, nil, err
	}
	pol := ParsePolicy(m.DeclaredLabels)
	if !pol.IsPublic() {
		return nil, nil, errors.New("terminals: projection for a non-public policy")
	}
	st := reducers.NewState()
	c := m.Controller
	st.Controller = &c
	st.Authorized = authorizedFromPolicy(m, pol)
	seen := map[id.EventID]bool{}
	for _, frame := range env.Frames {
		fe, eid, ok := verifiedProjectionFrame(spaceID, frame)
		if !ok || seen[eid] {
			continue
		}
		seen[eid] = true
		// Key distribution is not user-visible state, and a throwaway
		// state has no registry for member manifests. Sealed frames are
		// unreadable without epoch keys the preview rightly lacks.
		if fe.Schema == schemas.MembershipEpoch || fe.Schema == schemas.ManifestUpdated {
			continue
		}
		if fe.PayloadEncoding == signal.PayloadEncrypted {
			continue
		}
		st.Apply(fe, eid)
	}
	return st, m, nil
}

func (s *Space) InstallPublicProjection(env *projection.Envelope) (int, error) {
	if _, err := verifyProjectionManifest(s.ID, env); err != nil {
		return 0, err
	}
	// Revision-aware install (PA-1, anti-rollback): a stale or forked
	// manifest revision never replaces the held one; identical frames are
	// a no-op install-wise (frames still apply below).
	supersedes, err := manifestSupersedes(s.ManifestFrame, env.ManifestFrame)
	if err != nil {
		return 0, err
	}
	if supersedes {
		s.SetManifestFrame(env.ManifestFrame)
	}
	if !s.Policy().IsPublic() {
		return 0, errors.New("terminals: projection for a non-public policy")
	}
	if s.projSeen == nil {
		s.projSeen = map[id.EventID]bool{}
	}
	applied := 0
	for _, frame := range env.Frames {
		fe, eid, ok := verifiedProjectionFrame(s.ID, frame)
		if !ok || s.projSeen[eid] {
			continue
		}
		s.projSeen[eid] = true
		if s.Log.Has(eid) {
			// Our own (or otherwise locally held) event echoed back in the
			// authenticated projection: this is the at-least-once ACK (I8) —
			// it leaves the pending set; no re-materialization.
			continue
		}
		s.ProjectionFrames++
		s.absorb(eventlog.Applied{Env: fe, ID: eid, Frame: frame})
		applied++
	}
	return applied, nil
}

// UnackedLocalFrames is the contributor's durable pending set (I8): every
// local-log frame not yet observed inside an authenticated public
// projection. The canonical log IS the durable queue — restart-safe by
// construction; the cumulative bundle built from this set is byte-stable
// between changes, so relay-side content-idempotent Put holds one slot.
func (s *Space) UnackedLocalFrames() [][]byte {
	var out [][]byte
	_ = s.Log.Replay(func(a eventlog.Applied) error {
		if !s.projSeen[a.ID] {
			out = append(out, a.Frame)
		}
		return nil
	})
	return out
}
