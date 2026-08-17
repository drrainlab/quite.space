// The admission verdict (MD-0b).
//
// `error` or `nil` cannot carry the distinction this layer stands on. A frame
// may be refused because a prerequisite has NOT ARRIVED YET, or because it
// never can be — and the two demand opposite handling once IngressHold
// exists:
//
//	HOLD    keep the bytes, re-judge when the prerequisite changes
//	REJECT  delete them, and say why
//
// Holding everything refused would turn protection against loss into a
// durable denial of service: a revoked device could fill a queue on purpose
// with material that is perfectly signed and permanently forbidden. So the
// reason decides, and `revoked` is never a HOLD.
//
// CLASSIFICATION IS NOT PERSISTENCE. Nothing here stores a frame or knows
// that a hold exists. That keeps the identity layer from quietly acquiring
// transport custody, and it is why the orchestration above can be written
// as: persist raw → classify → act on the verdict.
package node

import (
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// AdmissionVerdict is what the ingress orchestration does next.
type AdmissionVerdict int

const (
	// Admit — apply it, and delete any hold.
	Admit AdmissionVerdict = iota
	// Hold — the bytes are not yet trustable but may become so. Keep them.
	Hold
	// Reject — no future state makes this admissible. Delete and diagnose.
	Reject
)

func (v AdmissionVerdict) String() string {
	switch v {
	case Admit:
		return "admit"
	case Hold:
		return "hold"
	case Reject:
		return "reject"
	}
	return "unknown"
}

// AdmissionReason names WHY, because the reason is what decides whether the
// bytes are worth keeping. Serialized into diagnostics; values must not move.
type AdmissionReason int

const (
	ReasonAdmitted AdmissionReason = iota
	// Transient — HOLD.
	ReasonCertificateNotKnown
	// Permanent — REJECT.
	ReasonRevoked
	ReasonPrincipalMismatch
)

func (r AdmissionReason) String() string {
	switch r {
	case ReasonAdmitted:
		return ""
	case ReasonCertificateNotKnown:
		return "certificate_not_known"
	case ReasonRevoked:
		return "revoked"
	case ReasonPrincipalMismatch:
		return "principal_certificate_mismatch"
	}
	return "unknown"
}

// AdmissionResult is one decision about one frame.
type AdmissionResult struct {
	Verdict AdmissionVerdict
	Reason  AdmissionReason
}

// classify decides, without storing anything.
//
// NOTE ON WHAT IS ABSENT. Malformed frames, bad signatures, wrong space and
// impossible chains are not here: they are refused before this layer and by
// the log itself. And `chain_predecessor_missing` is deliberately NOT a HOLD
// — eventlog already buffers future sequences (log.go:156-168), and two
// mechanisms holding one frame would be two retry state machines waiting on
// each other. IngressHold owns only what an EXTERNAL admission prerequisite
// blocks; ordering stays the log's business.
func (s *identityState) classify(env *signal.Envelope) AdmissionResult {
	// A certificate carries its own proof (verified in observe), so it is
	// admitted on its own merits and never on its bearer's — otherwise the
	// frame that would release the held ones would itself be held, and the
	// queue could never drain.
	switch env.Schema {
	case schemas.DeviceCertified, schemas.DeviceRevoked:
		return AdmissionResult{Verdict: Admit}
	}
	b := legacyBindingOf(env)
	if s.legacy[b] {
		return AdmissionResult{Verdict: Admit}
	}
	cert, known := s.store.Certificate(env.Device)
	if !known {
		// The one transient case: the certificate may still be one round
		// behind its bearer's first message. This is the whole reason
		// IngressHold exists.
		return AdmissionResult{Verdict: Hold, Reason: ReasonCertificateNotKnown}
	}
	if cert.Principal != env.Principal {
		// Certified, but by a different root than the one being claimed. No
		// certificate that ever arrives makes this true.
		return AdmissionResult{Verdict: Reject, Reason: ReasonPrincipalMismatch}
	}
	if err := s.store.Admit(env.Principal, env.Device, env.LogicalClock); err != nil {
		// Certified and matching, so the only remaining refusal is
		// revocation — and a revoked device must never be held, or being
		// forbidden becomes a way to fill somebody's disk.
		return AdmissionResult{Verdict: Reject, Reason: ReasonRevoked}
	}
	return AdmissionResult{Verdict: Admit}
}
