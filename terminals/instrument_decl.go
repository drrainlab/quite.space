package terminals

// The instrument channel declaration — the SIGNED half of the Instrument
// Plane contract (QI-0). An instrument's manifest is the
// single source of truth for what its channels MEAN (kind, unit, label);
// the observation frames carry only channel + value, so a frame can never
// contradict the declaration and a receiver never has to pick a winner.
//
// WHY "instrument" AND NOT "terminal": in this protocol a Terminal is a
// SPACE (id.TerminalID), and the relay lesson applies — one word carrying
// two load-bearing meanings is a six-month debugging tax. The user-facing
// label may still read «Терминалы»/«Инструменты»; the code never says
// terminal about a device.
//
// Grammar, riding the manifest's qp.* label convention (character.go):
//
//	qp.instr=<channel>:<kind>[:<unit>][:<label>]
//
// channel — live_signal's param grammar (^[a-z][a-z0-9_-]{0,31}$);
// kind    — number | boolean | enum (a closed v1 set; parsers keep
//           unknown kinds as declarations-without-renderers, ADR-009);
// unit and label — PERCENT-ESCAPED (owner's amendment 4): "°C", "%",
// "Температура: улица" and CJK all survive the colon-separated form
// because ':' and '%' are escaped on write and unescaped on read.

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/drrainlab/quiet_places/protocol/manifest"
)

// MaxInstrumentChannels bounds one instrument's panel. Deliberately its
// OWN limit with its own refusal: the manifest-wide MaxLabels=32 would
// otherwise make the channel budget depend on which other qp.* labels
// happen to exist, and the failure would read as a broken manifest rather
// than as "too many channels".
const MaxInstrumentChannels = 24

const instrLabel = charPrefix + "instr" // qp.instr

var instrChannelRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// ChannelDecl is one declared instrument channel.
type ChannelDecl struct {
	Channel string // instrChannelRe
	Kind    string // "number" | "boolean" | "enum" (open set on read)
	Unit    string // display unit, optional ("°C", "%", "ppm")
	Label   string // human label, optional; Channel shown when empty
}

// instrEscape keeps ':' and '%' out of the separator's way — and ONLY
// them: '%' first (or the escape would escape itself), then the colon.
// Everything else, CJK included, rides raw; a manifest label is UTF-8.
// The first version piped through url.PathEscape after the colon
// replacement and double-escaped its own '%3A' — the test caught the
// label coming back as "Температура%3A улица".
func instrEscape(s string) string {
	s = strings.ReplaceAll(s, "%", "%25")
	return strings.ReplaceAll(s, ":", "%3A")
}

func instrUnescape(s string) string {
	out, err := url.PathUnescape(s)
	if err != nil {
		return s // tolerant read: a bad escape shows raw, never breaks
	}
	return out
}

// InstrumentLabels encodes channel declarations as manifest labels.
// Refuses over-budget and malformed declarations OUTRIGHT — the producer
// is the strict layer; the parser below is the tolerant one.
func InstrumentLabels(channels []ChannelDecl) ([]string, error) {
	if len(channels) > MaxInstrumentChannels {
		return nil, fmt.Errorf("terminals: too_many_instrument_channels (%d > %d)",
			len(channels), MaxInstrumentChannels)
	}
	out := make([]string, 0, len(channels))
	seen := map[string]bool{}
	for _, c := range channels {
		if !instrChannelRe.MatchString(c.Channel) {
			return nil, fmt.Errorf("terminals: instrument channel %q does not parse", c.Channel)
		}
		if seen[c.Channel] {
			return nil, fmt.Errorf("terminals: instrument channel %q declared twice", c.Channel)
		}
		seen[c.Channel] = true
		switch c.Kind {
		case "number", "boolean", "enum":
		default:
			return nil, fmt.Errorf("terminals: instrument kind %q is not declared in v1", c.Kind)
		}
		l := instrLabel + "=" + c.Channel + ":" + c.Kind
		if c.Unit != "" || c.Label != "" {
			l += ":" + instrEscape(c.Unit)
		}
		if c.Label != "" {
			l += ":" + instrEscape(c.Label)
		}
		out = append(out, l)
	}
	return out, nil
}

// ParseInstruments reads channel declarations back off a manifest's
// labels. Tolerant, in the ParseCharacter spirit: unknown kinds are KEPT
// (a declaration is knowledge even when this build has no renderer for
// it); malformed entries are skipped, never fatal; the count is clamped
// to the budget so a hostile manifest cannot inflate a panel.
func ParseInstruments(labels []string) []ChannelDecl {
	var out []ChannelDecl
	seen := map[string]bool{}
	for _, l := range labels {
		if !strings.HasPrefix(l, instrLabel+"=") {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(l, instrLabel+"="), ":")
		if len(parts) < 2 {
			continue
		}
		ch := parts[0]
		if !instrChannelRe.MatchString(ch) || seen[ch] {
			continue
		}
		d := ChannelDecl{Channel: ch, Kind: parts[1]}
		if len(parts) > 2 {
			d.Unit = instrUnescape(parts[2])
		}
		if len(parts) > 3 {
			d.Label = instrUnescape(strings.Join(parts[3:], ":"))
		}
		seen[ch] = true
		out = append(out, d)
		if len(out) >= MaxInstrumentChannels {
			break
		}
	}
	return out
}

// IsInstrumentKind reports whether a manifest kind names an instrument —
// the projection split (owner's amendment 6): instruments are subjects of
// the space, but they are not people, and they never appear in the
// members list twice.
func IsInstrumentKind(kind manifest.Kind) bool {
	return kind == manifest.KindSensor || kind == manifest.KindActuator
}

