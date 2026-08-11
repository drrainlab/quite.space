// Space Character (user vision: "you don't decorate the room — you define
// how it senses presence, what it values, and how it remembers your shared
// life"). Character is not a theme file: it is the space's own declared
// labels in its signed manifest, so every replica renders the same feel and
// nothing about it is hidden client state.
//
// Hard rule carried from the honesty invariants: character can style the
// room, never the truth — custom presence states must not impersonate
// system properties, and no styling reaches authorship or capability
// badges.
package terminals

import (
	"errors"
	"strings"
)

// Archetypes (v1 set). An archetype seeds the scene, blocks, and suggested
// presence forms; it never limits what the Terminal can do.
var Archetypes = []string{
	"campfire", "forest", "studio", "workshop", "orbit", "home", "radio_room",
}

// Character is the parsed space character.
type Character struct {
	Archetype string   // campfire | forest | studio | workshop | orbit | home | radio_room
	Mood      string   // dawn | day | dusk | night
	Material  string   // light | wood | glass | metal | fog | paper
	Motion    string   // still | slow | lively
	Geometry  string   // fire | table | islands | orbit | corridor
	Central   string   // chat | objects | audio | members | telemetry
	Memory    string   // everything | manual | private_history
	Relic     string   // ember_ring | tree | constellation | structure | garden | waveform
	Rituals   []string // up to 3 chosen ritual slugs
	Presence  []string // the space's presence vocabulary
}

// DefaultCharacter returns the archetype's seed character.
func DefaultCharacter(archetype string) Character {
	c := Character{
		Archetype: archetype, Mood: "dusk", Material: "light",
		Motion: "slow", Geometry: "fire", Central: "chat",
		Memory: "everything", Relic: "ember_ring",
		Presence: []string{"around", "listening", "open_to_talk", "busy", "quiet_today", "do_not_disturb"},
	}
	switch archetype {
	case "forest":
		c.Material, c.Geometry, c.Relic = "wood", "islands", "tree"
	case "studio":
		c.Central, c.Geometry, c.Relic = "audio", "table", "waveform"
		c.Presence = append(c.Presence, "mixing_a_track")
	case "workshop":
		c.Central, c.Geometry, c.Material, c.Relic = "objects", "table", "metal", "structure"
		c.Presence = append(c.Presence, "in_the_shop", "ready_to_test")
	case "orbit":
		c.Geometry, c.Material, c.Motion, c.Relic = "orbit", "glass", "still", "constellation"
	case "home":
		c.Mood, c.Geometry, c.Material, c.Relic = "day", "fire", "paper", "garden"
	case "radio_room":
		c.Central, c.Material, c.Relic = "telemetry", "metal", "waveform"
		c.Presence = append(c.Presence, "monitoring_the_air")
	}
	return c
}

// maxPresenceStates bounds a space's presence vocabulary. Enforced when a
// character is written (Validate) AND when one is read off the wire
// (admissiblePresence) — the second is what makes it a bound rather than
// a request.
const maxPresenceStates = 12

// reservedPresence lists states custom presence must not impersonate:
// these look like protocol facts, and character may never fake those
// (invariant §2.4; user's rule about custom statuses).
var reservedPresence = []string{
	"online", "offline", "verified", "delivered", "read", "received",
	"acknowledged", "admin", "system", "human", "trusted",
}

// ValidatePresenceState rejects impersonating or unusable states. Matching
// is per word token, so "reading_not_replying" is fine while "always
// online" is not.
func ValidatePresenceState(state string) error {
	s := strings.ToLower(strings.TrimSpace(state))
	if s == "" || len(s) > 40 {
		return errors.New("terminals: presence state empty or too long")
	}
	tokens := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '_' || r == '-' || r == '.' || r == '/'
	})
	for _, tok := range tokens {
		for _, r := range reservedPresence {
			if tok == r {
				return errors.New("terminals: presence state may not impersonate system properties (" + r + ")")
			}
		}
	}
	return nil
}

// label prefix for character entries inside manifest DeclaredLabels.
const charPrefix = "qp."

// Labels encodes title + character as manifest declared labels.
func (c Character) Labels(title string) []string {
	out := []string{title}
	add := func(k, v string) {
		if v != "" {
			out = append(out, charPrefix+k+"="+v)
		}
	}
	add("archetype", c.Archetype)
	add("mood", c.Mood)
	add("material", c.Material)
	add("motion", c.Motion)
	add("geometry", c.Geometry)
	add("central", c.Central)
	add("memory", c.Memory)
	add("relic", c.Relic)
	for _, r := range c.Rituals {
		add("ritual", r)
	}
	if len(c.Presence) > 0 {
		add("presence", strings.Join(c.Presence, ","))
	}
	return out
}

// ParseCharacter decodes labels back into (title, character). Unknown
// labels are kept out of the character but never break parsing (ADR-009
// spirit).
func ParseCharacter(labels []string) (string, Character) {
	title := ""
	c := DefaultCharacter("campfire")
	c.Rituals = nil
	for _, l := range labels {
		if !strings.HasPrefix(l, charPrefix) {
			if title == "" {
				title = l
			}
			continue
		}
		kv := strings.SplitN(strings.TrimPrefix(l, charPrefix), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "archetype":
			c.Archetype = kv[1]
		case "mood":
			c.Mood = kv[1]
		case "material":
			c.Material = kv[1]
		case "motion":
			c.Motion = kv[1]
		case "geometry":
			c.Geometry = kv[1]
		case "central":
			c.Central = kv[1]
		case "memory":
			c.Memory = kv[1]
		case "relic":
			c.Relic = kv[1]
		case "ritual":
			if len(c.Rituals) < 3 {
				c.Rituals = append(c.Rituals, kv[1])
			}
		case "presence":
			c.Presence = admissiblePresence(strings.Split(kv[1], ","))
		}
	}
	return title, c
}

// admissiblePresence keeps the states a reader may honestly show.
//
// The vocabulary is authored by whoever owns the space and arrives inside
// a manifest, whose own Validate bounds how MANY labels there are and
// nothing about what is in them. Character.Validate holds the real rule —
// no state may impersonate a protocol fact (invariant §2.4) — and it runs
// only when a character is WRITTEN. So until this existed, a space could
// declare a presence state of "verified" or "system" and every reader
// would render it beside the honest ones, which is precisely the
// impersonation reservedPresence was introduced to prevent.
//
// Individual states are dropped, never the manifest: refusing a whole
// space over one bad word would make content disappear, and this
// codebase's rule on the read path is the same one qp.kind follows — an
// unknown or inadmissible VALUE is ignored, never fatal. A space that
// declares nothing but reserved words ends up with no vocabulary, which
// reads correctly as "this space declares no presence states".
//
// The cap matches Validate's own, so a hostile manifest cannot hand every
// reader ten thousand menu items to build.
func admissiblePresence(states []string) []string {
	var out []string
	for _, s := range states {
		if len(out) >= maxPresenceStates {
			break
		}
		if ValidatePresenceState(s) != nil {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Validate keeps the character within the v1 envelope and enforces the
// presence honesty rule.
func (c Character) Validate() error {
	found := false
	for _, a := range Archetypes {
		if c.Archetype == a {
			found = true
		}
	}
	if !found {
		return errors.New("terminals: unknown archetype")
	}
	if len(c.Rituals) > 3 {
		return errors.New("terminals: at most three rituals (they define life here — choose)")
	}
	if len(c.Presence) > maxPresenceStates {
		return errors.New("terminals: presence vocabulary too large")
	}
	for _, p := range c.Presence {
		if err := ValidatePresenceState(p); err != nil {
			return err
		}
	}
	return nil
}
