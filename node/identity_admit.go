// Device certification at the log's door (MD-0, ADR-002).
//
// ADR-002 has said since 2026-07-22 that "verification requires a valid,
// unrevoked certificate chain of exactly depth 1 (root → device)". The kernel
// for it — Certificate, Revocation, Store.Admit — was written and then wired
// to nothing: every eventlog.Open in the runtime passed a nil gate, so
// env.Principal has been thirty-two unverified bytes on every event in the
// system. This file is where that stops being true.
//
// TWO PASSES, and the order is the whole point. Certificates and revocations
// live in DIFFERENT space logs, so a single-pass replay would make admission
// depend on which space happened to open first: replay B, admit an event from
// D, then replay A and only now meet Revocation(D) — and what B already
// applied does not un-apply itself. So the scan finishes across every local
// log before one admission decision is taken.
//
// LEGACY IS A FROZEN ALLOWLIST, never an open door. A device we have simply
// never seen a certificate for is not thereby trusted — that would leave a
// new attacker free to skip certification forever and keep putting anybody's
// id in env.Principal, which is the exact hole this file exists to close.
// Instead the pairs that ALREADY had history here when this build first ran
// are frozen once, and after that a first-seen device must present a
// certificate.
package node

import (
	"errors"
	"fmt"
	"time"

	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
)

// identityState is this node's answer to "may this device speak at all".
type identityState struct {
	store *identity.Store
	// legacy is the frozen pre-certification allowlist, as a set. Empty
	// means nobody: absence is never permission.
	legacy map[storage.LegacyBinding]bool
}

func newIdentityState() *identityState {
	return &identityState{store: identity.NewStore(), legacy: map[storage.LegacyBinding]bool{}}
}

// observe folds one replayed envelope into the trust store. It is the whole
// of PASS 1, called for every event of every local log before any admission
// decision is taken.
//
// A malformed or unverifiable certificate is DROPPED, not fatal: it arrived
// over the wire from somebody else, and one bad frame must not stop a person
// opening their own spaces. It simply never becomes trust.
func (s *identityState) observe(env *signal.Envelope) {
	switch env.Schema {
	case schemas.DeviceCertified:
		c, err := identity.DecodeCertificate(env.Payload)
		if err != nil {
			return
		}
		_ = s.store.AddCertificate(c) // verifies the root signature
	case schemas.DeviceRevoked:
		r, err := identity.DecodeRevocation(env.Payload)
		if err != nil {
			return
		}
		_ = s.store.AddRevocation(r)
	}
}

// freezeLegacy records the (principal, device) pairs that already had history
// on this node. Called ONCE, at the migration to MD-0, and never appended to
// afterwards — an allowlist that keeps growing admits everyone eventually.
func (s *identityState) freezeLegacy(seen map[storage.LegacyBinding]bool) []storage.LegacyBinding {
	out := make([]storage.LegacyBinding, 0, len(seen))
	for b := range seen {
		s.legacy[b] = true
		out = append(out, b)
	}
	sortLegacy(out)
	return out
}

func (s *identityState) loadLegacy(bs []storage.LegacyBinding) {
	for _, b := range bs {
		s.legacy[b] = true
	}
}

// legacyBindingOf is the allowlist key: a PAIR, never a device. Being
// grandfathered in as yourself is not permission to claim somebody else.
func legacyBindingOf(env *signal.Envelope) storage.LegacyBinding {
	return storage.LegacyBinding{Principal: env.Principal, Device: env.Device}
}

// ErrAdmissionHold marks a refusal that is a DELAY, never a judgement: the
// prerequisite has not arrived yet. Any layer that caches refusals — the
// community rejected-ring, a future one — MUST check errors.Is against this
// before remembering a frame as bad, because remembering a hold is the
// refusal-is-permanent assumption MD-0b exists to outlaw: the certificate
// arrives an hour later and the cache goes on refusing what it never
// re-judged.
var ErrAdmissionHold = errors.New("node: admission prerequisite not yet known")

// admit is the gate itself (PASS 2). Installed on every space log.
//
// It is written THROUGH classify so the two can never drift about who gets
// in. A Hold verdict surfaces as ErrAdmissionHold so callers above the log —
// which only see an error — can still tell a delay from a judgement.
func (s *identityState) admit(env *signal.Envelope) error {
	res := s.classify(env)
	switch res.Verdict {
	case Admit:
		return nil
	case Hold:
		return fmt.Errorf("%w (%s)", ErrAdmissionHold, res.Reason)
	}
	return fmt.Errorf("node: %s (%s)", res.Verdict, res.Reason)
}

// selfCertify mints this device's own certificate if the keystore has none,
// and ONLY on the authority device — a secondary holds no root seed, so this
// is not a fallback there but an impossible operation, and Keystore.Identity
// already refuses to open such a keystore rather than let it proceed.
//
// Returns the encoded certificate to publish, and whether it is new.
func selfCertify(ks *storage.Keystore, prin *identity.Principal,
	dev *identity.Device, now uint64) ([]byte, bool, error) {

	for _, rec := range ks.Certs {
		if rec.Device == dev.ID {
			return rec.Frame, false, nil
		}
	}
	if prin == nil {
		return nil, false, storage.ErrNoDeviceCertificate
	}
	frame, err := prin.Certify(dev, now, 0).Encode()
	if err != nil {
		return nil, false, err
	}
	ks.Certs = append(ks.Certs, storage.CertRecord{Device: dev.ID, Frame: frame})
	return frame, true, nil
}

// certifyOwnDevice certifies a device this node owns but that is not the
// one it signs with: the assistant and the gateway each have their own key
// and their own chain (AI-0, TR-0) while sharing the person's principal.
// They are exactly the multi-device case ADR-002 describes, and the gate
// catches them the moment it goes on — correctly, which is how this was
// found. Only the authority can do it; a secondary simply skips them.
func certifyOwnDevice(ks *storage.Keystore, st *identityState,
	prin *identity.Principal, dev *identity.Device, now uint64) []byte {

	if dev == nil {
		return nil
	}
	frame, _, err := selfCertify(ks, prin, dev, now)
	if err != nil {
		return nil
	}
	if c, er := identity.DecodeCertificate(frame); er == nil {
		_ = st.store.AddCertificate(c)
	}
	return frame
}

// certifyOwnedDeviceLocked certifies a device this node owns AT THE MOMENT IT
// COMES INTO BEING, which is the only correct moment.
//
// Certifying only in Open was measured to be wrong the instant the gate went
// on: the assistant and the gateway are minted LAZILY — the assistant on first
// use, the gateway on the first connector projection — so a node that had
// neither at open certified neither, and then refused their very first write
// with certificate_not_known. Ninety-nine tests said so at once.
//
// Callers hold r.mu. On a secondary device (no root seed) this cannot certify,
// and does not pretend to: delegated certification is MD-2's business, and
// until then an owned device minted on a secondary is refused rather than
// quietly trusted.
func (r *Runtime) certifyOwnedDeviceLocked(dev *identity.Device) {
	if dev == nil || r.Principal == nil {
		return
	}
	certifyOwnDevice(r.ks, r.ident, r.Principal, dev, uint64(time.Now().Unix()))
	// A device minted MID-RUN (the gateway on its first connector, the
	// assistant on first use) will speak in spaces that were attached before
	// it existed, so its proof must reach those logs now rather than at the
	// next open — a peer cannot admit a writer whose certificate never
	// travelled.
	for _, st := range r.spaces {
		r.publishCertLocked(st.space)
	}
}

// certificateFor returns the stored certificate for a device, which is where
// a sibling's X25519 wrapping key comes from (MD-2). The certificate has
// carried that key since it was written — identity.Certificate.X25519Pub —
// so a controller holding one has everything AddMember needs.
func (s *identityState) certificateFor(dev id.DeviceID) (*identity.Certificate, bool) {
	return s.store.Certificate(dev)
}

func sortLegacy(bs []storage.LegacyBinding) {
	// Deterministic order, like every other keystore section.
	for i := 1; i < len(bs); i++ {
		for j := i; j > 0 && lessLegacy(bs[j], bs[j-1]); j-- {
			bs[j], bs[j-1] = bs[j-1], bs[j]
		}
	}
}

func lessLegacy(a, b storage.LegacyBinding) bool {
	for i := range a.Principal {
		if a.Principal[i] != b.Principal[i] {
			return a.Principal[i] < b.Principal[i]
		}
	}
	for i := range a.Device {
		if a.Device[i] != b.Device[i] {
			return a.Device[i] < b.Device[i]
		}
	}
	return false
}

// markCertPublished records that a space's log carries the certificate of
// one certified device. Callers hold r.mu.
func (r *Runtime) markCertPublished(tid id.TerminalID, dev id.DeviceID) {
	set, ok := r.certPublished[tid]
	if !ok {
		set = map[id.DeviceID]bool{}
		r.certPublished[tid] = set
	}
	set[dev] = true
}

// publishCertLocked emits EVERY owned device's certificate this space's log
// does not already carry — the person's device, the assistant, the gateway.
// Called wherever the manifest is published, because the two answer the same
// question at the same moment: who is speaking here, and by what authority.
//
// ADR-002:36-37 — "certificates travel in the event log so peers can verify
// any device's authority offline". Once per (space, device), and only where
// the log lacks it: a certificate re-emitted every restart would grow an
// append-only log forever to say a thing that has not changed. All emitted
// from the PERSON's chain — a certificate is admitted on its own root proof,
// never on its bearer's, so who carries it does not matter, and the person's
// chain is the one guaranteed to exist before its siblings speak.
func (r *Runtime) publishCertLocked(s *terminals.Space) {
	published := r.certPublished[s.ID]
	for _, rec := range r.ks.Certs {
		if len(rec.Frame) == 0 || published[rec.Device] {
			continue
		}
		if _, err := r.Self.Emit(s, schemas.DeviceCertified, rec.Frame,
			r.Self.DefaultAuthorship(), 0); err != nil {
			continue // best effort: what cannot go now goes at the next open
		}
		r.markCertPublished(s.ID, rec.Device)
	}
}

// expandMembersLocked realizes ADR-024's decision — membership belongs
// to the PRINCIPAL, the device wrap list is derived state — at the one
// moment it matters: just before an epoch is minted. For every device
// already in the wrap list, every currently certified, unrevoked
// sibling of its principal joins the list too, wrap key straight from
// the certificate (identity.Certificate.X25519Pub — carried for exactly
// this since MD-0).
//
// No new protocol message feeds this: certificates and revocations ride
// the space log plaintext so admission can see them, which means the
// owner's own store already holds everything the expansion reads. The
// latent failure this closes, measured in BETA_AUDIT_2026-08-20: an
// owner's members map never learned a paired sibling, so the owner's
// next rotation deafened it — space present, Undecryptable climbing,
// nobody told.
//
// Revocation falls out for free: a revoked device never comes back from
// CertifiedDevices, so it silently stops appearing in every future
// wrap, everywhere, as each owner next rotates. Caller holds r.mu.
func (r *Runtime) expandMembersLocked(sp *terminals.Space) {
	for dev := range sp.Members() {
		cert, ok := r.ident.certificateFor(dev)
		if !ok {
			continue // a legacy/pre-certification member: nothing to derive
		}
		for _, sib := range r.ident.store.CertifiedDevices(cert.Principal) {
			if sib.Device == dev || sp.HasMember(sib.Device) {
				continue
			}
			sp.AddMember(sib.Device, sib.X25519Pub)
		}
	}
}
