// What a person may quote out of a space (SHARE-1).
//
// TWO different names for forwarding exist in this tree and they must never
// meet. The NR track owns ForwardingRole, ForwardingContext and
// SuppressForward — that is TRANSPORT transit, whether this node carries
// somebody else's envelope between two external links. Nothing in this
// package or in node/share.go uses the word: a grep for Forward returns
// routing, and only routing. The interface may still say "Forward"; that is
// the word people use, and it is not an identifier.
//
// The allowlist is deliberately the SAME SEVEN as keep's, and the symmetry
// is the justification: what a space may keep is what a person may quote
// out of it. Two are enabled in this build, and that is a scope decision
// with a reason rather than caution — see EnabledInBeta.
package share

import "github.com/drrainlab/quiet_places/protocol/schemas"

// shareableSchemas is the eventual set. Everything absent is absent on
// purpose: reactions and resonance (a feeling about a message is not a
// message), keep and unkeep, presence, membership and epoch events,
// manifests, receipts and delivery events, app definitions and votes,
// listening commands, tombstones (a deletion is not a thing to pass on),
// block.attached.v1 (a carrier, not a message) and block.live_signal.v1 (a
// generative parameter set, not a statement).
var shareableSchemas = map[string]bool{
	schemas.MessageText: true,
	schemas.BlockVisual: true,
	schemas.BlockVideo:  true,
	schemas.BlockVoice:  true,
	schemas.BlockAudio:  true,
	schemas.BlockFile:   true,
	schemas.BlockLink:   true,
}

// enabledNow is what THIS build can quote.
//
// The copy is always a message.text.v1 carrying the quotation, and key 4 is
// only PROVEN safe for that schema: its decoder ends in a skip, so an older
// client renders the text and ignores the provenance. Every other schema
// would need its own audit before it could carry the same field, and a
// generic wrapper would force every renderer to learn a new container —
// which is the over-engineering this wave is avoiding. Text and link are
// enabled because both are text on the wire; media follows when the audit
// does, and until then the refusal says "yet".
var enabledNow = map[string]bool{
	schemas.MessageText: true,
	schemas.BlockLink:   true,
}

// Shareable reports whether events of this schema may EVER be quoted.
func Shareable(schema string) bool { return shareableSchemas[schema] }

// EnabledInBeta reports whether this build can quote them today. A schema
// that is Shareable but not enabled gets a "not yet", which is a different
// sentence from a refusal and must stay one.
func EnabledInBeta(schema string) bool { return enabledNow[schema] }
