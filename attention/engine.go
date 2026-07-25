package attention

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Engine ties the layers together. It holds no locks and no I/O: the caller
// owns persistence and concurrency, which keeps this package testable
// without a node.
type Engine struct {
	Policy Policy
	Model  *Model
	Inbox  *Inbox

	budget budgetState
	loc    *time.Location
}

func NewEngine(p Policy) *Engine {
	return &Engine{
		Policy: p, Model: NewModel(), Inbox: NewInbox(), loc: time.Local,
	}
}

// Context is the per-candidate side information the caller resolves.
type Context struct {
	Viewer      Viewer
	SpaceTitle  string
	AuthorName  string
	AuthorKnown bool
	// SpacePriority nudges spaces the person actually cares about.
	SpacePriority float64
	// ViewingNow suppresses signals for the space that is open right now —
	// telling someone about a message they are looking at is noise.
	ViewingNow bool
}

// Judge ranks one candidate. It returns ok=false when nothing should be
// stored: already judged, out of scope, or simply not interesting.
//
// Order matters and is the heart of the design:
//
//	hard rules → always a signal, statistics may not veto them
//	soft rules → candidates, which the personal model may demote
//	no rules   → nothing (the lexical model never invents signals on its own
//	             in Minimal mode; it ranks what the rules surfaced)
func (e *Engine) Judge(c Candidate, ctx Context, now int64) (Signal, bool) {
	if e.Policy.Mode == ModeOff {
		return Signal{}, false
	}
	spaceHex := c.SpaceID.Hex()
	scope := e.Policy.ScopeFor(spaceHex)
	if scope == ScopeOff {
		return Signal{}, false
	}
	if e.Inbox.Judged(c.EventID) {
		return Signal{}, false // never re-judge, however it was rediscovered
	}
	// Your own words are never news to you.
	if c.Author == ctx.Viewer.Principal {
		e.Inbox.FirstSeen(c.EventID, now)
		return Signal{}, false
	}

	receivedAt, _ := e.Inbox.FirstSeen(c.EventID, now)

	reasons, hard := Detect(c, ctx.Viewer)
	if watched := DetectWatched(c.Text, e.Policy.Watched); len(watched) > 0 {
		reasons = append(reasons, watched...)
	}
	if len(reasons) == 0 {
		return Signal{}, false
	}
	if scope == ScopeDirectOnly && !hard {
		return Signal{}, false
	}

	feats := Extract(c, ctx.AuthorKnown, ctx.SpacePriority)
	score := e.Model.Score(feats)
	layer := "rules"
	if e.Model.Trained() {
		if ex := e.Model.Explain(feats, score); len(ex) > 0 {
			reasons = append(reasons, ex...)
			layer = "lexical"
		}
	}

	delivery := e.deliver(hard, score, scope, receivedAt, ctx.ViewingNow)
	if delivery == DeliverySuppressed && !hard {
		// Remember the decision so a rescan does not reconsider it, but do
		// not store a signal the person will never see.
		return Signal{}, false
	}

	sig := Signal{
		ID:          signalID(c.EventID),
		SourceSpace: c.SpaceID, SourceEvent: c.EventID,
		SpaceHex: spaceHex, EventHex: c.EventID.Hex(),
		SpaceTitle: ctx.SpaceTitle, Author: ctx.AuthorName,
		Excerpt:  excerpt(c.Text),
		Delivery: delivery, Hard: hard, Reasons: reasons, Score: score,
		CreatedAt: c.CreatedAt, ReceivedAt: receivedAt, Layer: layer,
	}
	e.Inbox.Add(sig, now)
	return sig, true
}

// deliver picks the tier. Hard signals are guaranteed to be stored; the
// budget may still step them down to digest, but never to silence.
func (e *Engine) deliver(hard bool, score float64, scope Scope, receivedAt int64, viewingNow bool) Delivery {
	if scope == ScopeDigest {
		return DeliveryDigest
	}
	want := DeliveryDigest
	switch {
	case hard:
		want = DeliveryPriority
	case e.Model.Trained() && score >= 0.6:
		want = DeliveryPriority
	case e.Policy.Mode == ModeMinimal && !e.Model.Trained():
		// Cold model: soft candidates wait in the digest rather than
		// interrupting on a guess.
		want = DeliveryDigest
	}
	if want != DeliveryPriority {
		return want
	}
	if viewingNow {
		return DeliveryDigest // you are already reading that room
	}
	if !e.Policy.Budget.admit(&e.budget, receivedAt, e.loc) {
		return DeliveryDigest // over budget: demoted, never dropped
	}
	return DeliveryPriority
}

// Feedback applies a person's judgement of a signal to the ranking model.
// Only LEARNING verdicts reach here; policy commands (mute a space, digest
// only, never this topic) are applied by the caller to Policy and must not
// touch the model.
func (e *Engine) Feedback(c Candidate, ctx Context, useful bool) {
	feats := Extract(c, ctx.AuthorKnown, ctx.SpacePriority)
	label := 0.0
	if useful {
		label = 1
	}
	e.Model.Learn(feats, label)
}

// signalID is derived from the event so the same event never yields two
// signals, even across restarts.
func signalID(e [32]byte) string {
	sum := sha256.Sum256(append([]byte("qs.signal.v1:"), e[:]...))
	return hex.EncodeToString(sum[:8])
}

// excerpt keeps a short, whitespace-collapsed quote for the inbox card.
func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 160 {
		return string(r[:160]) + "…"
	}
	return s
}
