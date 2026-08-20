package terminals

import "testing"

// The glyph map rides the manifest labels and comes back exactly — and a
// glyph for a state the vocabulary does not declare is dropped on read,
// the same read-side honesty admissiblePresence applies to the states.
func TestPresenceGlyphsRideTheLabelsAndStayHonest(t *testing.T) {
	c := DefaultCharacter("campfire")
	c.Presence = append(c.Presence, "brewing_tea")
	c.PresenceGlyphs = map[string]string{
		"open_to_talk": "💬",
		"brewing_tea":  "🍵",
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	title, got := ParseCharacter(c.Labels("the yard"))
	if title != "the yard" {
		t.Fatalf("title mangled: %q", title)
	}
	if got.PresenceGlyphs["open_to_talk"] != "💬" || got.PresenceGlyphs["brewing_tea"] != "🍵" {
		t.Fatalf("glyphs did not survive the trip: %+v", got.PresenceGlyphs)
	}

	// A hand-built label naming an undeclared state: the glyph is dropped.
	_, sneaky := ParseCharacter([]string{"x", "qp.presence=around", "qp.presence_glyphs=around:🌿,admin:👑"})
	if sneaky.PresenceGlyphs["around"] != "🌿" {
		t.Fatalf("the declared glyph was lost: %+v", sneaky.PresenceGlyphs)
	}
	if _, ok := sneaky.PresenceGlyphs["admin"]; ok {
		t.Fatal("a glyph for an undeclared state survived the read")
	}

	// Markup is not a symbol: Validate refuses it.
	c.PresenceGlyphs["brewing_tea"] = "<svg onload=x>"
	if err := c.Validate(); err == nil {
		t.Fatal("markup admitted as a presence glyph")
	}

	// A glyph may not name a state that is not there.
	c.PresenceGlyphs = map[string]string{"not_a_state": "🌊"}
	if err := c.Validate(); err == nil {
		t.Fatal("a glyph for an undeclared state validated")
	}
}
