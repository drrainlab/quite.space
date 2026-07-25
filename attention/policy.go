package attention

import "time"

// Policy is the person's attention settings. It lives device-local (inside
// the node's encrypted settings blob) and is never emitted, bundled, or
// relayed: what you choose to pay attention to is nobody else's business.
type Policy struct {
	Mode Mode `json:"mode"`
	// Aliases are the names to watch for in message text. Explicit, because
	// guessing morphology on short names fires constantly.
	Aliases []string `json:"aliases,omitempty"`
	// Watched phrases power Interest Anchors before the semantic encoder
	// lands; afterwards the same anchors also match by meaning.
	Watched []string `json:"watched,omitempty"`
	// Spaces holds per-space scope overrides, keyed by terminal id hex.
	Spaces map[string]Scope `json:"spaces,omitempty"`
	Budget Budget           `json:"budget"`
}

type Mode string

const (
	// ModeOff silences the whole layer — no scanning, no signals.
	ModeOff Mode = "off"
	// ModeMinimal is the conservative default: hard rules always, soft
	// candidates only when the personal model has learned enough to agree.
	ModeMinimal Mode = "minimal"
	// ModeCustom follows the per-space scopes exactly.
	ModeCustom Mode = "custom"
)

// Scope is how much attention one space gets.
type Scope string

const (
	ScopeOff        Scope = "off"         // not scanned at all
	ScopeDirectOnly Scope = "direct_only" // hard rules only
	ScopeHighlights Scope = "highlights"  // hard + soft (default)
	ScopeDigest     Scope = "digest"      // everything lands in the digest
)

// Budget bounds how often the layer may put something in front of a person.
// Over-budget signals are DEMOTED to digest, never dropped: the point is to
// protect attention, not to lose messages.
type Budget struct {
	MaxPerDay    int  `json:"max_per_day"`
	MinGapSecs   int  `json:"min_gap_secs"`
	QuietFromHr  int  `json:"quiet_from_hour"` // local hour, inclusive
	QuietToHr    int  `json:"quiet_to_hour"`   // local hour, exclusive
	QuietEnabled bool `json:"quiet_enabled"`
}

// DefaultPolicy is what a fresh install runs: on, minimal, gentle budget.
func DefaultPolicy() Policy {
	return Policy{
		Mode:   ModeMinimal,
		Spaces: map[string]Scope{},
		Budget: Budget{MaxPerDay: 12, MinGapSecs: 120},
	}
}

// ScopeFor resolves the effective scope of a space. An unset mode reads as
// Minimal rather than as "off" — a fresh install should still catch a direct
// question, and silence must be a choice, not a zero value.
func (p Policy) ScopeFor(spaceHex string) Scope {
	if p.Mode == ModeOff {
		return ScopeOff
	}
	if s, ok := p.Spaces[spaceHex]; ok && s != "" {
		return s
	}
	return ScopeHighlights
}

// budgetState is the runtime side of Budget: what has already been shown.
type budgetState struct {
	dayStart  int64
	dayCount  int
	lastAtUTC int64
}

// admit decides whether a priority-worthy signal may actually be priority
// right now, or must step down to digest. Time comes from received_at (the
// local fact) — an author's clock must not be able to sneak past quiet hours.
func (b *Budget) admit(st *budgetState, receivedAt int64, loc *time.Location) bool {
	if receivedAt == 0 {
		receivedAt = time.Now().Unix()
	}
	t := time.Unix(receivedAt, 0)
	if loc != nil {
		t = t.In(loc)
	}
	if b.QuietEnabled && inQuietHours(t.Hour(), b.QuietFromHr, b.QuietToHr) {
		return false
	}
	day := t.Truncate(24 * time.Hour).Unix()
	if st.dayStart != day {
		st.dayStart, st.dayCount = day, 0
	}
	if b.MaxPerDay > 0 && st.dayCount >= b.MaxPerDay {
		return false
	}
	if b.MinGapSecs > 0 && st.lastAtUTC != 0 &&
		receivedAt-st.lastAtUTC < int64(b.MinGapSecs) {
		return false
	}
	st.dayCount++
	st.lastAtUTC = receivedAt
	return true
}

// inQuietHours handles windows that wrap past midnight (22 → 8).
func inQuietHours(hour, from, to int) bool {
	if from == to {
		return false
	}
	if from < to {
		return hour >= from && hour < to
	}
	return hour >= from || hour < to
}
