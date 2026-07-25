// Package attention is QuietRank: a local attention layer that helps a
// person not miss what matters, without turning the product into an
// engagement machine.
//
// Three layers, in order of trust:
//
//  1. HARD rules — a signed mention, a reply to you, a protocol alert. These
//     are facts about addressing, not guesses, so no statistical layer may
//     ever hide them.
//  2. LEXICAL features — hashed character n-grams plus RU/EN question and
//     action lexicons. Cheap, explainable, multilingual by construction
//     (n-grams do not know where one language ends), and always available:
//     no model, no download, no browser required.
//  3. SEMANTIC scores (AT-0B) — optional cross-language meaning. When the
//     encoder is absent the layer simply contributes nothing.
//
// Everything here is device-local. Nothing in this package is ever emitted
// into a space's event log, packed into a bundle, or handed to a relay:
// what caught your attention is yours alone.
package attention

import (
	"github.com/drrainlab/quiet_places/protocol/id"
)

// Candidate is one event offered to the attention layer. It carries only
// what ranking needs — the caller resolves it from the local projection.
type Candidate struct {
	EventID  id.EventID
	SpaceID  id.TerminalID
	Author   id.PrincipalID
	Kind     string // "text", "visual", … (reducers.EntryKind)
	Text     string
	ReplyTo  *id.EventID
	Mentions []id.PrincipalID

	// CreatedAt is the author's wall clock from the signed envelope —
	// ADVISORY, it can lie. ReceivedAt is the local fact of absorbing the
	// event and is what budgets and quiet hours must use.
	CreatedAt  uint64
	ReceivedAt int64
}

// Viewer is the person the ranking is for. Aliases are the names they told
// us to watch for — we never guess morphology, which would fire constantly
// on short Russian names.
type Viewer struct {
	Principal id.PrincipalID
	Aliases   []string
	// AuthoredByMe reports whether an event id is one of the viewer's own,
	// so "someone replied to you" is a fact rather than an inference.
	AuthoredByMe func(id.EventID) bool
}

// Reason explains why a candidate surfaced. Exact reasons are facts we can
// point at (a signed mention, a matched phrase). Approximate reasons come
// from hashed features, where collisions make an exact "this term did it"
// claim dishonest — those are labelled as learned patterns instead.
type Reason struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
	Exact  bool   `json:"exact"`
}

// Reason codes. Hard codes are facts about addressing; soft codes are
// signals that a personal model is allowed to demote.
const (
	// hard
	ReasonMention   = "direct_mention"
	ReasonReplyToMe = "reply_to_me"
	ReasonAlert     = "protocol_alert"
	// soft
	ReasonNameInText = "name_in_text"
	ReasonQuestion   = "direct_question"
	ReasonAction     = "action_required"
	ReasonWatched    = "watched_phrase"
	// learned / semantic
	ReasonLearned = "lexical_pattern"
	ReasonAnchor  = "matches_anchor"
)

// Verdict is the result of ranking one candidate.
type Verdict struct {
	Delivery Delivery
	Score    float64
	Hard     bool
	Reasons  []Reason
}

// Delivery is how far a signal is allowed to travel. There is deliberately
// no "instant" tier: QuietRank never raises an OS notification, so naming a
// level after push would promise something the product does not do.
type Delivery string

const (
	DeliveryPriority   Delivery = "priority"
	DeliveryDigest     Delivery = "digest"
	DeliverySuppressed Delivery = "suppressed"
)
