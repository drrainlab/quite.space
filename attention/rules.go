package attention

import (
	"slices"
	"strings"
	"unicode"
)

// Deterministic rules run before any statistics and are the only part of
// QuietRank that is allowed to be certain.
//
// The lexicons below are PREFIXES, not whole words: Russian inflects heavily
// ("посмотришь / посмотрите / посмотри"), and prefix matching on a tokenized
// text covers that without shipping a morphology engine. Tokenization stops
// at non-letter runes, so "надо" cannot fire inside "надоел".

// Each lexicon comes in two halves, and the split is the whole trick.
//
// EXACT entries must match a token outright. Short Russian words are
// dangerous as prefixes: "надо" would fire inside "надоел", "как" inside
// "какао". EN function words have the same problem ("is" in "island").
//
// PREFIX entries are long enough that a prefix is safe, and they buy us
// Russian inflection for free: "подтверд" covers подтверди / подтвердишь /
// подтвердите without shipping a morphology engine.

var questionExact = []string{
	// RU
	"кто", "что", "чего", "когда", "где", "куда", "почему", "зачем",
	"сколько", "как", "какой", "какая", "какие", "можешь", "сможешь",
	"можете", "сможете", "успеешь", "готово",
	// EN
	"who", "what", "when", "where", "why", "how", "which", "can", "could",
	"would", "should", "does", "did",
}

var questionPrefix = []string{
	// RU — inflection-friendly stems
	"подскаж", "посмотр", "провер", "уточн",
}

var actionExact = []string{
	// RU
	"надо", "сделай", "сделать", "жду", "срочно", "дедлайн",
	"пожалуйста", "прошу", "давай", "блокер",
	// EN
	"need", "needs", "please", "asap", "urgent", "blocker", "blocked",
	"review", "action", "required", "ping", "fix",
}

var actionPrefix = []string{
	// RU — "нужен / нужна / нужно / нужны" all ask the same thing, and a
	// live run showed that listing only "нужно" as an exact word silently
	// missed "нужен твой взгляд". "требу" likewise covers требуется/требуют.
	// "требуе/требую" and not bare "требу", which also opens "требуха".
	"нуж", "требуе", "требую", "подтверд", "напомн", "перенес", "исправ",
	// EN
	"confirm", "deadline", "remind",
}

// deadlinePhrases are multi-word deadline hints checked on the raw text.
var deadlinePhrases = []string{
	"до вечера", "до завтра", "до утра", "до пятницы", "до понедельника",
	"к вечеру", "к завтра", "сегодня до", "by tonight", "by tomorrow",
	"by friday", "by monday", "end of day", "eod ", "today by",
}

// Detect runs the deterministic layer. It returns the reasons found and
// whether any of them is HARD — a fact about addressing that the statistical
// layer must never be able to hide.
func Detect(c Candidate, v Viewer) (reasons []Reason, hard bool) {
	// --- HARD: facts, not guesses ---
	if slices.Contains(c.Mentions, v.Principal) {
		reasons = append(reasons, Reason{Code: ReasonMention, Exact: true})
		hard = true
	}
	// A reply edge only exists on real messages: the revision schema reuses
	// the same wire field for a different meaning, so callers must not hand
	// us revisions as text candidates.
	if c.ReplyTo != nil && v.AuthoredByMe != nil && v.AuthoredByMe(*c.ReplyTo) {
		reasons = append(reasons, Reason{Code: ReasonReplyToMe, Exact: true})
		hard = true
	}

	// --- SOFT: signals a personal model may demote ---
	lower := strings.ToLower(c.Text)
	tokens := tokenize(lower)

	for _, alias := range v.Aliases {
		a := strings.ToLower(strings.TrimSpace(alias))
		if a == "" {
			continue
		}
		if hasToken(tokens, a) {
			reasons = append(reasons, Reason{
				Code: ReasonNameInText, Detail: alias, Exact: true,
			})
			break
		}
	}
	if strings.Contains(c.Text, "?") ||
		matchesExact(tokens, questionExact) || matchesPrefix(tokens, questionPrefix) {
		reasons = append(reasons, Reason{Code: ReasonQuestion, Exact: true})
	}
	if matchesExact(tokens, actionExact) || matchesPrefix(tokens, actionPrefix) ||
		containsAny(lower, deadlinePhrases) {
		reasons = append(reasons, Reason{Code: ReasonAction, Exact: true})
	}
	return reasons, hard
}

// DetectWatched reports watched phrases found in the text. Anchors work this
// way until the semantic encoder lands (AT-0B) — same object, same setting,
// literal matching for now.
func DetectWatched(text string, phrases []string) []Reason {
	lower := strings.ToLower(text)
	var out []Reason
	seen := map[string]bool{}
	for _, p := range phrases {
		q := strings.ToLower(strings.TrimSpace(p))
		if q == "" || seen[q] {
			continue
		}
		if strings.Contains(lower, q) {
			seen[q] = true
			out = append(out, Reason{Code: ReasonWatched, Detail: p, Exact: true})
		}
	}
	return out
}

// tokenize splits on anything that is not a letter or digit, so lexicon
// prefixes match word starts rather than arbitrary substrings.
func tokenize(lower string) []string {
	return strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// matchesExact reports whether any token equals a lexicon word.
func matchesExact(tokens, lex []string) bool {
	for _, t := range tokens {
		if slices.Contains(lex, t) {
			return true
		}
	}
	return false
}

// matchesPrefix reports whether any token starts with a lexicon stem.
func matchesPrefix(tokens, lex []string) bool {
	for _, t := range tokens {
		for _, p := range lex {
			if strings.HasPrefix(t, p) {
				return true
			}
		}
	}
	return false
}

// maxAliasSuffix is how much inflection an alias may pick up. Two runes
// covers the Russian cases that matter for addressing someone ("Глебу",
// "Глебом") while refusing to let "Ян" swallow "январь".
const maxAliasSuffix = 2

// hasToken reports whether the alias addresses someone in this text. Short
// aliases demand an exact token — a two-letter name as a prefix would match
// half the dictionary.
func hasToken(tokens []string, alias string) bool {
	aliasRunes := len([]rune(alias))
	for _, t := range tokens {
		if t == alias {
			return true
		}
		if aliasRunes < 4 || !strings.HasPrefix(t, alias) {
			continue
		}
		if len([]rune(t))-aliasRunes <= maxAliasSuffix {
			return true
		}
	}
	return false
}

func containsAny(lower string, phrases []string) bool {
	for _, p := range phrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}
