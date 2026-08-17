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

// admit is the gate itself (PASS 2). Installed on every space log.
func (s *identityState) admit(env *signal.Envelope) error {
	// A CERTIFICATE CARRIES ITS OWN PROOF, so it is admitted on its own
	// merits and never on its bearer's. This is what unties the knot the
	// first attempt tied: a device cannot publish the certificate that would
	// let it be admitted if publishing already required being admitted.
	//
	// It costs nothing to allow. The payload is signed by a ROOT key and
	// observe() verifies that signature before any of it becomes trust, so a
	// forged one is bytes that go nowhere. What it buys is that certification
	// can always travel — which is the precondition for every other rule
	// here, and the reason this does not need the signed join structures
	// widened to carry one.
	switch env.Schema {
	case schemas.DeviceCertified, schemas.DeviceRevoked:
		return nil
	}
	b := storage.LegacyBinding{Principal: env.Principal, Device: env.Device}
	if s.legacy[b] {
		// Predates certification on this node. Admitted as it always was —
		// and note this is a pair, not a device: the allowlist does not let
		// a known device start claiming a different person.
		return nil
	}
	return s.store.Admit(env.Principal, env.Device, env.LogicalClock)
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

// publishCertLocked emits this device's certificate into a space that does
// not already carry it. Called wherever the manifest is published, because
// the two answer the same question at the same moment: who is speaking here,
// and by what authority.
//
// Idempotence is the registry-shaped kind: the trust store already holds our
// certificate, and the space log is scanned at open. What this guards is the
// runtime case — a space joined or created while the process is running,
// which never passes through Open's loop and would otherwise leave every
// peer unable to admit us.
func (r *Runtime) publishCertLocked(s *terminals.Space) {
	if len(r.selfCert) == 0 || r.certPublished[s.ID] {
		return
	}
	if _, err := r.Self.Emit(s, schemas.DeviceCertified, r.selfCert,
		r.Self.DefaultAuthorship(), 0); err != nil {
		return // best effort: a certificate that cannot go now goes at the next open
	}
	r.certPublished[s.ID] = true
}
